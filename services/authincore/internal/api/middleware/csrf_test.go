package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserCSRF(t *testing.T) {
	handler := BrowserCSRF([]string{"https://app.example.test"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		origin     string
		fetchSite  string
		authHeader string
		want       int
	}{
		{name: "same origin cookie", origin: "https://auth.example.test", want: http.StatusNoContent},
		{name: "allowed app origin", origin: "https://app.example.test", want: http.StatusNoContent},
		{name: "cross site origin", origin: "https://evil.example", want: http.StatusForbidden},
		{name: "missing origin with cookie", want: http.StatusForbidden},
		{name: "fetch metadata cross site", origin: "https://auth.example.test", fetchSite: "cross-site", want: http.StatusForbidden},
		{name: "bearer API call", authHeader: "Bearer token", want: http.StatusNoContent},
		// A same-origin <form method="POST"> navigation legitimately sends
		// Origin: null once Referrer-Policy: no-referrer is in effect (see
		// SecurityHeaders) — Sec-Fetch-Site must still allow it rather than
		// falling through to an Origin match that can never succeed.
		{name: "same-origin form post with null origin", origin: "null", fetchSite: "same-origin", want: http.StatusNoContent},
		{name: "same-site fetch metadata", fetchSite: "same-site", want: http.StatusNoContent},
		{name: "none fetch metadata (user-initiated navigation)", fetchSite: "none", want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://auth.example.test/settings", nil)
			req.Host = "auth.example.test"
			req.AddCookie(&http.Cookie{Name: "ory_session", Value: "token"})
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if test.authHeader != "" {
				req.Header.Set("Authorization", test.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status=%d, want %d", rec.Code, test.want)
			}
		})
	}
}
