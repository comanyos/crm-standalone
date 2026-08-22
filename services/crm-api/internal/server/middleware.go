// Package server wires the module's HTTP surface: routes, the connect
// handlers, and the request middleware chain.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

// RequestID returns the request id from the context (empty if none).
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// chain applies middleware in order (first listed = outermost).
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// recoverMW turns a panic into a 500 + structured error log instead of crashing.
func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// A recovered value can be derived from an untrusted request. Keep
				// the incident correlated without risking a body/token leak.
				slog.Error("panic recovered", "request_id", RequestID(r.Context()), "route", safeRoute(r))
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestIDMW attaches an X-Request-ID (honoring an inbound one) to ctx + response.
func requestIDMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = newID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMW emits one structured access log per request.
func loggingMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("http request",
			"method", r.Method, "route", safeRoute(r), "status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(), "request_id", RequestID(r.Context()))
	})
}

// statusWriter captures the response status for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b)
}

// validRequestID accepts a compact correlation token but rejects arbitrary
// header text so a caller cannot inject secrets or log-control data.
func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

func safeRoute(r *http.Request) string {
	p := r.URL.Path
	if p == "/healthz" || p == "/readyz" || p == "/metrics" {
		return p
	}
	if strings.HasPrefix(p, "/crm.v1.") || strings.HasPrefix(p, "/platform.v1.") {
		return p
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

// corsMW answers cross-origin browser calls (apps/web, on its own origin)
// from an explicit allowlist. The allowlist is exact-match and never
// wildcarded: the reflected origin is always one the operator configured, so
// a hostile page cannot read a response. Connect-ES's browser client sends a
// Connect-Protocol-Version header and an Authorization bearer token, both of
// which must be allowed or the preflight fails closed.
func corsMW(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			allowed[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			ok := origin != "" && allowed[origin]
			if ok {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Connect-Protocol-Version, Connect-Timeout-Ms, X-Request-ID, traceparent")
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Max-Age", "600")
				h.Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				status := http.StatusNoContent
				if !ok {
					status = http.StatusForbidden
				}
				w.WriteHeader(status)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
