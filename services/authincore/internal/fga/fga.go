// Package fga implements Fine-Grained Authorization using a Zanzibar-style
// relation tuple model.
//
// Core concept: every permission is a tuple:
//
//	(object_type:object_id, relation, subject_type:subject_id)
//
// Examples:
//
//	lead:123     editor    user:alice
//	lead:123     viewer    user:bob
//	deal:456     approver  user:charlie
//	deal:456     viewer    group:sales#member   ← all members of sales group
//
// Check: "can user:alice edit lead:123?"
//
//	→ look up tuple (lead:123, editor, user:alice) → yes
//
// With inheritance: define that "editor implies viewer"
//
//	→ check (lead:123, viewer, user:alice) → expands editor → yes
package fga

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/authincore/authincore/internal/metrics"
	"github.com/authincore/authincore/internal/tracing"
)

// Tuple is a single relation assertion.
// "subject has relation on object"
type Tuple struct {
	ObjectType string // e.g. "lead", "deal", "org"
	ObjectID   string // e.g. "123", "uuid"
	Relation   string // e.g. "editor", "viewer", "approver", "owner"

	SubjectType     string // e.g. "user", "group", "service_account"
	SubjectID       string // e.g. user UUID or group UUID
	SubjectRelation string // optional — for group expansion: "member"
}

func (t Tuple) Object() string { return t.ObjectType + ":" + t.ObjectID }
func (t Tuple) Subject() string {
	if t.SubjectRelation != "" {
		return t.SubjectType + ":" + t.SubjectID + "#" + t.SubjectRelation
	}
	return t.SubjectType + ":" + t.SubjectID
}

// CheckRequest asks: can subject perform relation on object?
type CheckRequest struct {
	OrgID       uuid.UUID
	ObjectType  string
	ObjectID    string
	Relation    string
	SubjectType string
	SubjectID   string
}

// ListObjectsRequest asks: which objects of type T can subject perform relation on?
type ListObjectsRequest struct {
	OrgID       uuid.UUID
	ObjectType  string
	Relation    string
	SubjectType string
	SubjectID   string
}

// ListSubjectsRequest asks: which subjects have relation on this object?
type ListSubjectsRequest struct {
	OrgID       uuid.UUID
	ObjectType  string
	ObjectID    string
	Relation    string
	SubjectType string // filter by subject type, empty = all
}

// Store persists and queries relation tuples.
type Store interface {
	// Write adds a relation tuple. Idempotent.
	Write(ctx context.Context, orgID uuid.UUID, tuple Tuple) error

	// Delete removes a relation tuple.
	Delete(ctx context.Context, orgID uuid.UUID, tuple Tuple) error

	// DirectTuples returns all tuples matching the given filters.
	// Any field left empty is treated as a wildcard.
	DirectTuples(ctx context.Context, orgID uuid.UUID, objectType, objectID, relation, subjectType, subjectID string) ([]Tuple, error)

	// DeleteObject removes all tuples for a given object (cascade on object delete).
	DeleteObject(ctx context.Context, orgID uuid.UUID, objectType, objectID string) error

	// DeleteSubject removes all tuples for a given subject (cascade on user delete).
	DeleteSubject(ctx context.Context, orgID uuid.UUID, subjectType, subjectID string) error
}

// Model defines which relations exist and inheritance rules per object type.
// Example:
//
//	"lead": {
//	    Relations: ["owner","editor","viewer","approver"],
//	    Implies: {"owner": ["editor"], "editor": ["viewer"]},
//	}
type Model struct {
	Types map[string]TypeDef `json:"types"`
}

type TypeDef struct {
	Relations []string            `json:"relations"`         // allowed relation names
	Implies   map[string][]string `json:"implies,omitempty"` // relation → relations it implies (inheritance)
}

// Service is the FGA engine. Check, list, write.
type Service struct {
	store Store
	model Model
}

func NewService(store Store, model Model) *Service {
	return &Service{store: store, model: model}
}

// DefaultModel returns a sensible default that covers most B2B SaaS patterns.
// Apps can extend this or replace it entirely.
func DefaultModel() Model {
	return Model{
		Types: map[string]TypeDef{
			// Generic resource — use for any object type not listed below
			"resource": {
				Relations: []string{"owner", "editor", "viewer"},
				Implies:   map[string][]string{"owner": {"editor"}, "editor": {"viewer"}},
			},
			// Org-level
			"org": {
				Relations: []string{"owner", "admin", "member"},
				Implies:   map[string][]string{"owner": {"admin"}, "admin": {"member"}},
			},
			// Project-level
			"project": {
				Relations: []string{"owner", "editor", "viewer"},
				Implies:   map[string][]string{"owner": {"editor"}, "editor": {"viewer"}},
			},
			// Generic document / record — Vymo leads, deals, tasks, etc.
			"document": {
				Relations: []string{"owner", "editor", "viewer", "approver", "commenter"},
				Implies: map[string][]string{
					"owner":    {"editor", "approver"},
					"editor":   {"viewer", "commenter"},
					"approver": {"viewer"},
				},
			},
		},
	}
}

// visitKey uniquely identifies a Check call; used to detect cycles in the relation graph.
type visitKey struct {
	objectType, objectID, relation, subjectType, subjectID string
}

// Check returns true if subject has relation on object (direct or via inheritance).
func (s *Service) Check(ctx context.Context, req CheckRequest) (bool, error) {
	ctx, span := tracing.Start(ctx, "fga.check")
	defer span.End()

	start := time.Now()
	allowed, err := s.checkVisited(ctx, req, make(map[visitKey]bool))
	metrics.FGACheckDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		return false, err
	}
	if allowed {
		metrics.FGACheckTotal.WithLabelValues("allowed").Inc()
	} else {
		metrics.FGACheckTotal.WithLabelValues("denied").Inc()
	}
	return allowed, nil
}

func (s *Service) checkVisited(ctx context.Context, req CheckRequest, visited map[visitKey]bool) (bool, error) {
	key := visitKey{req.ObjectType, req.ObjectID, req.Relation, req.SubjectType, req.SubjectID}
	if visited[key] {
		return false, nil // cycle: already evaluated this node
	}
	visited[key] = true

	if ok, err := s.checkDirect(ctx, req); err != nil || ok {
		return ok, err
	}
	if ok, err := s.checkInherited(ctx, req, visited); err != nil || ok {
		return ok, err
	}
	return s.checkGroupExpansion(ctx, req, visited)
}

func (s *Service) checkDirect(ctx context.Context, req CheckRequest) (bool, error) {
	tuples, err := s.store.DirectTuples(ctx, req.OrgID,
		req.ObjectType, req.ObjectID, req.Relation,
		req.SubjectType, req.SubjectID)
	if err != nil {
		return false, fmt.Errorf("fga check: %w", err)
	}
	return len(tuples) > 0, nil
}

func (s *Service) checkInherited(ctx context.Context, req CheckRequest, visited map[visitKey]bool) (bool, error) {
	td, ok := s.model.Types[req.ObjectType]
	if !ok {
		return false, nil
	}
	for parent, children := range td.Implies {
		for _, child := range children {
			if child != req.Relation {
				continue
			}
			parentReq := CheckRequest{
				OrgID: req.OrgID, ObjectType: req.ObjectType, ObjectID: req.ObjectID,
				Relation: parent, SubjectType: req.SubjectType, SubjectID: req.SubjectID,
			}
			if ok, err := s.checkVisited(ctx, parentReq, visited); err != nil || ok {
				return ok, err
			}
		}
	}
	return false, nil
}

func (s *Service) checkGroupExpansion(ctx context.Context, req CheckRequest, visited map[visitKey]bool) (bool, error) {
	groupTuples, err := s.store.DirectTuples(ctx, req.OrgID,
		req.ObjectType, req.ObjectID, req.Relation, "group", "")
	if err != nil {
		return false, fmt.Errorf("fga group expansion: %w", err)
	}
	for _, gt := range groupTuples {
		if gt.SubjectRelation == "" {
			continue
		}
		memberReq := CheckRequest{
			OrgID: req.OrgID, ObjectType: "group", ObjectID: gt.SubjectID,
			Relation: gt.SubjectRelation, SubjectType: req.SubjectType, SubjectID: req.SubjectID,
		}
		if ok, err := s.checkVisited(ctx, memberReq, visited); err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// Write adds a relation tuple. Idempotent.
func (s *Service) Write(ctx context.Context, orgID uuid.UUID, tuple Tuple) error {
	if err := s.validateTuple(tuple); err != nil {
		return err
	}
	return s.store.Write(ctx, orgID, tuple)
}

// Delete removes a relation tuple.
func (s *Service) Delete(ctx context.Context, orgID uuid.UUID, tuple Tuple) error {
	return s.store.Delete(ctx, orgID, tuple)
}

// ListObjects returns all object IDs of the given type where subject has the relation.
func (s *Service) ListObjects(ctx context.Context, req ListObjectsRequest) ([]string, error) {
	seen := make(map[string]bool)
	var ids []string
	add := func(objectID string) {
		if !seen[objectID] {
			seen[objectID] = true
			ids = append(ids, objectID)
		}
	}

	if err := s.collectDirect(ctx, req, add); err != nil {
		return nil, err
	}
	if err := s.collectInherited(ctx, req, add); err != nil {
		return nil, err
	}
	if err := s.collectViaGroups(ctx, req, seen, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Service) collectDirect(ctx context.Context, req ListObjectsRequest, add func(string)) error {
	tuples, err := s.store.DirectTuples(ctx, req.OrgID,
		req.ObjectType, "", req.Relation, req.SubjectType, req.SubjectID)
	if err != nil {
		return fmt.Errorf("fga list objects: %w", err)
	}
	for _, t := range tuples {
		add(t.ObjectID)
	}
	return nil
}

func (s *Service) collectInherited(ctx context.Context, req ListObjectsRequest, add func(string)) error {
	td, ok := s.model.Types[req.ObjectType]
	if !ok {
		return nil
	}
	for parent, children := range td.Implies {
		for _, child := range children {
			if child != req.Relation {
				continue
			}
			tuples, err := s.store.DirectTuples(ctx, req.OrgID,
				req.ObjectType, "", parent, req.SubjectType, req.SubjectID)
			if err != nil {
				return err
			}
			for _, t := range tuples {
				add(t.ObjectID)
			}
		}
	}
	return nil
}

// collectViaGroups adds objects reachable through group membership (Bug 13).
func (s *Service) collectViaGroups(ctx context.Context, req ListObjectsRequest, seen map[string]bool, ids *[]string) error {
	// Find all (objectType:*, relation, group:G#member) tuples regardless of subject.
	groupTuples, err := s.store.DirectTuples(ctx, req.OrgID,
		req.ObjectType, "", req.Relation, "group", "")
	if err != nil {
		return fmt.Errorf("fga list objects via groups: %w", err)
	}
	for _, gt := range groupTuples {
		if gt.SubjectRelation == "" {
			continue
		}
		memberReq := CheckRequest{
			OrgID: req.OrgID, ObjectType: "group", ObjectID: gt.SubjectID,
			Relation: gt.SubjectRelation, SubjectType: req.SubjectType, SubjectID: req.SubjectID,
		}
		ok, err := s.checkVisited(ctx, memberReq, make(map[visitKey]bool))
		if err != nil {
			return err
		}
		if ok && !seen[gt.ObjectID] {
			seen[gt.ObjectID] = true
			*ids = append(*ids, gt.ObjectID)
		}
	}
	return nil
}

// ListSubjects returns all subjects that have relation on the given object.
func (s *Service) ListSubjects(ctx context.Context, req ListSubjectsRequest) ([]Tuple, error) {
	return s.store.DirectTuples(ctx, req.OrgID,
		req.ObjectType, req.ObjectID, req.Relation,
		req.SubjectType, "")
}

// TupleFilter narrows a ListTuples browse query. Any empty field is a wildcard.
type TupleFilter struct {
	ObjectType  string
	ObjectID    string
	Relation    string
	SubjectType string
	SubjectID   string
}

// ListTuples returns all stored direct tuples matching the filter. It is the
// browse primitive behind the admin tuple viewer; empty filter fields match any
// value. Unlike Check/ListObjects it does not expand inheritance or groups — it
// returns exactly what is persisted.
func (s *Service) ListTuples(ctx context.Context, orgID uuid.UUID, f TupleFilter) ([]Tuple, error) {
	return s.store.DirectTuples(ctx, orgID,
		f.ObjectType, f.ObjectID, f.Relation, f.SubjectType, f.SubjectID)
}

// Model returns the authorization model (relation names and inheritance per
// object type) so callers such as the admin UI can present valid relations.
func (s *Service) Model() Model { return s.model }

// DeleteObject removes all permissions on a deleted object.
func (s *Service) DeleteObject(ctx context.Context, orgID uuid.UUID, objectType, objectID string) error {
	return s.store.DeleteObject(ctx, orgID, objectType, objectID)
}

// DeleteSubject removes all permissions for a deleted user/group.
func (s *Service) DeleteSubject(ctx context.Context, orgID uuid.UUID, subjectType, subjectID string) error {
	return s.store.DeleteSubject(ctx, orgID, subjectType, subjectID)
}

func (s *Service) validateTuple(t Tuple) error {
	if t.ObjectType == "" || t.ObjectID == "" {
		return fmt.Errorf("fga: object_type and object_id are required")
	}
	if t.Relation == "" {
		return fmt.Errorf("fga: relation is required")
	}
	if t.SubjectType == "" || t.SubjectID == "" {
		return fmt.Errorf("fga: subject_type and subject_id are required")
	}
	return nil
}
