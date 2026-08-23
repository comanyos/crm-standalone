package domain

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The in-memory half of the Google integration store. It exists to keep the
// development/test store behaviourally identical to PostgreSQL — including
// the two properties that actually matter for security: a tenant never reads
// another tenant's connection, and an OAuth state can be consumed exactly
// once.
//
// It does not reuse memTable[T]: neither record is an Entity (a connection is
// addressed by its owner, a state by its opaque token), and the atomic
// consume has no equivalent in the generic table.

type googleTables struct {
	mu sync.Mutex
	// connections is tenant -> owner -> connection.
	connections map[string]map[string]*GoogleConnection
	// states is tenant -> state -> record. The state value is globally
	// unique, which is what makes resolveGoogleStateTenant unambiguous.
	states map[string]map[string]*GoogleOAuthState
}

func newGoogleTables() *googleTables {
	return &googleTables{
		connections: make(map[string]map[string]*GoogleConnection),
		states:      make(map[string]map[string]*GoogleOAuthState),
	}
}

// CreateGoogleOAuthState records a pending authorization before the browser
// is redirected to Google.
func (s *MemStore) CreateGoogleOAuthState(
	_ context.Context, scope Scope, v *GoogleOAuthState,
) (*GoogleOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneGoogleOAuthState(v)
	record.TenantID = scope.TenantID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = nowStamp()
	}
	record.ConsumedAt = time.Time{}
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	for _, byState := range s.google.states {
		if _, taken := byState[record.State]; taken {
			return nil, ErrConflict
		}
	}
	if s.google.states[scope.TenantID] == nil {
		s.google.states[scope.TenantID] = make(map[string]*GoogleOAuthState)
	}
	s.google.states[scope.TenantID][record.State] = cloneGoogleOAuthState(record)
	return cloneGoogleOAuthState(record), nil
}

// ConsumeGoogleOAuthState redeems a pending authorization exactly once.
//
// The whole check-and-mark happens under one lock. Doing it as a read then a
// write would let two concurrent callbacks — which is what a replayed
// redirect looks like — both observe an unconsumed state and both proceed.
func (s *MemStore) ConsumeGoogleOAuthState(
	_ context.Context, scope Scope, state string, now time.Time,
) (*GoogleOAuthState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	record, ok := s.google.states[scope.TenantID][state]
	if !ok || !record.Usable(now) {
		// Deliberately ErrNotFound for consumed and expired alike: the
		// caller is an unauthenticated redirect, and telling it which of the
		// two happened is telling it whether the state was ever real.
		return nil, ErrNotFound
	}
	record.ConsumedAt = now.UTC().Truncate(storedTimePrecision)
	return cloneGoogleOAuthState(record), nil
}

// PurgeExpiredGoogleOAuthStates deletes states that can no longer be
// redeemed. Consumed states are swept too: once redeemed the record has no
// further use, and keeping the sealed verifier around has only downside.
func (s *MemStore) PurgeExpiredGoogleOAuthStates(
	_ context.Context, scope Scope, now time.Time,
) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	removed := 0
	for state, record := range s.google.states[scope.TenantID] {
		if !record.Usable(now) {
			delete(s.google.states[scope.TenantID], state)
			removed++
		}
	}
	return removed, nil
}

// ResolveGoogleOAuthStateTenant answers "which tenant does this callback
// belong to" for an unauthenticated redirect.
//
// Like DueOutboxTenants this is the rare read with no tenant to bind, and it
// is held to the same rule: it returns an identifier and nothing else. The
// caller must build a real Scope from it and do every subsequent read and
// write through the tenant-scoped methods. The lookup key is 256 bits of
// entropy that only the browser completing its own authorization holds.
func (s *MemStore) ResolveGoogleOAuthStateTenant(_ context.Context, state string) (string, error) {
	if state == "" {
		return "", ErrNotFound
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	for tenant, byState := range s.google.states {
		if _, ok := byState[state]; ok {
			return tenant, nil
		}
	}
	return "", ErrNotFound
}

// SaveGoogleConnection upserts one owner's connection.
//
// Reconnecting updates the existing row rather than adding a second one: two
// rows would mean two live refresh tokens for one mailbox, and disconnecting
// would revoke only whichever the code happened to read.
func (s *MemStore) SaveGoogleConnection(
	_ context.Context, scope Scope, v *GoogleConnection,
) (*GoogleConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneGoogleConnection(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	if s.google.connections[scope.TenantID] == nil {
		s.google.connections[scope.TenantID] = make(map[string]*GoogleConnection)
	}
	now := nowStamp()
	if existing, ok := s.google.connections[scope.TenantID][record.OwnerID]; ok {
		record.ID = existing.ID
		record.CreatedAt = existing.CreatedAt
	} else {
		if record.ID == "" {
			record.ID = newID()
		}
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	s.google.connections[scope.TenantID][record.OwnerID] = cloneGoogleConnection(record)
	return cloneGoogleConnection(record), nil
}

// GetGoogleConnection returns one owner's connection, or ErrNotFound.
func (s *MemStore) GetGoogleConnection(
	_ context.Context, scope Scope, ownerID string,
) (*GoogleConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	record, ok := s.google.connections[scope.TenantID][ownerID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneGoogleConnection(record), nil
}

// ListGoogleConnections returns the tenant's connections, ordered by owner.
func (s *MemStore) ListGoogleConnections(
	_ context.Context, scope Scope,
) ([]*GoogleConnection, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	out := make([]*GoogleConnection, 0, len(s.google.connections[scope.TenantID]))
	for _, record := range s.google.connections[scope.TenantID] {
		out = append(out, cloneGoogleConnection(record))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OwnerID < out[j].OwnerID })
	return out, nil
}

// DeleteGoogleConnection erases the row entirely — the purge a user asks for
// when they want no record of the link at all, as distinct from a disconnect,
// which revokes the credential and keeps a tombstone for audit.
func (s *MemStore) DeleteGoogleConnection(_ context.Context, scope Scope, ownerID string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}

	s.google.mu.Lock()
	defer s.google.mu.Unlock()

	if _, ok := s.google.connections[scope.TenantID][ownerID]; !ok {
		return ErrNotFound
	}
	delete(s.google.connections[scope.TenantID], ownerID)
	return nil
}
