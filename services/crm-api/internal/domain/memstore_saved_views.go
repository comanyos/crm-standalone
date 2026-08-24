package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The in-memory half of saved views (saved_views.go, migrations/0025_saved_views.sql).

type savedViewTables struct {
	mu    sync.Mutex
	views map[string]map[string]*SavedView
}

func newSavedViewTables() *savedViewTables {
	return &savedViewTables{views: make(map[string]map[string]*SavedView)}
}

func savedViewNameKey(objectType, name string) string {
	return objectType + "|" + strings.ToLower(strings.TrimSpace(name))
}

// CreateSavedView stores a new view. Names are unique per owner per object
// type, case-insensitively.
func (s *MemStore) CreateSavedView(_ context.Context, scope Scope, v *SavedView) (*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneSavedView(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.savedViews.mu.Lock()
	defer s.savedViews.mu.Unlock()

	for _, existing := range s.savedViews.views[scope.TenantID] {
		if existing.OwnerID == record.OwnerID &&
			savedViewNameKey(existing.ObjectType, existing.Name) == savedViewNameKey(record.ObjectType, record.Name) {
			return nil, fmt.Errorf("%w: you already have a view named %q for %s", ErrConflict, record.Name, record.ObjectType)
		}
	}

	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()
	record.CreatedAt, record.UpdatedAt = now, now
	if s.savedViews.views[scope.TenantID] == nil {
		s.savedViews.views[scope.TenantID] = make(map[string]*SavedView)
	}
	s.savedViews.views[scope.TenantID][record.ID] = cloneSavedView(record)
	return cloneSavedView(record), nil
}

// GetSavedView reads one of the caller's own views. ownerID is required, not
// a filter: matching notifications' own convention, the lookup fails a view
// that exists but belongs to someone else exactly like one that does not
// exist at all.
func (s *MemStore) GetSavedView(_ context.Context, scope Scope, ownerID, id string) (*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.savedViews.mu.Lock()
	defer s.savedViews.mu.Unlock()

	record, ok := s.savedViews.views[scope.TenantID][id]
	if !ok || record.OwnerID != ownerID {
		return nil, ErrNotFound
	}
	return cloneSavedView(record), nil
}

// ListSavedViews returns the caller's own views, optionally narrowed to one
// object type, ordered by name.
func (s *MemStore) ListSavedViews(_ context.Context, scope Scope, ownerID, objectType string) ([]*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if ownerID == "" {
		return nil, ErrInvalid
	}

	s.savedViews.mu.Lock()
	defer s.savedViews.mu.Unlock()

	out := make([]*SavedView, 0, 4)
	for _, record := range s.savedViews.views[scope.TenantID] {
		if record.OwnerID != ownerID {
			continue
		}
		if objectType != "" && record.ObjectType != objectType {
			continue
		}
		out = append(out, cloneSavedView(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateSavedView replaces one of the caller's own views.
func (s *MemStore) UpdateSavedView(_ context.Context, scope Scope, ownerID string, v *SavedView) (*SavedView, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneSavedView(v)
	record.OwnerID = ownerID
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.savedViews.mu.Lock()
	defer s.savedViews.mu.Unlock()

	existing, ok := s.savedViews.views[scope.TenantID][record.ID]
	if !ok || existing.OwnerID != ownerID {
		return nil, ErrNotFound
	}
	for _, other := range s.savedViews.views[scope.TenantID] {
		if other.ID != record.ID && other.OwnerID == ownerID &&
			savedViewNameKey(other.ObjectType, other.Name) == savedViewNameKey(record.ObjectType, record.Name) {
			return nil, fmt.Errorf("%w: you already have a view named %q for %s", ErrConflict, record.Name, record.ObjectType)
		}
	}
	record.CreatedAt = existing.CreatedAt
	record.UpdatedAt = nowStamp()
	s.savedViews.views[scope.TenantID][record.ID] = cloneSavedView(record)
	return cloneSavedView(record), nil
}

// DeleteSavedView removes one of the caller's own views. Idempotent from the
// caller's point of view for a view that never existed in the first place
// would be indistinguishable from one owned by someone else, so both report
// ErrNotFound rather than success — a delete that silently no-ops on a
// typo'd id would hide the mistake instead of surfacing it.
func (s *MemStore) DeleteSavedView(_ context.Context, scope Scope, ownerID, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}

	s.savedViews.mu.Lock()
	defer s.savedViews.mu.Unlock()

	existing, ok := s.savedViews.views[scope.TenantID][id]
	if !ok || existing.OwnerID != ownerID {
		return ErrNotFound
	}
	delete(s.savedViews.views[scope.TenantID], id)
	return nil
}
