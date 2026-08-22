package fga

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

// OrgResolver derives the caller's org from the request. It must come from a
// trusted source (Host-based tenant context or a verified token claim) — never
// from the request body. Returns false if no org could be resolved.
type OrgResolver func(*http.Request) (uuid.UUID, bool)

// Handler exposes the FGA API over HTTP for app/runtime permission checks.
// Mount with: fga.Handler(svc).Routes(mux, "/authz", wrap, org)
//
// The org is resolved per-request via the injected OrgResolver, so callers
// cannot operate across tenants by spoofing an org_id in the body.
type Handler struct {
	svc *Service
	org OrgResolver
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers the FGA endpoints. wrap is applied to every handler (e.g. an
// authentication middleware); org resolves the tenant for each request.
func (h *Handler) Routes(mux *http.ServeMux, prefix string, wrap func(http.HandlerFunc) http.HandlerFunc, org OrgResolver) {
	h.org = org
	mux.HandleFunc("POST "+prefix+"/tuples", wrap(h.writeTuple))
	mux.HandleFunc("DELETE "+prefix+"/tuples", wrap(h.deleteTuple))
	mux.HandleFunc("POST "+prefix+"/check", wrap(h.check))
	mux.HandleFunc("POST "+prefix+"/list-objects", wrap(h.listObjects))
	mux.HandleFunc("POST "+prefix+"/list-subjects", wrap(h.listSubjects))
}

// orgID resolves the request's tenant or writes a 401 and returns false.
func (h *Handler) orgID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if h.org == nil {
		http.Error(w, "org resolver not configured", http.StatusInternalServerError)
		return uuid.Nil, false
	}
	orgID, ok := h.org(r)
	if !ok {
		http.Error(w, "org could not be resolved", http.StatusUnauthorized)
		return uuid.Nil, false
	}
	return orgID, true
}

// POST /authz/tuples
// Body: {"object_type":"lead","object_id":"123","relation":"editor","subject_type":"user","subject_id":"alice-uuid"}
func (h *Handler) writeTuple(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	var body struct {
		ObjectType      string `json:"object_type"`
		ObjectID        string `json:"object_id"`
		Relation        string `json:"relation"`
		SubjectType     string `json:"subject_type"`
		SubjectID       string `json:"subject_id"`
		SubjectRelation string `json:"subject_relation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := h.svc.Write(r.Context(), orgID, Tuple{
		ObjectType:      body.ObjectType,
		ObjectID:        body.ObjectID,
		Relation:        body.Relation,
		SubjectType:     body.SubjectType,
		SubjectID:       body.SubjectID,
		SubjectRelation: body.SubjectRelation,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// DELETE /authz/tuples
func (h *Handler) deleteTuple(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	var body struct {
		ObjectType      string `json:"object_type"`
		ObjectID        string `json:"object_id"`
		Relation        string `json:"relation"`
		SubjectType     string `json:"subject_type"`
		SubjectID       string `json:"subject_id"`
		SubjectRelation string `json:"subject_relation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := h.svc.Delete(r.Context(), orgID, Tuple{
		ObjectType:      body.ObjectType,
		ObjectID:        body.ObjectID,
		Relation:        body.Relation,
		SubjectType:     body.SubjectType,
		SubjectID:       body.SubjectID,
		SubjectRelation: body.SubjectRelation,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /authz/check
// Body: {"object_type":"lead","object_id":"123","relation":"editor","subject_type":"user","subject_id":"alice-uuid"}
// Response: {"allowed": true}
func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	var body struct {
		ObjectType  string `json:"object_type"`
		ObjectID    string `json:"object_id"`
		Relation    string `json:"relation"`
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	allowed, err := h.svc.Check(r.Context(), CheckRequest{
		OrgID:       orgID,
		ObjectType:  body.ObjectType,
		ObjectID:    body.ObjectID,
		Relation:    body.Relation,
		SubjectType: body.SubjectType,
		SubjectID:   body.SubjectID,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"allowed": allowed})
}

// POST /authz/list-objects
// "Which leads can alice edit?"
func (h *Handler) listObjects(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	var body struct {
		ObjectType  string `json:"object_type"`
		Relation    string `json:"relation"`
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	ids, err := h.svc.ListObjects(r.Context(), ListObjectsRequest{
		OrgID:       orgID,
		ObjectType:  body.ObjectType,
		Relation:    body.Relation,
		SubjectType: body.SubjectType,
		SubjectID:   body.SubjectID,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object_ids": ids})
}

// POST /authz/list-subjects
// "Who can view lead:123?"
func (h *Handler) listSubjects(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	var body struct {
		ObjectType  string `json:"object_type"`
		ObjectID    string `json:"object_id"`
		Relation    string `json:"relation"`
		SubjectType string `json:"subject_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	tuples, err := h.svc.ListSubjects(r.Context(), ListSubjectsRequest{
		OrgID:       orgID,
		ObjectType:  body.ObjectType,
		ObjectID:    body.ObjectID,
		Relation:    body.Relation,
		SubjectType: body.SubjectType,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type subject struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Relation string `json:"relation,omitempty"`
	}
	var subjects []subject
	for _, t := range tuples {
		subjects = append(subjects, subject{
			Type:     t.SubjectType,
			ID:       t.SubjectID,
			Relation: t.SubjectRelation,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"subjects": subjects})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
