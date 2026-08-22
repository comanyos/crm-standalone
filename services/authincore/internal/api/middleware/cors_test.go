package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	allowed := []string{"http://localhost:3000", "https://shell.example.com"}

	tests := []struct {
		name            string
		method          string
		origin          string
		preflightFor    string // Access-Control-Request-Method; empty = not a preflight
		wantStatus      int
		wantAllowOrigin string
		wantNextCalled  bool
	}{
		{
			name:            "allowed origin gets headers and passes through",
			method:          http.MethodPost,
			origin:          "http://localhost:3000",
			wantStatus:      http.StatusOK,
			wantAllowOrigin: "http://localhost:3000",
			wantNextCalled:  true,
		},
		{
			name:           "disallowed origin gets no headers but request still served",
			method:         http.MethodPost,
			origin:         "https://evil.example.com",
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
		},
		{
			name:            "preflight from allowed origin short-circuits with 204",
			method:          http.MethodOptions,
			origin:          "http://localhost:3000",
			preflightFor:    http.MethodPost,
			wantStatus:      http.StatusNoContent,
			wantAllowOrigin: "http://localhost:3000",
			wantNextCalled:  false,
		},
		{
			name:           "preflight from disallowed origin is rejected",
			method:         http.MethodOptions,
			origin:         "https://evil.example.com",
			preflightFor:   http.MethodPost,
			wantStatus:     http.StatusForbidden,
			wantNextCalled: false,
		},
		{
			name:           "same-origin request without Origin is untouched",
			method:         http.MethodGet,
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			h := CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(tt.method, "/oauth2/token", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.preflightFor != "" {
				req.Header.Set("Access-Control-Request-Method", tt.preflightFor)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantAllowOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.wantAllowOrigin)
			}
			if nextCalled != tt.wantNextCalled {
				t.Errorf("next called = %v, want %v", nextCalled, tt.wantNextCalled)
			}
		})
	}
}

// A wildcard must never be reflected: with Allow-Credentials set, "*" would let
// any site read authenticated responses.
func TestCORS_NeverWildcards(t *testing.T) {
	h := CORS([]string{"http://localhost:3000"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatal("Allow-Origin must never be a wildcard alongside Allow-Credentials")
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin so caches do not cross origins", got)
	}
}
