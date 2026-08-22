package middleware

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sensitiveParams are query parameters whose values are credentials or
// single-use secrets. They are replaced with a marker rather than dropped, so a
// log line still shows that the parameter was present.
var sensitiveParams = map[string]struct{}{
	"code":            {},
	"code_verifier":   {},
	"access_token":    {},
	"id_token":        {},
	"refresh_token":   {},
	"token":           {},
	"password":        {},
	"consent_id":      {},
	"login_challenge": {},
}

// RequestLog writes one structured line per HTTP request.
//
// Without it the only record of a browser's journey through the login and
// authorize endpoints is whatever the client happens to show afterwards, which
// makes redirect loops ("why did /oauth2/authorize bounce back to /login?")
// undiagnosable from the server side. The line therefore carries the response
// status and, for redirects, where the browser was sent — that pair is what
// reconstructs a flow.
//
// Health probes log at debug so a liveness check every second does not bury
// everything else.
func RequestLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &logStatusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if q := redactQuery(r.URL.RawQuery); q != "" {
				attrs = append(attrs, "query", q)
			}
			if rid := RequestIDFromContext(r.Context()); rid != "" {
				attrs = append(attrs, "request_id", rid)
			}
			// Which session cookie the browser presented (by name, never value)
			// is the difference between "no session" and "session rejected".
			if name := sessionCookieName(r); name != "" {
				attrs = append(attrs, "session_cookie", name)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				attrs = append(attrs, "location", redactLocation(loc))
			}

			level := slog.LevelInfo
			if isGlobalProbe(r.URL.Path) {
				level = slog.LevelDebug
			} else if rec.status >= 500 {
				level = slog.LevelError
			}
			log.Log(r.Context(), level, "http request", attrs...)
		})
	}
}

// sessionCookieName reports which of the session cookie names the request
// carried, in the same precedence the authorize endpoint resolves them.
func sessionCookieName(r *http.Request) string {
	for _, name := range []string{"__Host-authincore_session", "authincore_session", "ory_session"} {
		if _, err := r.Cookie(name); err == nil {
			return name
		}
	}
	return ""
}

func isGlobalProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

// redactLocation redacts a Location header, which on a successful authorize
// carries the authorization code.
func redactLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return "<unparseable>"
	}
	u.RawQuery = redactQuery(u.RawQuery)
	return u.String()
}

// redactQuery keeps parameter names and non-sensitive values. return_to is
// recursively redacted: it carries a whole authorize URL, and that nested URL
// is exactly what a redirect-loop investigation needs to read.
func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "<unparseable>"
	}
	for key, vs := range values {
		if _, sensitive := sensitiveParams[strings.ToLower(key)]; sensitive {
			values[key] = []string{"<redacted>"}
			continue
		}
		if strings.EqualFold(key, "return_to") {
			for i, v := range vs {
				vs[i] = redactReturnTo(v)
			}
		}
	}
	return values.Encode()
}

func redactReturnTo(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return "<unparseable>"
	}
	u.RawQuery = redactQuery(u.RawQuery)
	return u.String()
}

type logStatusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *logStatusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *logStatusRecorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(b)
}
