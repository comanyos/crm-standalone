package domain

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The in-memory half of Calendar synchronisation. Its job, as with Gmail, is
// to behave exactly as PostgreSQL does on the properties sync depends on:
// one pass per calendar at a time, and one timeline entry per material change.

type calendarTables struct {
	mu     sync.Mutex
	state  map[string]map[string]*CalendarSyncState
	events map[string]map[calendarEntryKey]*CalendarEvent
}

type calendarEntryKey struct {
	owner, eventID, subjectType, subjectID string
}

func newCalendarTables() *calendarTables {
	return &calendarTables{
		state:  make(map[string]map[string]*CalendarSyncState),
		events: make(map[string]map[calendarEntryKey]*CalendarEvent),
	}
}

// GetCalendarSyncState returns one calendar's cursor, or ErrNotFound.
func (s *MemStore) GetCalendarSyncState(
	_ context.Context, scope Scope, ownerID string,
) (*CalendarSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	record, ok := s.calendar.state[scope.TenantID][ownerID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneCalendarSyncState(record), nil
}

// SaveCalendarSyncState upserts one calendar's cursor.
func (s *MemStore) SaveCalendarSyncState(
	_ context.Context, scope Scope, v *CalendarSyncState,
) (*CalendarSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneCalendarSyncState(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}

	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	if s.calendar.state[scope.TenantID] == nil {
		s.calendar.state[scope.TenantID] = make(map[string]*CalendarSyncState)
	}
	now := nowStamp()
	if existing, ok := s.calendar.state[scope.TenantID][record.OwnerID]; ok {
		record.CreatedAt = existing.CreatedAt
	} else {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	s.calendar.state[scope.TenantID][record.OwnerID] = cloneCalendarSyncState(record)
	return cloneCalendarSyncState(record), nil
}

// ClaimCalendarSync leases a calendar for one pass.
func (s *MemStore) ClaimCalendarSync(
	_ context.Context, scope Scope, ownerID string, now time.Time, lease time.Duration,
) (*CalendarSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	if s.calendar.state[scope.TenantID] == nil {
		s.calendar.state[scope.TenantID] = make(map[string]*CalendarSyncState)
	}
	record, ok := s.calendar.state[scope.TenantID][ownerID]
	if !ok {
		record = &CalendarSyncState{OwnerID: ownerID, CreatedAt: nowStamp()}
	} else if !record.SyncingUntil.IsZero() && now.UTC().Before(record.SyncingUntil.UTC()) {
		return nil, ErrConflict
	}
	record.SyncingUntil = now.UTC().Add(lease).Truncate(storedTimePrecision)
	record.UpdatedAt = nowStamp()
	s.calendar.state[scope.TenantID][ownerID] = record
	return cloneCalendarSyncState(record), nil
}

// DueCalendarMailboxes lists unclaimed calendars, least recently synced first.
func (s *MemStore) DueCalendarMailboxes(
	_ context.Context, now time.Time, limit int,
) ([]GmailMailboxRef, error) {
	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	type candidate struct {
		ref  GmailMailboxRef
		last time.Time
	}
	var due []candidate
	for tenant, byOwner := range s.calendar.state {
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

// RecordCalendarEvent mirrors an event and appends a timeline entry when the
// material state changed.
//
// The comparison, the mirror write and the Activity all happen under one
// lock, for the same reason PgStore does them in one transaction: two passes
// seeing the same "changed" state would both post.
func (s *MemStore) RecordCalendarEvent(
	ctx context.Context, scope Scope, event *CalendarEvent,
	activityFor func(CalendarChange) *Activity,
) (CalendarChange, error) {
	if !scope.Valid() {
		return CalendarUnchanged, ErrNoTenant
	}
	if event == nil || activityFor == nil {
		return CalendarUnchanged, ErrInvalid
	}
	if err := event.Validate(); err != nil {
		return CalendarUnchanged, err
	}
	if err := requireActivitySubject(ctx, s, scope, event.SubjectType, event.SubjectID); err != nil {
		return CalendarUnchanged, err
	}

	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	key := calendarEntryKey{event.OwnerID, event.ProviderEventID, event.SubjectType, event.SubjectID}
	if s.calendar.events[scope.TenantID] == nil {
		s.calendar.events[scope.TenantID] = make(map[calendarEntryKey]*CalendarEvent)
	}
	existing := s.calendar.events[scope.TenantID][key]
	if existing != nil && !existing.ErasedAt.IsZero() {
		// Erased. Leaving it alone is the whole point: an event that was
		// erased and is later rescheduled must not post a fresh timeline
		// entry carrying the title that was just removed. The suppression
		// list stops future collection; this stops resurrection of this one.
		return CalendarUnchanged, nil
	}

	previousKey := ""
	if existing != nil {
		previousKey = existing.StateKey
	}
	change := ClassifyCalendarChange(previousKey, event)

	stored := cloneCalendarEvent(event)
	now := nowStamp()
	stored.UpdatedAt = now
	if existing != nil {
		stored.CreatedAt = existing.CreatedAt
		// The first Activity's id is the row's identity; a later entry gets
		// its own Activity but does not renumber the row.
		stored.ActivityID = existing.ActivityID
	} else {
		stored.CreatedAt = now
	}

	if change == CalendarUnchanged {
		// The mirror still moves — a retitled or re-described event is worth
		// having current — but nothing reaches the timeline.
		stored.StateKey = previousKey
		s.calendar.events[scope.TenantID][key] = stored
		return CalendarUnchanged, nil
	}

	activity := activityFor(change)
	if activity == nil {
		return CalendarUnchanged, ErrInvalid
	}
	if existing == nil {
		stored.ActivityID = activity.ID
	}
	if _, err := s.activity.create(scope, activity); err != nil {
		return CalendarUnchanged, err
	}
	stored.StateKey = event.MaterialStateKey()
	s.calendar.events[scope.TenantID][key] = stored
	return change, nil
}

// ListCalendarEventsForSubject returns one record's meetings, newest first.
func (s *MemStore) ListCalendarEventsForSubject(
	_ context.Context, scope Scope, subjectType, subjectID string, limit int,
) ([]*CalendarEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	out := make([]*CalendarEvent, 0, 8)
	for key, record := range s.calendar.events[scope.TenantID] {
		if key.subjectType == subjectType && key.subjectID == subjectID {
			out = append(out, cloneCalendarEvent(record))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartsAt.Equal(out[j].StartsAt) {
			return out[i].StartsAt.After(out[j].StartsAt)
		}
		return out[i].ProviderEventID < out[j].ProviderEventID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PurgeCalendarDataForOwner drops the cursor, and the mirror too when asked.
func (s *MemStore) PurgeCalendarDataForOwner(
	_ context.Context, scope Scope, ownerID string, includeIndexedEvents bool,
) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}

	s.calendar.mu.Lock()
	defer s.calendar.mu.Unlock()

	delete(s.calendar.state[scope.TenantID], ownerID)
	if !includeIndexedEvents {
		return 0, nil
	}
	removed := 0
	for key := range s.calendar.events[scope.TenantID] {
		if key.owner == ownerID {
			delete(s.calendar.events[scope.TenantID], key)
			removed++
		}
	}
	return removed, nil
}
