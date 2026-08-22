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
	loc, _ := line["location"].(string)
	if !strings.Contains(loc, "state=abc") {
		t.Fatalf("location = %q, want the non-secret params kept", loc)
	}
}

// A login bounce is only diagnosable if the log shows where the browser was
// sent, and return_to holds the whole interrupted authorize request.
func TestRequestLogKeepsTheNestedReturnTo(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet,
		"/login?return_to=%2Foauth2%2Fauthorize%3Fclient_id%3Dcrm-web%26response_type%3Dcode", nil)
	line := logOneRequest(t, req, func(w http.ResponseWriter, _ *http.Request) {})

	query, _ := line["query"].(string)
	if !strings.Contains(query, "client_id") || !strings.Contains(query, "response_type") {
		t.Fatalf("query = %q, want the nested authorize params readable", query)
	}
}

func TestRequestLogRecordsStatusAndSessionCookieName(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/self-service/login?flow=1", nil)
	req.AddCookie(&http.Cookie{Name: "authincore_session", Value: "a-real-session-jwt"})
	line := logOneRequest(t, req, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	if got := line["status"]; got != float64(http.StatusTooManyRequests) {
		t.Fatalf("status = %v, want 429", got)
	}
	if got := line["session_cookie"]; got != "authincore_session" {
		t.Fatalf("session_cookie = %v, want the cookie name", got)
	}
	raw, _ := json.Marshal(line)
	if strings.Contains(string(raw), "a-real-session-jwt") {
		t.Fatalf("session cookie value leaked into the log line: %s", raw)
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
