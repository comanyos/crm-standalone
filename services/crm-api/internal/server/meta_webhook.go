package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/santoshguru/crm/internal/domain"
	metaintegration "github.com/santoshguru/crm/internal/integration/meta"
)

const metaWebhookPath = "/integrations/meta/webhook"

type metaWebhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				LeadgenID   string `json:"leadgen_id"`
				PageID      string `json:"page_id"`
				FormID      string `json:"form_id"`
				AdID        string `json:"ad_id"`
				CreatedTime int64  `json:"created_time"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func (s *Server) metaWebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.metaWebhookChallenge(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, domain.MaxCaptureBodyBytes))
		if err != nil {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		if !validMetaSignature(body, r.Header.Get("X-Hub-Signature-256"), s.cfg.MetaAppSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var payload metaWebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if payload.Object != "page" {
			w.WriteHeader(http.StatusOK)
			return
		}
		transientFailure := false
		for _, entry := range payload.Entry {
			for _, change := range entry.Changes {
				if change.Field != "leadgen" || change.Value.LeadgenID == "" || change.Value.FormID == "" {
					continue
				}
				pageID := change.Value.PageID
				if pageID == "" {
					pageID = entry.ID
				}
				refs, resolveErr := s.store.ResolveMetaConnections(r.Context(), pageID, change.Value.FormID)
				if errors.Is(resolveErr, domain.ErrNotFound) {
					continue
				}
				if resolveErr != nil {
					transientFailure = true
					continue
				}
				rawEvent, _ := json.Marshal(change.Value)
				for _, ref := range refs {
					_, ingestErr := s.meta.ProcessWebhookLead(r.Context(), ref, change.Value.LeadgenID, string(rawEvent))
					if ingestErr == nil {
						continue
					}
					if errors.Is(ingestErr, metaintegration.ErrPermanent) {
						slog.Warn("Meta lead delivery was permanently rejected", "request_id", RequestID(r.Context()))
						continue
					}
					transientFailure = true
					slog.Warn("Meta lead delivery failed transiently", "request_id", RequestID(r.Context()))
				}
			}
		}
		if transientFailure {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("EVENT_RECEIVED"))
	})
}

func (s *Server) metaWebhookChallenge(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	provided := query.Get("hub.verify_token")
	expected := s.cfg.MetaWebhookVerifyToken
	if query.Get("hub.mode") != "subscribe" || provided == "" || len(provided) != len(expected) ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	challenge := query.Get("hub.challenge")
	if challenge == "" || len(challenge) > 1024 {
		http.Error(w, "invalid challenge", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(challenge))
}

func validMetaSignature(body []byte, header, appSecret string) bool {
	if !strings.HasPrefix(header, "sha256=") || appSecret == "" {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}
