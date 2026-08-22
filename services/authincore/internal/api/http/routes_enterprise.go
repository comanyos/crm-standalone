//go:build enterprise

package http

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/authincore/authincore/internal/audit"
	"github.com/authincore/authincore/internal/enterprise"
	"github.com/google/uuid"
)

func (s *Server) registerEnterpriseRoutes(mux *http.ServeMux) {
	// --- SAML SP ---
	if s.deps.SAMLSP != nil {
		mux.HandleFunc("GET /saml/{provider_id}/metadata", s.deps.SAMLSP.MetadataHandler)
		mux.HandleFunc("GET /saml/{provider_id}/login", s.deps.SAMLSP.InitiateSSO)
		mux.HandleFunc("POST /saml/{provider_id}/acs", s.deps.SAMLSP.AssertionConsumer)
		mux.HandleFunc("POST /api/v1/saml/providers", s.requirePermission("provider", "write", s.deps.SAMLSP.CreateProvider))
	}

	// --- SAML IdP ---
	if s.deps.SAMLIdP != nil {
		mux.HandleFunc("GET /saml/idp/{org_id}/metadata", s.deps.SAMLIdP.MetadataHandler)
		mux.HandleFunc("GET /saml/idp/{org_id}/sso", s.deps.SAMLIdP.SingleSignOnHandler)
		mux.HandleFunc("POST /saml/idp/{org_id}/sso", s.deps.SAMLIdP.SingleSignOnHandler)
	}

	// --- SCIM 2.0 provisioning ---
	if s.deps.SCIMHandler != nil {
		gatedMux := http.NewServeMux()
		s.deps.SCIMHandler.Routes(gatedMux, "/scim/v2")
		mux.HandleFunc("/scim/", gatedMux.ServeHTTP)
	}

	// --- Custom branding (public) ---
	// The hosted login UI fetches this before authenticating to apply the org's
	// logo and primary color. It only exposes presentational fields and falls
	// back to defaults when no branding row exists, so an unbranded org simply
	// renders the stock theme.
	if s.deps.Branding != nil {
		mux.HandleFunc("GET /api/v1/public/branding", s.handlePublicBranding)
		mux.HandleFunc("GET /api/v1/orgs/{org_id}/branding", s.requirePermission("branding", "read", s.handleGetBranding))
		mux.HandleFunc("POST /api/v1/orgs/{org_id}/branding", s.requirePermission("branding", "write", s.handleUpsertBranding))
	}

	if s.deps.LoginPolicies != nil {
		mux.HandleFunc("GET /api/v1/orgs/{org_id}/login-policy", s.requirePermission("login-policy", "read", s.handleGetLoginPolicy))
		mux.HandleFunc("POST /api/v1/orgs/{org_id}/login-policy", s.requirePermission("login-policy", "write", s.handleUpsertLoginPolicy))
	}

	if s.deps.AuditConfigs != nil {
		mux.HandleFunc("GET /api/v1/orgs/{org_id}/audit-export", s.requirePermission("audit-export", "read", s.handleListAuditExports))
		mux.HandleFunc("POST /api/v1/orgs/{org_id}/audit-export", s.requirePermission("audit-export", "write", s.handleCreateAuditExport))
		mux.HandleFunc("DELETE /api/v1/orgs/{org_id}/audit-export/{id}", s.requirePermission("audit-export", "delete", s.handleDeleteAuditExport))
	}

	// --- SCIM token management (admin) ---
	// Admin endpoints for issuing/listing/revoking SCIM bearer tokens.
	if s.deps.SCIMTokenIssuer != nil {
		mux.HandleFunc("POST /api/v1/scim/tokens", s.requirePermission("scim-token", "write", s.handleIssueSCIMToken))
		mux.HandleFunc("GET /api/v1/scim/tokens", s.requirePermission("scim-token", "read", s.handleListSCIMTokens))
		mux.HandleFunc("DELETE /api/v1/scim/tokens/{token_id}", s.requirePermission("scim-token", "delete", s.handleRevokeSCIMToken))
	}
}

func enterpriseOrgFromPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		writeError(w, "invalid org_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	if orgID != orgIDFromRequest(r) {
		writeError(w, "not found", http.StatusNotFound)
		return uuid.Nil, false
	}
	return orgID, true
}

func (s *Server) handleGetBranding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	b, err := s.deps.Branding.Get(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleUpsertBranding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	var body enterprise.Branding
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.OrgID = orgID
	if body.PrimaryColor != "" && (!strings.HasPrefix(body.PrimaryColor, "#") || len(body.PrimaryColor) != 7) {
		writeError(w, "primary_color must be a six-digit hex color", http.StatusBadRequest)
		return
	}
	if err := s.deps.Branding.Upsert(r.Context(), body); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleGetLoginPolicy(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	p, err := s.deps.LoginPolicies.Get(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sso_enforced": p.SSOEnforced, "sso_provider_id": p.SSOProviderID, "allow_password_fallback": p.AllowPasswordFallback})
}

func (s *Server) handleUpsertLoginPolicy(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		SSOEnforced           bool       `json:"sso_enforced"`
		SSOProviderID         *uuid.UUID `json:"sso_provider_id"`
		AllowPasswordFallback bool       `json:"allow_password_fallback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	p := enterprise.LoginPolicy{SSOEnforced: body.SSOEnforced, SSOProviderID: body.SSOProviderID, AllowPasswordFallback: body.AllowPasswordFallback}
	if err := s.deps.LoginPolicies.Upsert(r.Context(), orgID, p); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleListAuditExports(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	configs, err := s.deps.AuditConfigs.List(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, configs)
}

func (s *Server) handleCreateAuditExport(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Type        string `json:"type"`
		EndpointURL string `json:"endpoint_url"`
		Format      string `json:"format"`
		Secret      string `json:"secret"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	endpoint, err := url.Parse(body.EndpointURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
		writeError(w, "endpoint_url must be an absolute HTTPS URL", http.StatusBadRequest)
		return
	}
	if body.Type == "" {
		body.Type = "generic_webhook"
	}
	if body.Format == "" {
		body.Format = "json"
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.Secret == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			httpError(w, err, s.deps.Log)
			return
		}
		body.Secret = base64.RawURLEncoding.EncodeToString(raw)
	}
	cfg := &audit.Config{OrgID: orgID, Type: body.Type, Endpoint: body.EndpointURL, Secret: body.Secret, Format: body.Format, Enabled: enabled}
	if err := s.deps.AuditConfigs.Create(r.Context(), cfg); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"config": cfg, "signing_secret": body.Secret})
}

func (s *Server) handleDeleteAuditExport(w http.ResponseWriter, r *http.Request) {
	orgID, ok := enterpriseOrgFromPath(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := s.deps.AuditConfigs.Delete(r.Context(), orgID, id); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicBranding returns the presentational branding for the request's org,
// resolved from the tenancy context (Host/JWT), never the request body. The shape
// matches what the hosted-ui bundle expects: { appName, logoUrl, primaryColor }.
func (s *Server) handlePublicBranding(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	b, err := s.deps.Branding.Get(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"logoUrl":      b.LogoURL,
		"primaryColor": b.PrimaryColor,
	})
}
