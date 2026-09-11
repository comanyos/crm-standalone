package server

import (
	"html"
	"log/slog"
	"net/http"
	"net/url"

	metaintegration "github.com/santoshguru/crm/internal/integration/meta"
)

const metaCallbackPath = "/integrations/meta/callback"

func (s *Server) metaCallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		result := s.meta.Complete(r.Context(), query.Get("state"), query.Get("code"), query.Get("error"))
		if !result.Connected {
			slog.Warn("Meta authorization did not complete", "reason", result.Reason, "request_id", RequestID(r.Context()))
		}
		if target, ok := s.metaReturnURL(result); ok {
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		message := "Meta was not connected."
		if result.Connected {
			message = "Meta authorization complete. Return to Savvy CRM to select a Page and form."
		}
		_, _ = w.Write([]byte("<!doctype html><title>Savvy CRM · Meta</title><p>" + html.EscapeString(message) + "</p>"))
	})
}

func (s *Server) metaReturnURL(result metaintegration.CallbackResult) (string, bool) {
	if s.cfg.WebAppBaseURL == "" {
		return "", false
	}
	path := result.ReturnPath
	if path == "" {
		path = "/settings/integrations"
	}
	target, err := url.Parse(s.cfg.WebAppBaseURL + path)
	if err != nil {
		return "", false
	}
	query := target.Query()
	if result.Connected {
		query.Set("meta", "select_form")
	} else {
		query.Set("meta", "error")
		query.Set("meta_reason", result.Reason)
	}
	target.RawQuery = query.Encode()
	return target.String(), true
}
