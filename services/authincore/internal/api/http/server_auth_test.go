package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/authincore/authincore/internal/api/interceptor"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

func newAuthTestServer(t *testing.T, issuer string) (*Server, *token.Service) {
	t.Helper()
	signer, err := token.NewRS256Signer(nil, "test-key")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	tokenSvc := token.NewService(signer, issuer, time.Minute, time.Hour)
	return &Server{deps: Deps{Authn: interceptor.NewAuthnInterceptor(tokenSvc)}}, tokenSvc
}

func TestRequireAuthEnforcesTenantMatch(t *testing.T) {
	const issuer = "https://auth.example.test"
	server, tokenSvc := newAuthTestServer(t, issuer)
	orgA, orgB := uuid.New(), uuid.New()
	accessToken, err := tokenSvc.MintAccessToken(
		t.Context(), uuid.New(), orgA, uuid.New(), nil, nil, "", 1,
	)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	handlerCalled := false
	handler := server.requireAuth(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("matching organization is allowed", func(t *testing.T) {
		handlerCalled = false
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req = req.WithContext(tenancy.WithTenant(req.Context(), &tenancy.Tenant{OrgID: orgA}))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusNoContent || !handlerCalled {
			t.Fatalf("status=%d called=%v, want 204 and called", rec.Code, handlerCalled)
		}
	})

	t.Run("different organization is forbidden", func(t *testing.T) {
		handlerCalled = false
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req = req.WithContext(tenancy.WithTenant(req.Context(), &tenancy.Tenant{OrgID: orgB}))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusForbidden || handlerCalled {
			t.Fatalf("status=%d called=%v, want 403 and not called", rec.Code, handlerCalled)
		}
	})
}

func TestRequirePermissionAcceptsMatchingTokenScope(t *testing.T) {
	const issuer = "https://auth.example.test"
	server, tokenSvc := newAuthTestServer(t, issuer)
	orgID := uuid.New()
	accessToken, err := tokenSvc.MintAccessToken(
		t.Context(), uuid.New(), orgID, uuid.New(), nil, []string{"identity:read"}, "", 1,
	)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	called := false
	handler := server.requirePermission("identity", "read", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/identities", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req = req.WithContext(tenancy.WithTenant(req.Context(), &tenancy.Tenant{OrgID: orgID}))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("status=%d called=%v, want 204 and called", rec.Code, called)
	}
}
