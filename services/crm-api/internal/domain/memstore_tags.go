package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The in-memory half of tags (tags.go, migrations/0024_tags.sql).

type tagTables struct {
	mu          sync.Mutex
	definitions map[string]map[string]*TagDefinition
	assignments map[string]map[string]*TagAssignment
}

func newTagTables() *tagTables {
	return &tagTables{
		definitions: make(map[string]map[string]*TagDefinition),
		assignments: make(map[string]map[string]*TagAssignment),
	}
}

func tagNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// CreateTagDefinition stores a new tag. Names are unique per tenant,
// case-insensitively — the same property migrations/0024_tags.sql enforces
// with a unique index on lower(name), so a name collision reads the same way
// under either store.
func (s *MemStore) CreateTagDefinition(_ context.Context, scope Scope, v *TagDefinition) (*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneTagDefinition(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	for _, existing := range s.tags.definitions[scope.TenantID] {
		if tagNameKey(existing.Name) == tagNameKey(record.Name) {
			return nil, fmt.Errorf("%w: a tag named %q already exists", ErrConflict, record.Name)
		}
	}

	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()
	record.CreatedAt, record.UpdatedAt = now, now
	if s.tags.definitions[scope.TenantID] == nil {
		s.tags.definitions[scope.TenantID] = make(map[string]*TagDefinition)
	}
	s.tags.definitions[scope.TenantID][record.ID] = cloneTagDefinition(record)
	return cloneTagDefinition(record), nil
}

// GetTagDefinition reads one tag.
func (s *MemStore) GetTagDefinition(_ context.Context, scope Scope, id string) (*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	record, ok := s.tags.definitions[scope.TenantID][id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneTagDefinition(record), nil
}

// ListTagDefinitions returns the tenant's tags, ordered by name.
func (s *MemStore) ListTagDefinitions(_ context.Context, scope Scope) ([]*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	out := make([]*TagDefinition, 0, len(s.tags.definitions[scope.TenantID]))
	for _, record := range s.tags.definitions[scope.TenantID] {
		out = append(out, cloneTagDefinition(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateTagDefinition renames or recolors a tag.
func (s *MemStore) UpdateTagDefinition(_ context.Context, scope Scope, v *TagDefinition) (*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneTagDefinition(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	existing, ok := s.tags.definitions[scope.TenantID][record.ID]
	if !ok {
		return nil, ErrNotFound
	}
	for _, other := range s.tags.definitions[scope.TenantID] {
		if other.ID != record.ID && tagNameKey(other.Name) == tagNameKey(record.Name) {
			return nil, fmt.Errorf("%w: a tag named %q already exists", ErrConflict, record.Name)
		}
	}
	record.CreatedAt = existing.CreatedAt
	record.CreatedBy = existing.CreatedBy
	record.UpdatedAt = nowStamp()
	s.tags.definitions[scope.TenantID][record.ID] = cloneTagDefinition(record)
	return cloneTagDefinition(record), nil
}

// DeleteTagDefinition removes a tag and every assignment of it. There is no
// archive state for a tag the way there is for a pipeline or a custom field:
// a tag carries no history anything depends on, so retiring one that is no
// longer useful should not leave a permanent row behind.
func (s *MemStore) DeleteTagDefinition(_ context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	if _, ok := s.tags.definitions[scope.TenantID][id]; !ok {
		return ErrNotFound
	}
	delete(s.tags.definitions[scope.TenantID], id)
	for assignmentID, assignment := range s.tags.assignments[scope.TenantID] {
		if assignment.TagID == id {
			delete(s.tags.assignments[scope.TenantID], assignmentID)
		}
	}
	return nil
}

func assignmentKey(objectType, objectID, tagID string) string {
	return objectType + "|" + objectID + "|" + tagID
}

// AssignTag attaches a tag to a record. Idempotent: assigning a tag a record
// already carries returns the existing assignment rather than a conflict, so
// a bulk "add tag" action can retry safely without needing to check first.
func (s *MemStore) AssignTag(_ context.Context, scope Scope, v *TagAssignment) (*TagAssignment, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneTagAssignment(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	if _, ok := s.tags.definitions[scope.TenantID][record.TagID]; !ok {
		return nil, fmt.Errorf("%w: tag %q does not exist in this tenant", ErrInvalid, record.TagID)
	}

	key := assignmentKey(record.ObjectType, record.ObjectID, record.TagID)
	for _, existing := range s.tags.assignments[scope.TenantID] {
		if assignmentKey(existing.ObjectType, existing.ObjectID, existing.TagID) == key {
			return cloneTagAssignment(existing), nil
		}
	}

	if record.ID == "" {
		record.ID = newID()
	}
	record.CreatedAt = nowStamp()
	if s.tags.assignments[scope.TenantID] == nil {
		s.tags.assignments[scope.TenantID] = make(map[string]*TagAssignment)
	}
	s.tags.assignments[scope.TenantID][record.ID] = cloneTagAssignment(record)
	return cloneTagAssignment(record), nil
}

// RemoveTag detaches a tag from a record. Idempotent: removing a tag a
// record does not carry succeeds silently, for the same retry-safety reason
// AssignTag is idempotent.
func (s *MemStore) RemoveTag(_ context.Context, scope Scope, objectType, objectID, tagID string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	key := assignmentKey(objectType, objectID, tagID)
	for assignmentID, existing := range s.tags.assignments[scope.TenantID] {
		if assignmentKey(existing.ObjectType, existing.ObjectID, existing.TagID) == key {
			delete(s.tags.assignments[scope.TenantID], assignmentID)
			return nil
		}
	}
	return nil
}

// ListTagsForRecord returns the tags attached to one record, ordered by name.
func (s *MemStore) ListTagsForRecord(_ context.Context, scope Scope, objectType, objectID string) ([]*TagDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	out := make([]*TagDefinition, 0, 4)
	for _, assignment := range s.tags.assignments[scope.TenantID] {
		if assignment.ObjectType != objectType || assignment.ObjectID != objectID {
			continue
		}
		if def, ok := s.tags.definitions[scope.TenantID][assignment.TagID]; ok {
			out = append(out, cloneTagDefinition(def))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ListTaggedRecords returns one tag's assignments, optionally narrowed to one
// object type, ordered by id — the same simple id-keyset pagination
// ListLeads/ListOpportunities use for an unfiltered list. This is the filter
// surface a list page uses to answer "show me every X tagged Y" until
// per-entity filter structs grow a tag field of their own.
func (s *MemStore) ListTaggedRecords(
	_ context.Context, scope Scope, tagID, objectType string, page Page,
) ([]*TagAssignment, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	if strings.TrimSpace(tagID) == "" {
		return nil, "", ErrInvalid
	}

	s.tags.mu.Lock()
	defer s.tags.mu.Unlock()

	matches := make([]*TagAssignment, 0, 8)
	for _, assignment := range s.tags.assignments[scope.TenantID] {
		if assignment.TagID != tagID {
			continue
		}
		if objectType != "" && assignment.ObjectType != objectType {
			continue
		}
		if page.Token == "" || assignment.ID > page.Token {
			matches = append(matches, assignment)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })

	size := page.size()
	var next string
	if len(matches) > size {
		matches = matches[:size]
		next = matches[len(matches)-1].ID
	}
	out := make([]*TagAssignment, 0, len(matches))
	for _, m := range matches {
		out = append(out, cloneTagAssignment(m))
	}
	return out, next, nil
}
