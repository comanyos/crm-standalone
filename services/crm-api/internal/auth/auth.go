// Package auth verifies AuthinCore JWTs (via JWKS) and enforces per-RPC scopes through
// a connect-go interceptor. Modules never run their own login — they verify the
// platform's tokens (see ../ideas/PLATFORM-MODULE-CONTRACT.md §3).
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
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

// Interceptor returns a connect interceptor that authenticates every unary RPC and,
// for procedures listed in policy, requires the mapped scope.
func (a *Authenticator) Interceptor(policy map[string]string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if a.disabled {
				return next(WithPrincipal(ctx, devPrincipal), req)
			}
			tok := bearer(req.Header().Get("Authorization"))
			if tok == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
			}
			p, err := a.v.verify(ctx, tok)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			if scope := policy[req.Spec().Procedure]; scope != "" && !p.HasScope(scope) {
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
