package tenancy

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// failingResolver rejects every host, so the middleware takes its warn+404
// path.
type failingResolver struct{}

func (failingResolver) ByDomain(_ context.Context, _ string) (*Tenant, error) {
	return nil, ErrInvalidDomain
}

func (failingResolver) ByOrgID(_ context.Context, _ uuid.UUID) (*Tenant, error) {
	return nil, ErrInvalidDomain
}

// The Host header is caller-supplied and names the tenant. A failed
// resolution must neither log it nor echo it back: the access-log policy
// excludes headers and tenant identifiers, and reflecting the value returns
// unvalidated request data to whoever sent it.
func TestFailedTenantResolutionDoesNotLogOrReflectTheHost(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const hostileHost = "tenant-a-secret.internal.example"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/identities", nil)
	req.Host = hostileHost
	rec := httptest.NewRecorder()

	Middleware(failingResolver{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran despite a failed tenant resolution")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if strings.Contains(rec.Body.String(), hostileHost) {
		t.Fatalf("response reflected the Host header: %s", rec.Body.String())
	}
	if strings.Contains(logged.String(), hostileHost) {
		t.Fatalf("log line carried the Host header: %s", logged.String())
	}
}
