// Package middleware provides composable net/http middleware.
package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

type ctxKeyRequestID struct{}

// RequestIDFromContext retrieves the request ID injected by RequestID middleware.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// BrowserCSRF blocks cross-site state-changing requests authenticated by a
// browser cookie. Bearer-token and cookie-free API calls are not subject to
// CSRF, while same-origin and explicitly allowed application origins remain
// supported.
func BrowserCSRF(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if normalized := strings.TrimRight(strings.TrimSpace(origin), "/"); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isUnsafeMethod(r.Method) || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") == "" {
				next.ServeHTTP(w, r)
				return
			}
			// Sec-Fetch-Site (Fetch Metadata) is the modern, browser-guaranteed
			// signal and is trusted fully when present: "cross-site" is always
			// rejected, anything else ("same-origin", "same-site", or "none" for a
			// user-initiated navigation like a typed URL or bookmark) is allowed
			// without also requiring an Origin match. That fallback Origin check
			// below only makes sense for browsers that omit Fetch Metadata — a
			// genuine same-origin <form method="POST"> navigation legitimately
			// sends Origin: null (the literal string, per the Fetch spec) once
			// SecurityHeaders' Referrer-Policy: no-referrer is in effect, which
			// would make Origin-matching reject every real same-origin form post,
			// not just forged cross-site ones.
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
				if strings.EqualFold(site, "cross-site") {
					csrfDenied(w)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			origin := strings.TrimRight(r.Header.Get("Origin"), "/")
			if origin == "" || !originAllowed(origin, r.Host, allowed) {
				csrfDenied(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isUnsafeMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func originAllowed(origin, requestHost string, allowed map[string]struct{}) bool {
	if _, ok := allowed[origin]; ok {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == requestHost && (parsed.Scheme == "https" || parsed.Scheme == "http")
}

func csrfDenied(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"csrf_rejected","message":"cross-site request rejected"}`))
}

// RequestID injects a UUID into the request context and echoes it in X-Request-ID response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if !validRequestID(rid) {
			rid = uuid.New().String()
		}
		ctx := context.WithValue(r.Context(), ctxKeyRequestID{}, rid)
		w.Header().Set("X-Request-ID", rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validRequestID prevents arbitrary header data, including accidental secrets,
// from becoming a correlation field in structured logs.
func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// SecurityHeaders sets secure HTTP response headers on every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=()")
		// CSP: API-only server — no scripts, no frames, no inline content.
		h.Set("Content-Security-Policy", "default-src 'none'")
		// Only set HSTS when behind TLS. Check if TLS was used or X-Forwarded-Proto says https.
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}

// CORS answers cross-origin browser calls from an explicit origin allowlist.
// Browser-based OIDC clients (the platform shell, any SPA on its own origin)
// exchange the authorization code from JavaScript, so /oauth2/token is reached
// cross-origin and fails without these headers.
//
// The allowlist is exact-match and never wildcarded: the reflected origin is
// always one the operator configured, so a hostile page cannot read responses.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
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
				setCORSHeaders(w, origin)
			}

			// Preflight is answered here; it never reaches the route table,
			// where an unmatched OPTIONS would fall through to the SPA.
			if isPreflight(r) {
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

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func setCORSHeaders(w http.ResponseWriter, origin string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Org-ID")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Max-Age", "600")
	// Responses differ per origin — keep caches from serving one origin's
	// response to another.
	h.Add("Vary", "Origin")
}

// Chain composes multiple middleware into one, outermost first.
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(final http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
