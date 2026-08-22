package tenancy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestCanonicalHost(t *testing.T) {
	tests := map[string]string{
		"auth.example.com":      "auth.example.com",
		"AUTH.EXAMPLE.COM.":     "auth.example.com",
		"auth.example.com:8080": "auth.example.com",
		"[2001:db8::1]:8443":    "2001:db8::1",
	}
	for input, want := range tests {
		if got := canonicalHost(input); got != want {
			t.Errorf("canonicalHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMiddlewareAllowsGlobalOIDCRoutesWithoutTenant(t *testing.T) {
	resolver := &testResolver{}
	h := Middleware(resolver)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/oauth2/keys", "/.well-known/openid-configuration"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "authincore"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Errorf("%s status = %d, want %d", path, w.Code, http.StatusNoContent)
		}
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolver.calls)
	}
}

type testResolver struct{ calls int }

func (r *testResolver) ByDomain(_ context.Context, _ string) (*Tenant, error) {
	r.calls++
	return &Tenant{OrgID: uuid.New()}, nil
}

func (r *testResolver) ByOrgID(_ context.Context, _ uuid.UUID) (*Tenant, error) {
	return nil, ErrInvalidDomain
}
