package http

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"

	oidcstrategy "github.com/authincore/authincore/internal/flow/strategy/oidc"
	"github.com/authincore/authincore/internal/webhook"
	"github.com/google/uuid"
)

// OIDCProviderManager is the subset of OIDCProviderStore used by the admin API.
type OIDCProviderManager interface {
	List(ctx context.Context, orgID uuid.UUID) ([]*oidcstrategy.Provider, error)
	Create(ctx context.Context, orgID uuid.UUID, p *oidcstrategy.Provider) error
	Delete(ctx context.Context, orgID uuid.UUID, providerID string) error
}

// WebhookManager is the subset of WebhookStore used by the admin API.
type WebhookManager interface {
	List(ctx context.Context, orgID uuid.UUID) ([]*webhook.Webhook, error)
	Create(ctx context.Context, w *webhook.Webhook) error
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

type oidcProviderResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ClientID    string   `json:"client_id"`
	AuthURL     string   `json:"auth_url"`
	TokenURL    string   `json:"token_url"`
	UserInfoURL string   `json:"user_info_url"`
	Scopes      []string `json:"scopes"`
	Mapper      string   `json:"mapper,omitempty"`
}

func publicOIDCProvider(p *oidcstrategy.Provider) oidcProviderResponse {
	return oidcProviderResponse{p.ID, p.Name, p.ClientID, p.AuthURL, p.TokenURL, p.UserInfoURL, p.Scopes, p.Mapper}
}

// --- OIDC provider handlers ---

func (s *Server) handleListOIDCProviders(w http.ResponseWriter, r *http.Request) {
	if s.deps.OIDCProviders == nil {
		writeError(w, "not configured", http.StatusNotImplemented)
		return
	}
	orgID := orgIDFromRequest(r)
	providers, err := s.deps.OIDCProviders.List(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if providers == nil {
		providers = []*oidcstrategy.Provider{}
	}
	items := make([]oidcProviderResponse, 0, len(providers))
	for _, provider := range providers {
		items = append(items, publicOIDCProvider(provider))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": items})
}

func (s *Server) handleCreateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	if s.deps.OIDCProviders == nil {
		writeError(w, "not configured", http.StatusNotImplemented)
		return
	}
	var p oidcstrategy.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if p.ID == "" || p.Name == "" {
		writeError(w, "id and name are required", http.StatusBadRequest)
		return
	}
	for _, endpoint := range []string{p.AuthURL, p.TokenURL, p.UserInfoURL} {
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			writeError(w, "provider endpoints must be absolute HTTPS URLs", http.StatusBadRequest)
			return
		}
	}
	orgID := orgIDFromRequest(r)
	if err := s.deps.OIDCProviders.Create(r.Context(), orgID, &p); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, publicOIDCProvider(&p))
}

func (s *Server) handleDeleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	if s.deps.OIDCProviders == nil {
		writeError(w, "not configured", http.StatusNotImplemented)
		return
	}
	providerID := r.PathValue("id")
	orgID := orgIDFromRequest(r)
	if err := s.deps.OIDCProviders.Delete(r.Context(), orgID, providerID); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Webhook handlers ---

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.deps.WebhookStore == nil {
		writeError(w, "not configured", http.StatusNotImplemented)
		return
	}
	orgID := orgIDFromRequest(r)
	hooks, err := s.deps.WebhookStore.List(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	if hooks == nil {
		hooks = []*webhook.Webhook{}
	}
	items := make([]map[string]any, 0, len(hooks))
	for _, hook := range hooks {
		items = append(items, map[string]any{
			"id": hook.ID, "org_id": hook.OrgID, "url": hook.URL,
			"events": hook.Events, "enabled": hook.Enabled,
			"connect_timeout_ms": hook.ConnectTimeoutMS, "read_timeout_ms": hook.ReadTimeoutMS,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": items})
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	if s.deps.WebhookStore == nil {
		writeError(w, "not configured", http.StatusNotImplemented)
		return
	}
	var wh webhook.Webhook
	if err := json.NewDecoder(r.Body).Decode(&wh); err != nil {
		writeError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if wh.URL == "" {
		writeError(w, "url is required", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(wh.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		writeError(w, "webhook url must be an absolute HTTPS URL", http.StatusBadRequest)
		return
	}
	wh.OrgID = orgIDFromRequest(r)
	wh.Enabled = true
	if wh.SigningSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			httpError(w, err, s.deps.Log)
			return
		}
		wh.SigningSecret = base64.RawURLEncoding.EncodeToString(secret)
	}
	if wh.ConnectTimeoutMS == 0 {
		wh.ConnectTimeoutMS = 5000
	}
	if wh.ReadTimeoutMS == 0 {
		wh.ReadTimeoutMS = 10000
	}
	if err := s.deps.WebhookStore.Create(r.Context(), &wh); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, wh)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if s.deps.WebhookStore == nil {
		writeError(w, "not configured", http.StatusNotImplemented)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, errInvalidID, http.StatusBadRequest)
		return
	}
	orgID := orgIDFromRequest(r)
	if err := s.deps.WebhookStore.Delete(r.Context(), orgID, id); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
