package domain

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type metaTables struct {
	mu                    sync.Mutex
	connections           map[string]*MetaConnection
	states                map[string]map[string]*MetaOAuthState
	attributionByProvider map[string]map[string]*MetaLeadAttribution
	attributionByLead     map[string]map[string]*MetaLeadAttribution
}

func newMetaTables() *metaTables {
	return &metaTables{
		connections:           make(map[string]*MetaConnection),
		states:                make(map[string]map[string]*MetaOAuthState),
		attributionByProvider: make(map[string]map[string]*MetaLeadAttribution),
		attributionByLead:     make(map[string]map[string]*MetaLeadAttribution),
	}
}

func (s *MemStore) CreateMetaOAuthState(_ context.Context, scope Scope, v *MetaOAuthState) (*MetaOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMetaOAuthState(v)
	record.TenantID = scope.TenantID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = nowStamp()
	}
	record.ConsumedAt = time.Time{}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	for _, rows := range s.meta.states {
		if _, exists := rows[record.State]; exists {
			return nil, ErrConflict
		}
	}
	if s.meta.states[scope.TenantID] == nil {
		s.meta.states[scope.TenantID] = make(map[string]*MetaOAuthState)
	}
	s.meta.states[scope.TenantID][record.State] = cloneMetaOAuthState(record)
	return cloneMetaOAuthState(record), nil
}

func (s *MemStore) ConsumeMetaOAuthState(_ context.Context, scope Scope, state string, now time.Time) (*MetaOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	record, ok := s.meta.states[scope.TenantID][state]
	if !ok || !record.Usable(now) {
		return nil, ErrNotFound
	}
	record.ConsumedAt = now.UTC().Truncate(storedTimePrecision)
	return cloneMetaOAuthState(record), nil
}

func (s *MemStore) PurgeExpiredMetaOAuthStates(_ context.Context, scope Scope, now time.Time) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	removed := 0
	for state, record := range s.meta.states[scope.TenantID] {
		if !record.Usable(now) {
			delete(s.meta.states[scope.TenantID], state)
			removed++
		}
	}
	return removed, nil
}

func (s *MemStore) ResolveMetaOAuthStateTenant(_ context.Context, state string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", ErrNotFound
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	for tenantID, rows := range s.meta.states {
		if _, ok := rows[state]; ok {
			return tenantID, nil
		}
	}
	return "", ErrNotFound
}

func (s *MemStore) SaveMetaConnection(_ context.Context, scope Scope, v *MetaConnection) (*MetaConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMetaConnection(v)
	if record.ID == "" {
		record.ID = newID()
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	now := nowStamp()
	if record.Status == MetaStatusConnected {
		for tenantID, existing := range s.meta.connections {
			if tenantID != scope.TenantID && existing.Status == MetaStatusConnected &&
				existing.PageID == record.PageID && existing.FormID == record.FormID {
				return nil, ErrConflict
			}
		}
	}
	if existing := s.meta.connections[scope.TenantID]; existing != nil {
		if record.ID != existing.ID {
			return nil, ErrConflict
		}
		record.CreatedAt = existing.CreatedAt
	} else if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	s.meta.connections[scope.TenantID] = cloneMetaConnection(record)
	return cloneMetaConnection(record), nil
}

func (s *MemStore) GetMetaConnection(_ context.Context, scope Scope) (*MetaConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	record := s.meta.connections[scope.TenantID]
	if record == nil {
		return nil, ErrNotFound
	}
	return cloneMetaConnection(record), nil
}

func (s *MemStore) DeleteMetaConnection(_ context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	record := s.meta.connections[scope.TenantID]
	if record == nil || record.ID != id {
		return ErrNotFound
	}
	delete(s.meta.connections, scope.TenantID)
	delete(s.meta.attributionByProvider, scope.TenantID)
	delete(s.meta.attributionByLead, scope.TenantID)
	return nil
}

func (s *MemStore) ResolveMetaConnections(_ context.Context, pageID, formID string) ([]MetaConnectionRef, error) {
	pageID, formID = strings.TrimSpace(pageID), strings.TrimSpace(formID)
	if pageID == "" || formID == "" {
		return nil, ErrNotFound
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	refs := make([]MetaConnectionRef, 0, 1)
	for tenantID, record := range s.meta.connections {
		if record.Status == MetaStatusConnected && record.PageID == pageID && record.FormID == formID {
			refs = append(refs, MetaConnectionRef{
				TenantID: tenantID, ConnectionID: record.ID,
				PageTokenCiphertext: append([]byte(nil), record.PageTokenCiphertext...),
				BusinessID:          record.BusinessID, PageID: pageID, FormID: formID, Status: record.Status,
			})
		}
	}
	if len(refs) == 0 {
		return nil, ErrNotFound
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].TenantID < refs[j].TenantID })
	return refs, nil
}

func (s *MemStore) CreateMetaCapturedLead(ctx context.Context, scope Scope, lead *Lead, attribution *MetaLeadAttribution) (*Lead, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if lead == nil || attribution == nil {
		return nil, false, ErrInvalid
	}
	check := cloneMetaAttribution(attribution)
	if check.CapturedAt.IsZero() {
		check.CapturedAt = nowStamp()
	}
	if err := check.Validate(); err != nil {
		return nil, false, err
	}

	s.compound.Lock()
	defer s.compound.Unlock()
	s.meta.mu.Lock()
	existing := s.meta.attributionByProvider[scope.TenantID][check.ProviderLeadID]
	s.meta.mu.Unlock()
	if existing != nil {
		stored, err := s.GetLead(ctx, scope, existing.LeadID)
		return stored, false, err
	}

	stored, err := s.CreateLead(ctx, scope, lead)
	if err != nil {
		return nil, false, err
	}
	check.LeadID = stored.ID
	if err := check.Validate(); err != nil {
		_ = s.lead.delete(scope, stored.ID)
		return nil, false, err
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	if s.meta.attributionByProvider[scope.TenantID] == nil {
		s.meta.attributionByProvider[scope.TenantID] = make(map[string]*MetaLeadAttribution)
		s.meta.attributionByLead[scope.TenantID] = make(map[string]*MetaLeadAttribution)
	}
	s.meta.attributionByProvider[scope.TenantID][check.ProviderLeadID] = cloneMetaAttribution(check)
	s.meta.attributionByLead[scope.TenantID][check.LeadID] = cloneMetaAttribution(check)
	return stored, true, nil
}

func (s *MemStore) GetMetaLeadAttribution(_ context.Context, scope Scope, leadID string) (*MetaLeadAttribution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.meta.mu.Lock()
	defer s.meta.mu.Unlock()
	record := s.meta.attributionByLead[scope.TenantID][leadID]
	if record == nil {
		return nil, ErrNotFound
	}
	return cloneMetaAttribution(record), nil
}
