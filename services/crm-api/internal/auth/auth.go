// Package auth verifies AuthinCore JWTs (via JWKS) and enforces per-RPC scopes through
// a connect-go interceptor. Modules never run their own login — they verify the
// platform's tokens (see ../ideas/PLATFORM-MODULE-CONTRACT.md §3).
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/MicahParks/jwkset"
	"github.com/santoshguru/crm/internal/metrics"
)

// Principal is the authenticated caller, derived from JWT claims.
type Principal struct {
	Subject  string
	TenantID string
	OrgID    string
	Roles    []string
	Scopes   []string
}

// HasScope reports whether the principal holds a scope (or the wildcard "crm:*").
func (p *Principal) HasScope(s string) bool {
	for _, x := range p.Scopes {
		if x == s || x == "crm:*" {
			return true
		}
	}
	return false
}

type principalKey struct{}

// WithPrincipal stores the principal in the context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the authenticated principal, if any.
func FromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// verifier validates a raw token and returns its principal.
type verifier interface {
	verify(ctx context.Context, token string) (*Principal, error)
}

// Authenticator authenticates requests and enforces scopes.
type Authenticator struct {
	v        verifier
	disabled bool

	// storage and jwksURL back Ready (jwks.go). They are nil for a disabled
	// authenticator, which has no key set to report on.
	storage jwkset.Storage
	jwksURL string
}

// devPrincipal is injected when auth is disabled (local dev).
var devPrincipal = &Principal{
	Subject: "dev", TenantID: "default", OrgID: "default",
	Roles: []string{"SUPER_ADMIN"}, Scopes: []string{"crm:*"},
}

// Disabled returns an Authenticator that allows all requests (dev only).
func Disabled() *Authenticator { return &Authenticator{disabled: true} }

// FromConfig builds an Authenticator: JWKS-backed if jwksURL is set, else disabled.
func FromConfig(ctx context.Context, jwksURL, issuer, audience string) (*Authenticator, error) {
	if jwksURL == "" {
		slog.Warn("AUTH_JWKS_URL not set — auth DISABLED; all requests run as a dev super-admin")
		return Disabled(), nil
	}
	return newFromJWKS(ctx, jwksURL, issuer, audience)
}

// Authenticate verifies an Authorization header and returns a context carrying the
// principal. When auth is disabled it injects the dev principal. For non-connect (raw
// HTTP) handlers such as CSV export; connect RPCs go through Interceptor instead.
func (a *Authenticator) Authenticate(ctx context.Context, authHeader string) (context.Context, error) {
	if a.disabled {
		return WithPrincipal(ctx, devPrincipal), nil
	}
	tok := bearer(authHeader)
	if tok == "" {
		return ctx, errors.New("missing bearer token")
	}
	p, err := a.v.verify(ctx, tok)
	if err != nil {
		return ctx, err
	}
	return WithPrincipal(ctx, p), nil
}

// ScopePolicy maps procedures to the scope a caller must hold, and denies
// anything it has not been told about.
//
// The previous shape was a bare map[string]string, which failed open: a
// procedure absent from the map was enforced as public, so adding a handler
// and forgetting its entry silently published it to every authenticated
// principal in every organization. Forgetting is the normal failure — it
// happens during a rushed change, and nothing about the running system looks
// wrong afterwards. So the missing entry now has to be the loud case.
//
// Unscoped is not a loophole around that: it is an allowlist whose values are
// the reason each procedure legitimately needs no scope. A procedure must
// appear in exactly one of the two maps, which server.assertPolicyIsTotal
// checks against the generated service descriptors at startup and in tests.
type ScopePolicy struct {
	// Required maps a procedure to the single scope it demands.
	Required map[string]string
	// Unscoped maps a procedure to why it needs no scope. Authentication is
	// still enforced; only the scope check is waived.
	Unscoped map[string]string
}

// Decide reports the scope a procedure requires. known is false when the
// policy has never heard of the procedure, which callers must treat as deny.
func (p ScopePolicy) Decide(procedure string) (scope string, known bool) {
	if scope, ok := p.Required[procedure]; ok {
		return scope, true
	}
	if _, ok := p.Unscoped[procedure]; ok {
		return "", true
	}
	return "", false
}

// Procedures returns every procedure the policy has a decision for.
func (p ScopePolicy) Procedures() []string {
	out := make([]string, 0, len(p.Required)+len(p.Unscoped))
	for procedure := range p.Required {
		out = append(out, procedure)
	}
	for procedure := range p.Unscoped {
		out = append(out, procedure)
	}
	sort.Strings(out)
	return out
}

// Interceptor returns a connect interceptor that authenticates every unary RPC
// and enforces the policy's scope decision for it.
func (a *Authenticator) Interceptor(policy ScopePolicy) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if a.disabled {
				return next(WithPrincipal(ctx, devPrincipal), req)
			}
			tok := bearer(req.Header().Get("Authorization"))
			if tok == "" {
				metrics.JWKSValidationFailuresTotal.WithLabelValues("missing_bearer_token").Inc()
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
			}
			p, err := a.v.verify(ctx, tok)
			if err != nil {
				metrics.JWKSValidationFailuresTotal.WithLabelValues("invalid_token").Inc()
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}

			procedure := req.Spec().Procedure
			scope, known := policy.Decide(procedure)
			if !known {
				// Deliberately vague to the caller and loud in the log: the
				// caller learns nothing about what exists, and the operator
				// learns a handler shipped without a policy entry.
				slog.Error("refusing an RPC with no scope policy entry", "procedure", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied,
					errors.New("this procedure is not available"))
			}
			// The scope check runs before the handler, so a caller without the
			// scope cannot use the response to learn whether a record exists.
			if scope != "" && !p.HasScope(scope) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("missing required scope %q", scope))
			}
			return next(WithPrincipal(ctx, p), req)
		}
	}
}

func bearer(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
