//go:build enterprise

package http

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

type issueSCIMTokenRequest struct {
	Description string `json:"description"`
}

type issueSCIMTokenResponse struct {
	Token     string `json:"token"`
	TokenID   string `json:"token_id"`
	CreatedAt string `json:"created_at"`
}

// handleIssueSCIMToken creates a new SCIM bearer token for the org.
// POST /api/v1/scim/tokens
// Body: { "description": "Okta integration" }
// Response: { "token": "plaintext...", "token_id": "uuid", "created_at": "..." }
func (s *Server) handleIssueSCIMToken(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)

	var req issueSCIMTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}

	plaintext, meta, err := s.deps.SCIMTokenIssuer.Issue(r.Context(), orgID, req.Description)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}

	writeJSON(w, http.StatusCreated, issueSCIMTokenResponse{
		Token:     plaintext,
		TokenID:   meta.ID,
		CreatedAt: meta.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// handleListSCIMTokens returns all SCIM tokens for the org (without plaintext values).
// GET /api/v1/scim/tokens
// Response: [{ "id": "uuid", "description": "...", "created_at": "...", "last_used_at": "..." }]
func (s *Server) handleListSCIMTokens(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)

	tokens, err := s.deps.SCIMTokenIssuer.List(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}

	writeJSON(w, http.StatusOK, tokens)
}

// handleRevokeSCIMToken deletes a SCIM token by ID.
// DELETE /api/v1/scim/tokens/{token_id}
func (s *Server) handleRevokeSCIMToken(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	tokenID := r.PathValue("token_id")

	if tokenID == "" {
		http.Error(w, "missing token_id", http.StatusBadRequest)
		return
	}

	// Validate tokenID is a UUID
	if _, err := uuid.Parse(tokenID); err != nil {
		http.Error(w, "invalid token_id", http.StatusBadRequest)
		return
	}

	if err := s.deps.SCIMTokenIssuer.Revoke(r.Context(), orgID, tokenID); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
