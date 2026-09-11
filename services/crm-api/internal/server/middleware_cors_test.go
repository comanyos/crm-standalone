package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestCORSMW_AllowsConfiguredOrigin(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"}, nil)(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/crm.v1.CRMService/ListAccounts", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the configured origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want true for cookie auth", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (request must still reach the handler)", rec.Code)
	}
}

func TestCORSMW_RejectsUnknownOrigin(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"}, nil)(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/crm.v1.CRMService/ListAccounts", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for an unallowed origin", got)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 so a cookie-authenticated mutation cannot execute", rec.Code)
	}
}

func TestCORSMW_PreflightAllowed(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"}, nil)(okHandler())

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
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "traceparent") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want traceparent", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PATCH") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PATCH for administration updates", got)
	}
}

func TestCORSMW_PreflightRejectedForUnknownOrigin(t *testing.T) {
	h := corsMW([]string{"https://app.example.com"}, nil)(okHandler())

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
	h := corsMW(nil, nil)(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty when no origins are configured", got)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when no browser origins are approved", rec.Code)
	}
}

func TestCORSMW_AuthOriginIsLimitedToPublicLoginRoutes(t *testing.T) {
	h := corsMW([]string{"https://crm.example.com"}, []string{"https://www.example.com"})(okHandler())
	tests := []struct {
		name, method, path string
		wantStatus         int
	}{
		{"request code", http.MethodPost, "/auth/email/request", http.StatusOK},
		{"verify code", http.MethodPost, "/auth/email/verify", http.StatusOK},
		{"start Google", http.MethodGet, "/auth/google/start", http.StatusOK},
		{"capabilities", http.MethodGet, "/auth/capabilities", http.StatusOK},
		{"inspect session", http.MethodGet, "/auth/session", http.StatusForbidden},
		{"logout", http.MethodPost, "/auth/logout", http.StatusForbidden},
		{"administration", http.MethodGet, "/auth/admin/organizations", http.StatusForbidden},
		{"CRM RPC", http.MethodPost, "/crm.v1.CRMService/ListAccounts", http.StatusForbidden},
		{"wrong method", http.MethodGet, "/auth/email/verify", http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Header.Set("Origin", "https://www.example.com")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d", rec.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusOK && rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("public login route omitted credentialed CORS")
			}
		})
	}
}

func TestCORSMW_AppOriginRetainsFullAPIAccess(t *testing.T) {
	h := corsMW([]string{"https://crm.example.com"}, []string{"https://www.example.com"})(okHandler())
	for _, request := range []struct{ method, path string }{
		{http.MethodPost, "/auth/session"},
		{http.MethodPost, "/auth/logout"},
		{http.MethodPost, "/crm.v1.CRMService/ListAccounts"},
		{http.MethodPatch, "/auth/admin/organizations/org_example"},
	} {
		req := httptest.NewRequest(request.method, request.path, nil)
		req.Header.Set("Origin", "https://crm.example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("app origin %s status=%d", request.path, rec.Code)
		}
	}
}

func TestCORSMW_AuthOriginPreflightUsesRequestedMethodAndRoute(t *testing.T) {
	h := corsMW(nil, []string{"https://www.example.com"})(okHandler())
	for _, test := range []struct {
		path, requestedMethod string
		want                  int
	}{
		{"/auth/email/verify", http.MethodPost, http.StatusNoContent},
		{"/auth/email/verify", http.MethodDelete, http.StatusForbidden},
		{"/auth/session", http.MethodGet, http.StatusForbidden},
		{"/crm.v1.CRMService/ListAccounts", http.MethodPost, http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodOptions, test.path, nil)
		req.Header.Set("Origin", "https://www.example.com")
		req.Header.Set("Access-Control-Request-Method", test.requestedMethod)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Errorf("%s %s preflight=%d want=%d", test.requestedMethod, test.path, rec.Code, test.want)
		}
	}
}
