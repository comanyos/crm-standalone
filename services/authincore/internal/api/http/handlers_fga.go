package http

import (
	"encoding/json"
	"net/http"

	"github.com/authincore/authincore/internal/fga"
)

// FGA (fine-grained authorization) admin handlers power the tuple viewer/editor
// in the admin UI. They live on the authenticated admin plane: the org is always
// taken from the request's tenancy context via orgIDFromRequest — never from the
// body — so an admin token cannot operate across tenants. The unauthenticated
// /authz routes (fga.Handler) remain for app/runtime permission checks.

// fgaTupleDTO is the wire shape for a relation tuple (snake_case for the UI).
type fgaTupleDTO struct {
	ObjectType      string `json:"object_type"`
	ObjectID        string `json:"object_id"`
	Relation        string `json:"relation"`
	SubjectType     string `json:"subject_type"`
	SubjectID       string `json:"subject_id"`
	SubjectRelation string `json:"subject_relation,omitempty"`
}

func (d fgaTupleDTO) toTuple() fga.Tuple {
	return fga.Tuple{
		ObjectType:      d.ObjectType,
		ObjectID:        d.ObjectID,
		Relation:        d.Relation,
		SubjectType:     d.SubjectType,
		SubjectID:       d.SubjectID,
		SubjectRelation: d.SubjectRelation,
	}
}

func fgaTupleToDTO(t fga.Tuple) fgaTupleDTO {
	return fgaTupleDTO{
		ObjectType:      t.ObjectType,
		ObjectID:        t.ObjectID,
		Relation:        t.Relation,
		SubjectType:     t.SubjectType,
		SubjectID:       t.SubjectID,
		SubjectRelation: t.SubjectRelation,
	}
}

// GET /api/v1/admin/fga/tuples — browse tuples; query params are wildcard filters.
func (s *Server) handleFGAListTuples(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	q := r.URL.Query()
	tuples, err := s.deps.FGAService.ListTuples(r.Context(), orgID, fga.TupleFilter{
		ObjectType:  q.Get("object_type"),
		ObjectID:    q.Get("object_id"),
		Relation:    q.Get("relation"),
		SubjectType: q.Get("subject_type"),
		SubjectID:   q.Get("subject_id"),
	})
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	items := make([]fgaTupleDTO, 0, len(tuples))
	for _, t := range tuples {
		items = append(items, fgaTupleToDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuples": items, "total": len(items)})
}

// POST /api/v1/admin/fga/tuples — add a relation tuple. Idempotent.
func (s *Server) handleFGAWriteTuple(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body fgaTupleDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.deps.FGAService.Write(r.Context(), orgID, body.toTuple()); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok"})
}

// DELETE /api/v1/admin/fga/tuples — remove a relation tuple.
func (s *Server) handleFGADeleteTuple(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body fgaTupleDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.deps.FGAService.Delete(r.Context(), orgID, body.toTuple()); err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/v1/admin/fga/check — test "can subject do relation on object?".
func (s *Server) handleFGACheck(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromRequest(r)
	var body struct {
		ObjectType  string `json:"object_type"`
		ObjectID    string `json:"object_id"`
		Relation    string `json:"relation"`
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid body", http.StatusBadRequest)
		return
	}
	allowed, err := s.deps.FGAService.Check(r.Context(), fga.CheckRequest{
		OrgID:       orgID,
		ObjectType:  body.ObjectType,
		ObjectID:    body.ObjectID,
		Relation:    body.Relation,
		SubjectType: body.SubjectType,
		SubjectID:   body.SubjectID,
	})
	if err != nil {
		httpError(w, err, s.deps.Log)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"allowed": allowed})
}

// GET /api/v1/admin/fga/model — the authorization model (relations + inheritance).
func (s *Server) handleFGAModel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.FGAService.Model())
}
