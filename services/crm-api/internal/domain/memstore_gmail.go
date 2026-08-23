package domain

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The in-memory half of Gmail synchronisation.
//
// Its job is to make the development and test store behave the same as
// PostgreSQL on the two properties sync depends on: a mailbox can be claimed
// by only one pass at a time, and a timeline entry is written at most once.
// Both are cheap to get wrong in a way no single-threaded test would notice.

type gmailTables struct {
	mu sync.Mutex
	// state is tenant -> owner -> cursor.
	state map[string]map[string]*GmailSyncState
	// messages is tenant -> timeline-entry key -> entry.
	messages map[string]map[gmailEntryKey]*GmailMessage
}

// gmailEntryKey mirrors the primary key of gmail_messages: one entry per
// message per matched record, which is what makes each timeline entry
// individually idempotent.
type gmailEntryKey struct {
	owner, messageID, subjectType, subjectID string
}

func newGmailTables() *gmailTables {
	return &gmailTables{
		state:    make(map[string]map[string]*GmailSyncState),
		messages: make(map[string]map[gmailEntryKey]*GmailMessage),
	}
}

// GetGmailSyncState returns one mailbox's cursor, or ErrNotFound.
func (s *MemStore) GetGmailSyncState(
	_ context.Context, scope Scope, ownerID string,
) (*GmailSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	record, ok := s.gmail.state[scope.TenantID][ownerID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneGmailSyncState(record), nil
}

// SaveGmailSyncState upserts one mailbox's cursor.
func (s *MemStore) SaveGmailSyncState(
	_ context.Context, scope Scope, v *GmailSyncState,
) (*GmailSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneGmailSyncState(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	if s.gmail.state[scope.TenantID] == nil {
		s.gmail.state[scope.TenantID] = make(map[string]*GmailSyncState)
	}
	now := nowStamp()
	if existing, ok := s.gmail.state[scope.TenantID][record.OwnerID]; ok {
		record.CreatedAt = existing.CreatedAt
	} else {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	s.gmail.state[scope.TenantID][record.OwnerID] = cloneGmailSyncState(record)
	return cloneGmailSyncState(record), nil
}

// ClaimGmailSync leases a mailbox for one pass, creating its state row on
// first sight. A live lease held by someone else is ErrConflict.
func (s *MemStore) ClaimGmailSync(
	_ context.Context, scope Scope, ownerID string, now time.Time, lease time.Duration,
) (*GmailSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	if s.gmail.state[scope.TenantID] == nil {
		s.gmail.state[scope.TenantID] = make(map[string]*GmailSyncState)
	}
	record, ok := s.gmail.state[scope.TenantID][ownerID]
	if !ok {
		record = &GmailSyncState{OwnerID: ownerID, CreatedAt: nowStamp()}
	} else if !record.SyncingUntil.IsZero() && now.UTC().Before(record.SyncingUntil.UTC()) {
		return nil, ErrConflict
	}
	record.SyncingUntil = now.UTC().Add(lease).Truncate(storedTimePrecision)
	record.UpdatedAt = nowStamp()
	s.gmail.state[scope.TenantID][ownerID] = record
	return cloneGmailSyncState(record), nil
}

// DueGmailMailboxes lists unclaimed mailboxes, least recently synced first.
//
// Like DueOutboxTenants this is the rare read with no tenant to bind, and it
// is held to the same rule: identifiers only.
func (s *MemStore) DueGmailMailboxes(
	_ context.Context, now time.Time, limit int,
) ([]GmailMailboxRef, error) {
	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	type candidate struct {
		ref  GmailMailboxRef
		last time.Time
	}
	var due []candidate
	for tenant, byOwner := range s.gmail.state {
		for owner, record := range byOwner {
			if !record.SyncingUntil.IsZero() && now.UTC().Before(record.SyncingUntil.UTC()) {
				continue
			}
			due = append(due, candidate{GmailMailboxRef{TenantID: tenant, OwnerID: owner}, record.LastSyncedAt})
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].last.Equal(due[j].last) {
			return due[i].last.Before(due[j].last)
		}
		if due[i].ref.TenantID != due[j].ref.TenantID {
			return due[i].ref.TenantID < due[j].ref.TenantID
		}
		return due[i].ref.OwnerID < due[j].ref.OwnerID
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	out := make([]GmailMailboxRef, 0, len(due))
	for _, c := range due {
		out = append(out, c.ref)
	}
	return out, nil
}

// RecordGmailActivity writes one timeline entry, or reports that it already
// exists.
//
// Both writes happen under one lock for the same reason PgStore does them in
// one transaction: an Activity without its index row would be re-created on
// the next pass, and an index row without its Activity is a timeline entry
// that never appears.
func (s *MemStore) RecordGmailActivity(
	ctx context.Context, scope Scope, msg *GmailMessage, activity *Activity,
) (bool, error) {
	if !scope.Valid() {
		return false, ErrNoTenant
	}
	if msg == nil || activity == nil {
		return false, ErrInvalid
	}
	if err := msg.Validate(); err != nil {
		return false, err
	}
	if err := activity.Validate(); err != nil {
		return false, err
	}
	if err := requireActivitySubject(ctx, s, scope, activity.SubjectType, activity.SubjectID); err != nil {
		return false, err
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	key := gmailEntryKey{msg.OwnerID, msg.ProviderMessageID, msg.SubjectType, msg.SubjectID}
	if s.gmail.messages[scope.TenantID] == nil {
		s.gmail.messages[scope.TenantID] = make(map[gmailEntryKey]*GmailMessage)
	}
	if _, exists := s.gmail.messages[scope.TenantID][key]; exists {
		return false, nil
	}

	stored := cloneGmailMessage(msg)
	stored.CreatedAt = nowStamp()
	activity.ID = msg.ActivityID
	if _, err := s.activity.create(scope, activity); err != nil {
		return false, err
	}
	s.gmail.messages[scope.TenantID][key] = stored
	return true, nil
}

// ListGmailMessagesForSubject returns one record's synced correspondence,
// newest first.
func (s *MemStore) ListGmailMessagesForSubject(
	_ context.Context, scope Scope, subjectType, subjectID string, limit int,
) ([]*GmailMessage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	out := make([]*GmailMessage, 0, 8)
	for key, record := range s.gmail.messages[scope.TenantID] {
		if key.subjectType == subjectType && key.subjectID == subjectID {
			out = append(out, cloneGmailMessage(record))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.After(out[j].OccurredAt)
		}
		return out[i].ProviderMessageID < out[j].ProviderMessageID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PurgeGmailDataForOwner drops one mailbox's cursor, and the index rows too
// when asked. See the Store interface for why disconnecting does not ask.
func (s *MemStore) PurgeGmailDataForOwner(
	_ context.Context, scope Scope, ownerID string, includeIndexedMessages bool,
) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	delete(s.gmail.state[scope.TenantID], ownerID)
	if !includeIndexedMessages {
		return 0, nil
	}
	removed := 0
	for key := range s.gmail.messages[scope.TenantID] {
		if key.owner == ownerID {
			delete(s.gmail.messages[scope.TenantID], key)
			removed++
		}
	}
	return removed, nil
}
