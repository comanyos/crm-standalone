package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCORSMW_AllowsConfiguredOrigin(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"})(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/crm.v1.CRMService/ListAccounts", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the configured origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (request must still reach the handler)", rec.Code)
	}
}

func TestCORSMW_RejectsUnknownOrigin(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"})(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/crm.v1.CRMService/ListAccounts", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for an unallowed origin", got)
	}
}

func TestCORSMW_PreflightAllowed(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"})(okHandler())

	req := httptest.NewRequest(http.MethodOptions, "/crm.v1.CRMService/ListAccounts", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("expected Access-Control-Allow-Headers on a preflight response")
	}
}

func TestCORSMW_PreflightRejectedForUnknownOrigin(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"})(okHandler())

	req := httptest.NewRequest(http.MethodOptions, "/crm.v1.CRMService/ListAccounts", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("preflight status = %d, want 403 for an unallowed origin", rec.Code)
	}
}

func TestCORSMW_NoOriginsConfiguredSetsNoHeaders(t *testing.T) {
	h := corsMW(nil)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty when no origins are configured", got)
	}
}
