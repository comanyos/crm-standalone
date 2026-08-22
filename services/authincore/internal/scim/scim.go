// Package scim implements SCIM 2.0 (RFC 7643/7644) endpoints for push
// provisioning from Okta, Azure AD, and other enterprise IdPs.
//
// Supported operations:
//   - Users:  GET (list+filter), GET by ID, POST (create), PUT (replace), PATCH (update), DELETE
//   - Groups: GET (list+filter), GET by ID, POST (create), PUT (replace), DELETE
//   - ServiceProviderConfig, ResourceTypes, Schemas discovery endpoints
//
// Authentication: Bearer token per org, stored as bcrypt hash in scim_tokens.
package scim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// --- SCIM resource types ---

const (
	schemaUser  = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaError = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaList  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaPatch = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// User is a SCIM 2.0 User resource.
type User struct {
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	Schemas     []string `json:"schemas"`
	UserName    string   `json:"userName"`
	DisplayName string   `json:"displayName,omitempty"`
	Emails      []Email  `json:"emails,omitempty"`
	Active      bool     `json:"active"`
	Meta        *Meta    `json:"meta,omitempty"`
}

type Email struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

type Meta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
	Location     string    `json:"location"`
}

// Group is a SCIM 2.0 Group resource.
type Group struct {
	ID          string        `json:"id,omitempty"`
	ExternalID  string        `json:"externalId,omitempty"`
	Schemas     []string      `json:"schemas"`
	DisplayName string        `json:"displayName"`
	Members     []GroupMember `json:"members,omitempty"`
	Meta        *Meta         `json:"meta,omitempty"`
}

type GroupMember struct {
	Value   string `json:"value"` // user ID
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

type PatchOp struct {
	Schemas    []string         `json:"schemas"`
	Operations []PatchOperation `json:"Operations"`
}

type PatchOperation struct {
	Op    string          `json:"op"` // add, remove, replace
	Path  string          `json:"path,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

type listResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []any    `json:"Resources"`
}

// --- storage interfaces ---

type UserStore interface {
	Create(ctx context.Context, orgID uuid.UUID, u *User) (*User, error)
	ByID(ctx context.Context, orgID uuid.UUID, id string) (*User, error)
	ByExternalID(ctx context.Context, orgID uuid.UUID, externalID string) (*User, error)
	ByUserName(ctx context.Context, orgID uuid.UUID, userName string) (*User, error)
	Replace(ctx context.Context, orgID uuid.UUID, u *User) (*User, error)
	SetActive(ctx context.Context, orgID uuid.UUID, id string, active bool) error
	Delete(ctx context.Context, orgID uuid.UUID, id string) error
	List(ctx context.Context, orgID uuid.UUID, filter *Filter, startIndex, count int) ([]*User, int, error)
}

type GroupStore interface {
	Create(ctx context.Context, orgID uuid.UUID, g *Group) (*Group, error)
	ByID(ctx context.Context, orgID uuid.UUID, id string) (*Group, error)
	Replace(ctx context.Context, orgID uuid.UUID, g *Group) (*Group, error)
	Delete(ctx context.Context, orgID uuid.UUID, id string) error
	List(ctx context.Context, orgID uuid.UUID, filter *Filter, startIndex, count int) ([]*Group, int, error)
	SetMembers(ctx context.Context, orgID uuid.UUID, groupID string, memberIDs []string) error
}

type TokenValidator interface {
	Validate(ctx context.Context, orgID uuid.UUID, token string) error
}

// --- Handler ---

type Handler struct {
	users  UserStore
	groups GroupStore
	tokens TokenValidator
	base   string // e.g. https://auth.example.com/scim/v2
}

func NewHandler(users UserStore, groups GroupStore, tokens TokenValidator, base string) *Handler {
	return &Handler{users: users, groups: groups, tokens: tokens, base: base}
}

// Routes mounts all SCIM endpoints onto mux under the given prefix.
func (h *Handler) Routes(mux *http.ServeMux, prefix string) {
	mux.Handle(prefix+"/Users", h.auth(http.HandlerFunc(h.usersCollection)))
	mux.Handle(prefix+"/Users/{id}", h.auth(http.HandlerFunc(h.userResource)))
	mux.Handle(prefix+"/Groups", h.auth(http.HandlerFunc(h.groupsCollection)))
	mux.Handle(prefix+"/Groups/{id}", h.auth(http.HandlerFunc(h.groupResource)))
	mux.Handle(prefix+"/ServiceProviderConfig", http.HandlerFunc(h.serviceProviderConfig))
	mux.Handle(prefix+"/ResourceTypes", http.HandlerFunc(h.resourceTypes))
	mux.Handle(prefix+"/Schemas", http.HandlerFunc(h.schemas))
}

// auth validates the Bearer token and injects orgID into the request context.
func (h *Handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgID, err := uuid.Parse(r.Header.Get("X-Org-ID"))
		if err != nil {
			scimError(w, http.StatusUnauthorized, "invalid or missing X-Org-ID")
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err := h.tokens.Validate(r.Context(), orgID, bearer); err != nil {
			scimError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(withOrgID(r.Context(), orgID)))
	})
}

// --- Users ---

func (h *Handler) usersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listUsers(w, r)
	case http.MethodPost:
		h.createUser(w, r)
	default:
		scimError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) userResource(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getUser(w, r)
	case http.MethodPut:
		h.replaceUser(w, r)
	case http.MethodPatch:
		h.patchUser(w, r)
	case http.MethodDelete:
		h.deleteUser(w, r)
	default:
		scimError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	q := r.URL.Query()
	start, _ := strconv.Atoi(q.Get("startIndex"))
	if start < 1 {
		start = 1
	}
	count, _ := strconv.Atoi(q.Get("count"))
	if count < 1 {
		count = 100
	}
	users, total, err := h.users.List(r.Context(), orgID, ParseFilter(q.Get("filter")), start, count)
	if err != nil {
		scimError(w, http.StatusInternalServerError, "list error")
		return
	}
	resources := make([]any, len(users))
	for i, u := range users {
		h.setUserMeta(u, orgID)
		resources[i] = u
	}
	scimJSON(w, http.StatusOK, listResponse{
		Schemas:      []string{schemaList},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: count,
		Resources:    resources,
	})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		scimError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if u.UserName == "" {
		scimError(w, http.StatusBadRequest, "userName is required")
		return
	}
	u.Schemas = []string{schemaUser}
	created, err := h.users.Create(r.Context(), orgID, &u)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			scimError(w, http.StatusConflict, "userName already exists")
			return
		}
		scimError(w, http.StatusInternalServerError, "create error")
		return
	}
	h.setUserMeta(created, orgID)
	w.Header().Set("Location", created.Meta.Location)
	scimJSON(w, http.StatusCreated, created)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	u, err := h.users.ByID(r.Context(), orgID, r.PathValue("id"))
	if err != nil {
		scimError(w, http.StatusNotFound, "user not found")
		return
	}
	h.setUserMeta(u, orgID)
	scimJSON(w, http.StatusOK, u)
}

func (h *Handler) replaceUser(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	var u User
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		scimError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	u.ID = r.PathValue("id")
	updated, err := h.users.Replace(r.Context(), orgID, &u)
	if err != nil {
		scimError(w, http.StatusNotFound, "user not found")
		return
	}
	h.setUserMeta(updated, orgID)
	scimJSON(w, http.StatusOK, updated)
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	id := r.PathValue("id")
	var patch PatchOp
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		scimError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	for _, op := range patch.Operations {
		if strings.EqualFold(op.Op, "replace") && strings.EqualFold(op.Path, "active") {
			var active bool
			if err := json.Unmarshal(op.Value, &active); err == nil {
				if err := h.users.SetActive(r.Context(), orgID, id, active); err != nil {
					scimError(w, http.StatusInternalServerError, "patch error")
					return
				}
			}
		}
	}
	u, err := h.users.ByID(r.Context(), orgID, id)
	if err != nil {
		scimError(w, http.StatusNotFound, "user not found")
		return
	}
	h.setUserMeta(u, orgID)
	scimJSON(w, http.StatusOK, u)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	if err := h.users.Delete(r.Context(), orgID, r.PathValue("id")); err != nil {
		scimError(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Groups ---

func (h *Handler) groupsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listGroups(w, r)
	case http.MethodPost:
		h.createGroup(w, r)
	default:
		scimError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) groupResource(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getGroup(w, r)
	case http.MethodPut:
		h.replaceGroup(w, r)
	case http.MethodDelete:
		h.deleteGroup(w, r)
	default:
		scimError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	q := r.URL.Query()
	start, _ := strconv.Atoi(q.Get("startIndex"))
	if start < 1 {
		start = 1
	}
	count, _ := strconv.Atoi(q.Get("count"))
	if count < 1 {
		count = 100
	}
	groups, total, err := h.groups.List(r.Context(), orgID, ParseFilter(q.Get("filter")), start, count)
	if err != nil {
		scimError(w, http.StatusInternalServerError, "list error")
		return
	}
	resources := make([]any, len(groups))
	for i, g := range groups {
		h.setGroupMeta(g, orgID)
		resources[i] = g
	}
	scimJSON(w, http.StatusOK, listResponse{
		Schemas:      []string{schemaList},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: count,
		Resources:    resources,
	})
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	var g Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		scimError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	g.Schemas = []string{schemaGroup}
	created, err := h.groups.Create(r.Context(), orgID, &g)
	if err != nil {
		scimError(w, http.StatusInternalServerError, "create error")
		return
	}
	h.setGroupMeta(created, orgID)
	w.Header().Set("Location", created.Meta.Location)
	scimJSON(w, http.StatusCreated, created)
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	g, err := h.groups.ByID(r.Context(), orgID, r.PathValue("id"))
	if err != nil {
		scimError(w, http.StatusNotFound, "group not found")
		return
	}
	h.setGroupMeta(g, orgID)
	scimJSON(w, http.StatusOK, g)
}

func (h *Handler) replaceGroup(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	var g Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		scimError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	g.ID = r.PathValue("id")
	// Sync members.
	memberIDs := make([]string, len(g.Members))
	for i, m := range g.Members {
		memberIDs[i] = m.Value
	}
	if err := h.groups.SetMembers(r.Context(), orgID, g.ID, memberIDs); err != nil {
		scimError(w, http.StatusInternalServerError, "member sync error")
		return
	}
	updated, err := h.groups.Replace(r.Context(), orgID, &g)
	if err != nil {
		scimError(w, http.StatusNotFound, "group not found")
		return
	}
	h.setGroupMeta(updated, orgID)
	scimJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromCtx(r.Context())
	if err := h.groups.Delete(r.Context(), orgID, r.PathValue("id")); err != nil {
		scimError(w, http.StatusNotFound, "group not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Discovery ---

func (h *Handler) serviceProviderConfig(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          map[string]any{"supported": true},
		"bulk":           map[string]any{"supported": false},
		"filter":         map[string]any{"supported": true, "maxResults": 200},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"name":        "OAuth Bearer Token",
			"description": "Authentication scheme using OAuth 2.0 Bearer Token",
			"type":        "oauthbearertoken",
			"primary":     true,
		}},
	})
}

func (h *Handler) resourceTypes(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{schemaList},
		"totalResults": 2,
		"Resources": []map[string]any{
			{"id": "User", "name": "User", "endpoint": "/Users", "schema": schemaUser},
			{"id": "Group", "name": "Group", "endpoint": "/Groups", "schema": schemaGroup},
		},
	})
}

func (h *Handler) schemas(w http.ResponseWriter, r *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{schemaList},
		"totalResults": 2,
		"Resources":    []map[string]any{{"id": schemaUser}, {"id": schemaGroup}},
	})
}

// --- helpers ---

func (h *Handler) setUserMeta(u *User, orgID uuid.UUID) {
	u.Meta = &Meta{
		ResourceType: "User",
		Location:     fmt.Sprintf("%s/%s/Users/%s", h.base, orgID, u.ID),
	}
}

func (h *Handler) setGroupMeta(g *Group, orgID uuid.UUID) {
	g.Meta = &Meta{
		ResourceType: "Group",
		Location:     fmt.Sprintf("%s/%s/Groups/%s", h.base, orgID, g.ID),
	}
}

func scimJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func scimError(w http.ResponseWriter, status int, detail string) {
	scimJSON(w, status, map[string]any{
		"schemas": []string{schemaError},
		"status":  strconv.Itoa(status),
		"detail":  detail,
	})
}

type ctxKeyOrgID struct{}

func withOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, ctxKeyOrgID{}, id)
}

func orgIDFromCtx(ctx context.Context) uuid.UUID {
	v, _ := ctx.Value(ctxKeyOrgID{}).(uuid.UUID)
	return v
}
