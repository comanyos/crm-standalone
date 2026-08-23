package domain

import (
	"context"
	"sort"
)

// The in-memory half of erasure and suppression.

// EraseSyncedContent removes synced timeline entries, redacts their index
// rows, and suppresses addresses — all under the locks that make it one
// operation, mirroring PgStore's single transaction.
func (s *MemStore) EraseSyncedContent(
	_ context.Context, scope Scope, in EraseSubject,
) (ErasureResult, error) {
	if !scope.Valid() {
		return ErasureResult{}, ErrNoTenant
	}
	if err := in.Validate(); err != nil {
		return ErasureResult{}, err
	}
	now := nowStamp()

	s.gmail.mu.Lock()
	s.calendar.mu.Lock()
	defer s.gmail.mu.Unlock()
	defer s.calendar.mu.Unlock()

	var result ErasureResult
	doomed := make(map[string]bool, 16)

	for key, record := range s.gmail.messages[scope.TenantID] {
		if record.ErasedAt.IsZero() && matchesGmailErasure(key, record, in) {
			doomed[record.ActivityID] = true
			redacted := cloneGmailMessage(record)
			redacted.MessageSubject = ""
			redacted.FromAddress = ""
			redacted.ThreadID = ""
			redacted.ErasedAt = now
			s.gmail.messages[scope.TenantID][key] = redacted
			result.MessagesRedacted++
		}
	}
	for key, record := range s.calendar.events[scope.TenantID] {
		if record.ErasedAt.IsZero() && matchesCalendarErasure(key, record, in) {
			doomed[record.ActivityID] = true
			redacted := cloneCalendarEvent(record)
			redacted.Title = ""
			redacted.OrganizerAddress = ""
			redacted.ICalUID = ""
			redacted.RecurringEventID = ""
			redacted.ErasedAt = now
			s.calendar.events[scope.TenantID][key] = redacted
			result.EventsRedacted++
		}
	}

	// Delete only the Activities those tombstones named, and only if sync
	// created them. An id that turns out to belong to a hand-logged activity
	// is left alone rather than trusted.
	s.activity.mu.Lock()
	for id := range doomed {
		record, ok := s.activity.rows[scope.TenantID][id]
		if !ok || !IsSyncedActivityKind(record.Kind) {
			continue
		}
		delete(s.activity.rows[scope.TenantID], id)
		result.ActivitiesDeleted++
	}
	s.activity.mu.Unlock()

	for _, address := range in.NormalizedSuppressions() {
		if s.suppressions[scope.TenantID] == nil {
			s.suppressions[scope.TenantID] = make(map[string]*SyncSuppression)
		}
		if _, exists := s.suppressions[scope.TenantID][address]; exists {
			continue
		}
		s.suppressions[scope.TenantID][address] = &SyncSuppression{
			Address: address, Reason: in.Reason, CreatedBy: in.Actor, CreatedAt: now,
		}
		result.AddressesSuppressed++
	}
	return result, nil
}

func matchesGmailErasure(key gmailEntryKey, record *GmailMessage, in EraseSubject) bool {
	if in.SubjectType != "" && (key.subjectType != in.SubjectType || key.subjectID != in.SubjectID) {
		return false
	}
	if in.OwnerID != "" && key.owner != in.OwnerID {
		return false
	}
	if !in.Before.IsZero() && !record.OccurredAt.Before(in.Before) {
		return false
	}
	return true
}

func matchesCalendarErasure(key calendarEntryKey, record *CalendarEvent, in EraseSubject) bool {
	if in.SubjectType != "" && (key.subjectType != in.SubjectType || key.subjectID != in.SubjectID) {
		return false
	}
	if in.OwnerID != "" && key.owner != in.OwnerID {
		return false
	}
	if !in.Before.IsZero() && !record.StartsAt.Before(in.Before) {
		return false
	}
	return true
}

// SuppressSyncAddress adds one address to the never-match list.
func (s *MemStore) SuppressSyncAddress(_ context.Context, scope Scope, v *SyncSuppression) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if v == nil {
		return ErrInvalid
	}
	record := cloneSyncSuppression(v)
	if err := record.Validate(); err != nil {
		return err
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	if s.suppressions[scope.TenantID] == nil {
		s.suppressions[scope.TenantID] = make(map[string]*SyncSuppression)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = nowStamp()
	}
	s.suppressions[scope.TenantID][record.Address] = record
	return nil
}

// ReleaseSyncAddress removes one, for an address suppressed in error.
func (s *MemStore) ReleaseSyncAddress(_ context.Context, scope Scope, address string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	address = NormalizeEmail(address)
	if _, ok := s.suppressions[scope.TenantID][address]; !ok {
		return ErrNotFound
	}
	delete(s.suppressions[scope.TenantID], address)
	return nil
}

// SuppressedSyncAddresses returns the tenant's whole list as a set.
func (s *MemStore) SuppressedSyncAddresses(
	_ context.Context, scope Scope,
) (map[string]bool, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	out := make(map[string]bool, len(s.suppressions[scope.TenantID]))
	for address := range s.suppressions[scope.TenantID] {
		out[address] = true
	}
	return out, nil
}

// ListSyncSuppressions returns the tenant's list, ordered by address.
func (s *MemStore) ListSyncSuppressions(
	_ context.Context, scope Scope,
) ([]*SyncSuppression, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.gmail.mu.Lock()
	defer s.gmail.mu.Unlock()

	out := make([]*SyncSuppression, 0, len(s.suppressions[scope.TenantID]))
	for _, record := range s.suppressions[scope.TenantID] {
		out = append(out, cloneSyncSuppression(record))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}
