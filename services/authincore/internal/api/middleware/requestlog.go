package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

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
				"route", safeRoute(r),
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if rid := RequestIDFromContext(r.Context()); rid != "" {
				attrs = append(attrs, "request_id", rid)
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

func isGlobalProbe(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

// safeRoute returns a registered route pattern, never request values or a
// dynamic URL. This keeps identifiers and query/body PII out of logs.
func safeRoute(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	if strings.HasPrefix(r.URL.Path, "/authincore.v1.") {
		return r.URL.Path
	}
	return "unmatched"
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
