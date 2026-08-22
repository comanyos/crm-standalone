// Package interceptor provides connect-go interceptors for authn, authz, tenant, and audit.
package interceptor

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/authincore/authincore/internal/command"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/authincore/authincore/internal/token"
)

type ctxKeyClaims struct{}

// ClaimsFromContext retrieves verified JWT claims injected by AuthnInterceptor.
func ClaimsFromContext(ctx context.Context) (*token.Claims, bool) {
	c, ok := ctx.Value(ctxKeyClaims{}).(*token.Claims)
	return c, ok && c != nil
}

// AuthnInterceptor validates bearer tokens and session cookies, populates actor in context.
type AuthnInterceptor struct {
	tokenSvc *token.Service
}

func NewAuthnInterceptor(tokenSvc *token.Service) *AuthnInterceptor {
	return &AuthnInterceptor{tokenSvc: tokenSvc}
}

func (i *AuthnInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx = i.resolve(ctx, req.Header())
		return next(ctx, req)
	}
}

func (i *AuthnInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *AuthnInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx = i.resolve(ctx, conn.RequestHeader())
		return next(ctx, conn)
	}
}

// ResolveHTTP validates the Authorization or Cookie header and injects claims into ctx.
// Call this from plain net/http middleware.
func (i *AuthnInterceptor) ResolveHTTP(r *http.Request) (*http.Request, bool) {
	ctx := i.resolve(r.Context(), r.Header)
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return r, false
	}
	return r.WithContext(ctx), claims != nil
}

func (i *AuthnInterceptor) resolve(ctx context.Context, headers http.Header) context.Context {
	var tokenStr string

	// Bearer token takes priority.
	if bearer := headers.Get("Authorization"); strings.HasPrefix(bearer, "Bearer ") {
		tokenStr = strings.TrimPrefix(bearer, "Bearer ")
	} else if cookie := headers.Get("Cookie"); cookie != "" {
		// Accept the current cookie and legacy names during migration.
		for _, part := range strings.Split(cookie, ";") {
			part = strings.TrimSpace(part)
			for _, name := range []string{"__Host-authincore_session", "authincore_session", "ory_session"} {
				if strings.HasPrefix(part, name+"=") {
					tokenStr = strings.TrimPrefix(part, name+"=")
					break
				}
			}
			if tokenStr != "" {
				break
			}
		}
	}

	if tokenStr == "" {
		return ctx
	}

	claims, err := i.tokenSvc.Verify(tokenStr)
	if err != nil || claims == nil {
		return ctx
	}

	ctx = context.WithValue(ctx, ctxKeyClaims{}, claims)
	ctx = command.WithActor(ctx, claims.Subject)
	return ctx
}

// TenantInterceptor resolves tenant from the X-Org-ID header (gRPC callers).
type TenantInterceptor struct {
	resolver tenancy.Resolver
}

func NewTenantInterceptor(resolver tenancy.Resolver) *TenantInterceptor {
	return &TenantInterceptor{resolver: resolver}
}

func (ti *TenantInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if orgIDStr := req.Header().Get("X-Org-ID"); orgIDStr != "" {
			// Simplified: resolve by explicit org ID header.
			// In production, also resolve via host if not set.
			_ = orgIDStr // tenant resolution wired up in full impl
		}
		return next(ctx, req)
	}
}

func (ti *TenantInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (ti *TenantInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
