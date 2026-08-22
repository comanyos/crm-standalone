package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func logOneRequest(t *testing.T, req *http.Request, h http.HandlerFunc) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	RequestLog(log)(h).ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON (%v): %s", err, buf.String())
	}
	return line
}

// The authorize endpoint's redirect carries the authorization code. Logging it
// verbatim would put a credential good for a full token exchange into every
// log sink the service writes to.
func TestRequestLogRedactsTheAuthorizationCode(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/oauth2/authorize?client_id=crm-web&state=abc", nil)
	line := logOneRequest(t, req, func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, req, "http://localhost:3000/auth/callback?code=SECRET-CODE&state=abc", http.StatusFound)
	})

	raw, _ := json.Marshal(line)
	if strings.Contains(string(raw), "SECRET-CODE") {
		t.Fatalf("authorization code leaked into the log line: %s", raw)
	}
	if _, ok := line["location"]; ok {
		t.Fatalf("redirect location must not be logged: %v", line)
	}
}

func TestRequestLogOmitsQueryValuesAndCookieValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/login?email=person@example.test&access_token=SECRET-TOKEN", nil)
	req.AddCookie(&http.Cookie{Name: "authincore_session", Value: "a-real-session-jwt"})
	line := logOneRequest(t, req, func(w http.ResponseWriter, _ *http.Request) {})
	raw, _ := json.Marshal(line)
	for _, secret := range []string{"person@example.test", "SECRET-TOKEN", "a-real-session-jwt"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("sensitive value leaked into the log line: %s", raw)
		}
	}
}

func TestRequestLogRecordsStatusWithoutCookieMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/self-service/login?flow=1", nil)
	req.AddCookie(&http.Cookie{Name: "authincore_session", Value: "a-real-session-jwt"})
	line := logOneRequest(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	if got := line["status"]; got != float64(http.StatusTooManyRequests) {
		t.Fatalf("status = %v, want 429", got)
	}
	if _, ok := line["session_cookie"]; ok {
		t.Fatalf("session cookie metadata must not be logged: %v", line)
	}
}

// A handler that never calls WriteHeader still returned 200.
func TestRequestLogDefaultsToOKWhenTheHandlerOnlyWrites(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	line := logOneRequest(t, req, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	if got := line["status"]; got != float64(http.StatusOK) {
		t.Fatalf("status = %v, want 200", got)
	}
	if got := line["level"]; got != "DEBUG" {
		t.Fatalf("level = %v, want health probes at DEBUG", got)
	}
}

func TestRequestIDPreservesSafeValueAndReplacesUnsafeValue(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestIDFromContext(r.Context()); got != w.Header().Get("X-Request-ID") {
			t.Fatalf("context request id=%q response=%q", got, w.Header().Get("X-Request-ID"))
		}
	}))
	for _, tc := range []struct {
		inbound  string
		preserve bool
	}{
		{inbound: "trace-12345678", preserve: true},
		{inbound: "Bearer TOP-SECRET", preserve: false},
	} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-ID", tc.inbound)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-ID")
		if got == "" || (tc.preserve && got != tc.inbound) || (!tc.preserve && got == tc.inbound) {
			t.Fatalf("inbound=%q got=%q preserve=%v", tc.inbound, got, tc.preserve)
		}
	}
}
