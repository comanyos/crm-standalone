package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// RoleManager is the admin CRUD interface for roles.
type RoleManager interface {
	ListRoles(ctx context.Context, orgID uuid.UUID) ([]*AdminRole, error)
	CreateRole(ctx context.Context, r *AdminRole) error
	DeleteRole(ctx context.Context, orgID, id uuid.UUID) error
	AssignRole(ctx context.Context, orgID, identityID, roleID uuid.UUID) error
	UnassignRole(ctx context.Context, orgID, identityID, roleID uuid.UUID) error
}

// ApplicationManager is the admin CRUD interface for OIDC client applications.
type ApplicationManager interface {
	ListApplications(ctx context.Context, orgID uuid.UUID) ([]*AdminApplication, error)
	CreateApplication(ctx context.Context, a *AdminApplication) error
	DeleteApplication(ctx context.Context, orgID, id uuid.UUID) error
}

// AdminRole mirrors the roles table.
type AdminRole struct {
	ID          uuid.UUID `json:"id"`
	OrgID       uuid.UUID `json:"org_id"`
	Key         string    `json:"key"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

// AdminApplication mirrors the applications table for OIDC clients.
type AdminApplication struct {
	ID            uuid.UUID `json:"id"`
	OrgID         uuid.UUID `json:"org_id"`
	Name          string    `json:"name"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret,omitempty"`
	RedirectURIs  []string  `json:"redirect_uris"`
	GrantTypes    []string  `json:"grant_types"`
	ResponseTypes []string  `json:"response_types"`
	CreatedAt     time.Time `json:"created_at"`
}

// --- Role handlers ---

func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	roles, err := s.deps.RoleManager.ListRoles(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": roles, "total": len(roles)})
}

func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body struct {
		Key         string `json:"key"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
		writeError(w, "key required", http.StatusBadRequest)
		return
	}
	role := &AdminRole{
		ID:          uuid.New(),
		OrgID:       orgID,
		Key:         body.Key,
		DisplayName: body.DisplayName,
	}
	if err := s.deps.RoleManager.CreateRole(r.Context(), role); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, role)
}

func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := s.deps.RoleManager.DeleteRole(r.Context(), orgID, id); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAssignRole(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body struct {
		IdentityID string `json:"identity_id"`
		RoleID     string `json:"role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	identityID, err := uuid.Parse(body.IdentityID)
	if err != nil {
		writeError(w, "invalid identity_id", http.StatusBadRequest)
		return
	}
	roleID, err := uuid.Parse(body.RoleID)
	if err != nil {
		writeError(w, "invalid role_id", http.StatusBadRequest)
		return
	}
	if err := s.deps.RoleManager.AssignRole(r.Context(), orgID, identityID, roleID); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func (s *Server) handleUnassignRole(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body struct {
		IdentityID string `json:"identity_id"`
		RoleID     string `json:"role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	identityID, err := uuid.Parse(body.IdentityID)
	if err != nil {
		writeError(w, "invalid identity_id", http.StatusBadRequest)
		return
	}
	roleID, err := uuid.Parse(body.RoleID)
	if err != nil {
		writeError(w, "invalid role_id", http.StatusBadRequest)
		return
	}
	if err := s.deps.RoleManager.UnassignRole(r.Context(), orgID, identityID, roleID); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Application handlers ---

func (s *Server) handleListApplications(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	apps, err := s.deps.AppManager.ListApplications(r.Context(), orgID)
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": apps, "total": len(apps)})
}

func (s *Server) handleCreateApplication(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body struct {
		Name          string   `json:"name"`
		RedirectURIs  []string `json:"redirect_uris"`
		GrantTypes    []string `json:"grant_types"`
		ResponseTypes []string `json:"response_types"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, "name required", http.StatusBadRequest)
		return
	}
	if len(body.GrantTypes) == 0 {
		body.GrantTypes = []string{"authorization_code"}
	}
	if len(body.ResponseTypes) == 0 {
		body.ResponseTypes = []string{"code"}
	}
	app := &AdminApplication{
		ID:            uuid.New(),
		OrgID:         orgID,
		Name:          body.Name,
		ClientID:      "client_" + uuid.New().String()[:8],
		ClientSecret:  uuid.New().String(),
		RedirectURIs:  body.RedirectURIs,
		GrantTypes:    body.GrantTypes,
		ResponseTypes: body.ResponseTypes,
	}
	if err := s.deps.AppManager.CreateApplication(r.Context(), app); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) handleDeleteApplication(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := s.deps.AppManager.DeleteApplication(r.Context(), orgID, id); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
