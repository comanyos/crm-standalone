// Package tenancy resolves the instance → org → project → app hierarchy
// from the incoming request (host header, JWT claim, or explicit header).
package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/authincore/authincore/internal/api/middleware"
	"github.com/google/uuid"
)

// Tenant carries the resolved tenant context for a request.
type Tenant struct {
	InstanceID uuid.UUID
	OrgID      uuid.UUID
	OrgSlug    string
	Domain     string
}

// Resolver resolves a tenant from a domain/slug.
type Resolver interface {
	ByDomain(ctx context.Context, domain string) (*Tenant, error)
	ByOrgID(ctx context.Context, orgID uuid.UUID) (*Tenant, error)
}

type ctxKeyTenant struct{}

// WithTenant injects a tenant into the context.
func WithTenant(ctx context.Context, t *Tenant) context.Context {
	return context.WithValue(ctx, ctxKeyTenant{}, t)
}

// FromContext retrieves the tenant from context.
func FromContext(ctx context.Context) (*Tenant, bool) {
	t, ok := ctx.Value(ctxKeyTenant{}).(*Tenant)
	return t, ok && t != nil
}

// MustFromContext panics if no tenant is in context (programming error, not runtime).
func MustFromContext(ctx context.Context) *Tenant {
	t, ok := FromContext(ctx)
	if !ok {
		panic("tenancy: no tenant in context")
	}
	return t
}

// Middleware resolves the tenant for each HTTP request from the Host header.
// Health probe paths (/healthz, /readyz) bypass tenant resolution.
func Middleware(resolver Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isGlobalRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			host := canonicalHost(r.Host)
			tenant, err := resolver.ByDomain(r.Context(), host)
			if err != nil {
				// The Host header is caller-supplied and identifies the
				// tenant, so it is neither logged nor reflected back: the
				// access-log policy excludes headers and tenant identifiers,
				// and echoing the value would return unvalidated request data
				// to whoever sent it. The request id already correlates this
				// warning with the access-log line that carries the route.
				slog.Warn("tenant resolution failed",
					"request_id", middleware.RequestIDFromContext(r.Context()))
				http.Error(w, "tenant not found", http.StatusNotFound)
				return
			}
			ctx := WithTenant(r.Context(), tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isGlobalRoute lists endpoints whose response is instance-wide rather than
// organization-specific. In particular, resource servers retrieve the JWKS
// over the internal Docker hostname, which is intentionally not an org domain.
// Requiring tenant resolution there turns a valid JWT setup into a startup 404.
func isGlobalRoute(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/oauth2/keys",
		"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server":
		return true
	default:
		return false
	}
}

func canonicalHost(authority string) string {
	return CanonicalHost(authority)
}

// CanonicalHost reduces a request authority to the form domains are stored in:
// lowercase, no port, no trailing root dot, IPv6 brackets removed.
//
// Requests are matched against stored domains by exact string equality, so the
// two must be produced by the same function. They were not: resolution
// canonicalised the Host while organization creation stored whatever it was
// given, so an org registered as "localhost:8080" could never be resolved from
// a request to localhost:8080 — every tenant silently 404'd.
func CanonicalHost(authority string) string {
	host := strings.TrimSpace(authority)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	// SplitHostPort strips the brackets from "[::1]:8080" but not from a bare
	// "[::1]", which has no port to split.
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// ErrInvalidDomain reports a domain that cannot be stored.
var ErrInvalidDomain = errors.New("invalid domain")

// CanonicalDomain validates and canonicalises a domain for persistence.
//
// It accepts what can appear in a Host header — a hostname, a hostname with
// port, an IPv4 literal, a bracketed IPv6 literal — and rejects anything that
// is a URL rather than an authority. A value like "https://acme.test/login"
// would be stored verbatim and then never match any request, so it is refused
// at write time instead of becoming an unreachable tenant nobody can explain.
func CanonicalDomain(input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidDomain)
	}
	// A scheme, path, query, fragment or credentials means the caller passed a
	// URL. Guessing which part was meant is how a wrong tenant gets stored.
	for _, bad := range []struct{ tok, why string }{
		{"://", "scheme"}, {"/", "path"}, {"?", "query"},
		{"#", "fragment"}, {"@", "credentials"}, {" ", "whitespace"},
	} {
		if strings.Contains(raw, bad.tok) {
			return "", fmt.Errorf("%w: %q contains a %s; store the host only", ErrInvalidDomain, input, bad.why)
		}
	}

	host := CanonicalHost(raw)
	if host == "" {
		return "", fmt.Errorf("%w: %q has no host", ErrInvalidDomain, input)
	}
	if ip := net.ParseIP(host); ip != nil {
		// IPv6 canonicalises through the parser, so ::0001 and ::1 agree.
		return strings.ToLower(ip.String()), nil
	}
	if !validHostname(host) {
		return "", fmt.Errorf("%w: %q is not a hostname or IP literal", ErrInvalidDomain, input)
	}
	return host, nil
}

// validHostname applies the label rules a stored domain has to satisfy.
func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !isAlnum && r != '-' {
				return false
			}
		}
	}
	return true
}
