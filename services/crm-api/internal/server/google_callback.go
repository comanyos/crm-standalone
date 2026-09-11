package server

import (
	"html"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/santoshguru/crm/internal/integration/google"
)

// googleCallbackPath is where Google returns the user's browser. It must match
// the redirect URI registered on the OAuth client and GOOGLE_OAUTH_REDIRECT_URL
// exactly, including the scheme, host and port — Google compares the whole
// string, not a prefix.
const googleCallbackPath = "/integrations/google/callback"

// googleCallbackHandler finishes an authorization.
//
// This route is deliberately outside the authenticating interceptor: it is a
// top-level browser navigation from accounts.google.com, so it carries no
// Authorization header and cannot be made to carry one. Three things stand in
// for authentication, and none of them is in this file's hands:
//
//  1. The one-time state record, written server-side before the redirect and
//     redeemed exactly once (internal/domain).
//  2. The PKCE verifier, sealed under a binding to that state's tenant, so a
//     state row belonging to another tenant cannot be exchanged.
//  3. Google's own check that the code was issued to this client for this
//     exact redirect URI.
//
// What this file *is* responsible for is that nothing an attacker controls
// reaches the response. The query parameters are read and passed to the
// service, which returns a bounded reason code; the reason and the stored
// return path are the only things that reach the browser, and neither
// originates in this request.
func (s *Server) googleCallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		result := s.google.Complete(r.Context(),
			query.Get("state"), query.Get("code"), query.Get("error"))

		if !result.Connected {
			// The request id, not the reason, is what ties this to the server
			// log; the reason is already bounded but the log is where the
			// detail lives.
			slog.Warn("google authorization did not complete",
				"reason", result.Reason, "request_id", RequestID(r.Context()))
		}

		target, ok := s.googleReturnURL(result)
		if !ok {
			// No web application origin is configured, so there is nowhere
			// safe to send the browser. Render the outcome instead of
			// guessing an origin from the request, which is how an open
			// redirect ships.
			renderGoogleResult(w, result)
			return
		}
		// #nosec G710 -- the origin is validated deployment config and the path was validated before being stored server-side.
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
}

// googleReturnURL builds the post-callback destination, or reports that there
// is none.
//
// The origin comes from configuration and the path from the state record
// written when the flow started — never from this request. The outcome is
// appended as a query parameter so the web application can show a result
// without the server rendering any of it.
func (s *Server) googleReturnURL(result google.CallbackResult) (string, bool) {
	if s.cfg.WebAppBaseURL == "" {
		return "", false
	}
	path := result.ReturnPath
	if path == "" {
		path = "/"
	}
	target, err := url.Parse(s.cfg.WebAppBaseURL + path)
	if err != nil {
		return "", false
	}
	query := target.Query()
	if result.Connected {
		query.Set("google", "connected")
	} else {
		query.Set("google", "error")
		query.Set("google_reason", result.Reason)
	}
	target.RawQuery = query.Encode()
	return target.String(), true
}

// renderGoogleResult is the fallback page for a deployment with no configured
// web application origin.
//
// Every dynamic value it prints is one of this service's own bounded reason
// codes, and it is escaped anyway: a page that is safe only because of what
// the caller happens to put in it is a page one refactor away from being
// unsafe.
func renderGoogleResult(w http.ResponseWriter, result google.CallbackResult) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// This page reflects the outcome of an authorization. It must not be
	// stored, framed, or sniffed into another content type.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")

	status := http.StatusOK
	heading, detail := "Google account connected", "You can close this window and return to the CRM."
	if !result.Connected {
		status = http.StatusBadRequest
		heading = "Google was not connected"
		detail = "Reason: " + result.Reason + ". Start the connection again from the CRM."
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<title>` + html.EscapeString(heading) + `</title>` +
		`<style>body{font:16px/1.5 system-ui,sans-serif;margin:4rem auto;max-width:34rem;padding:0 1rem;color:#0f172a}</style>` +
		`</head><body><h1>` + html.EscapeString(heading) + `</h1><p>` +
		html.EscapeString(detail) + `</p></body></html>`))
}
