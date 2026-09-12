package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of Calendar synchronisation
// (migrations/0021_calendar_sync.sql).

const calendarSyncStateColumns = `owner_id, sync_token, backfill_page_token, backfill_complete,
	syncing_until, last_synced_at, last_event_at, events_indexed, activities_created,
	last_error, created_at, updated_at`

func scanCalendarSyncState(row pgx.Row) (*CalendarSyncState, error) {
	var (
		v        CalendarSyncState
		syncing  *time.Time
		lastSync *time.Time
		lastEvt  *time.Time
	)
	if err := row.Scan(&v.OwnerID, &v.SyncToken, &v.BackfillPageToken, &v.BackfillComplete,
		&syncing, &lastSync, &lastEvt, &v.EventsIndexed, &v.ActivitiesCreated,
		&v.LastError, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.SyncingUntil = timeOrZero(syncing)
	v.LastSyncedAt = timeOrZero(lastSync)
	v.LastEventAt = timeOrZero(lastEvt)
	return &v, nil
}

// GetCalendarSyncState reads one calendar's cursor.
func (s *PgStore) GetCalendarSyncState(
	ctx context.Context, scope Scope, ownerID string,
) (*CalendarSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanCalendarSyncState(s.db.QueryRow(ctx, scope,
		`SELECT `+calendarSyncStateColumns+` FROM calendar_sync_state
		  WHERE tenant_id = $1 AND owner_id = $2`, scope.TenantID, ownerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get calendar sync state: %w", err)
	}
	return out, nil
}

// SaveCalendarSyncState upserts one calendar's cursor.
func (s *PgStore) SaveCalendarSyncState(
	ctx context.Context, scope Scope, v *CalendarSyncState,
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
	now := nowStamp()

	out, err := scanCalendarSyncState(s.db.QueryRow(ctx, scope,
		`INSERT INTO calendar_sync_state
		     (tenant_id, owner_id, sync_token, backfill_page_token, backfill_complete,
		      syncing_until, last_synced_at, last_event_at, events_indexed,
		      activities_created, last_error, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		 ON CONFLICT (tenant_id, owner_id) DO UPDATE SET
		      sync_token          = EXCLUDED.sync_token,
		      backfill_page_token = EXCLUDED.backfill_page_token,
		      backfill_complete   = EXCLUDED.backfill_complete,
		      syncing_until       = EXCLUDED.syncing_until,
		      last_synced_at      = EXCLUDED.last_synced_at,
		      last_event_at       = EXCLUDED.last_event_at,
		      events_indexed      = EXCLUDED.events_indexed,
		      activities_created  = EXCLUDED.activities_created,
		      last_error          = EXCLUDED.last_error,
		      updated_at          = EXCLUDED.updated_at
		 RETURNING `+calendarSyncStateColumns,
		scope.TenantID, record.OwnerID, record.SyncToken, record.BackfillPageToken,
		record.BackfillComplete, nullTimeValue(record.SyncingUntil),
		nullTimeValue(record.LastSyncedAt), nullTimeValue(record.LastEventAt),
		record.EventsIndexed, record.ActivitiesCreated, record.LastError, now))
	if err != nil {
		return nil, fmt.Errorf("save calendar sync state: %w", err)
	}
	return out, nil
}

// ClaimCalendarSync leases a calendar for one pass. The claim is one
// statement whose WHERE clause does the testing — see ClaimGmailSync.
func (s *PgStore) ClaimCalendarSync(
	ctx context.Context, scope Scope, ownerID string, now time.Time, lease time.Duration,
) (*CalendarSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	stamp := now.UTC().Truncate(storedTimePrecision)
	until := stamp.Add(lease)

	out, err := scanCalendarSyncState(s.db.QueryRow(ctx, scope,
		`INSERT INTO calendar_sync_state (tenant_id, owner_id, syncing_until, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$4)
		 ON CONFLICT (tenant_id, owner_id) DO UPDATE SET
		      syncing_until = EXCLUDED.syncing_until,
		      updated_at    = EXCLUDED.updated_at
		 WHERE calendar_sync_state.syncing_until IS NULL
		    OR calendar_sync_state.syncing_until <= $4
		 RETURNING `+calendarSyncStateColumns,
		scope.TenantID, ownerID, until, stamp))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("claim calendar sync: %w", err)
	}
	return out, nil
}

// DueCalendarMailboxes lists unclaimed calendars, least recently synced
// first. It runs on the drain pool for the same reason DueOutboxTenants and
// DueGmailMailboxes do, and returns identifiers only.
func (s *PgStore) DueCalendarMailboxes(
	ctx context.Context, now time.Time, limit int,
) ([]GmailMailboxRef, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.drain.Query(ctx,
		`SELECT tenant_id, owner_id FROM calendar_sync_state
		  WHERE syncing_until IS NULL OR syncing_until <= $1
		  ORDER BY last_synced_at NULLS FIRST, tenant_id, owner_id
		  LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("due calendar mailboxes: %w", err)
	}
	defer rows.Close()

	out := make([]GmailMailboxRef, 0, 8)
	for rows.Next() {
		var ref GmailMailboxRef
		if err := rows.Scan(&ref.TenantID, &ref.OwnerID); err != nil {
			return nil, fmt.Errorf("scan calendar mailbox: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

const calendarEventColumns = `owner_id, provider_event_id, provider_etag, created_by,
	subject_type, subject_id, calendar_id,
	ical_uid, recurring_event_id, activity_id, title, status, starts_at, ends_at,
	all_day, time_zone, organizer_address, attendee_count, state_key, created_at, updated_at,
	erased_at`

func scanCalendarEvent(row pgx.Row) (*CalendarEvent, error) {
	var (
		v      CalendarEvent
		endsAt *time.Time
		erased *time.Time
	)
	if err := row.Scan(&v.OwnerID, &v.ProviderEventID, &v.ProviderETag, &v.CreatedBy,
		&v.SubjectType, &v.SubjectID, &v.CalendarID,
		&v.ICalUID, &v.RecurringEventID, &v.ActivityID, &v.Title, &v.Status, &v.StartsAt, &endsAt,
		&v.AllDay, &v.TimeZone, &v.OrganizerAddress, &v.AttendeeCount, &v.StateKey,
		&v.CreatedAt, &v.UpdatedAt, &erased); err != nil {
		return nil, err
	}
	v.EndsAt = timeOrZero(endsAt)
	v.ErasedAt = timeOrZero(erased)
	return &v, nil
}

// RecordCalendarEvent mirrors one event and appends a timeline entry when the
// material state changed.
//
// The whole read-decide-write runs inside one transaction under an advisory
// lock keyed on the entry, and the lock is what makes it correct rather than
// merely atomic-looking.
//
// SELECT ... FOR UPDATE is not enough here, which is worth spelling out
// because it looks like it should be. A row lock can only lock a row that
// exists; on an event's first sighting there is none, so two concurrent
// passes both read nothing, both classify the event as newly scheduled, and
// both insert an Activity — the upsert on the mirror then resolves cleanly
// and hides it. The customer's timeline says the meeting was scheduled twice.
// That is a phantom, and the tool for a phantom is a lock on the key rather
// than on the row.
//
// pg_advisory_xact_lock releases at commit, so a failed pass cannot leave the
// event stuck. hashtext collisions are possible and harmless: the cost is two
// unrelated events briefly serializing.
func (s *PgStore) RecordCalendarEvent(
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

	// The lock key names one timeline entry: this tenant's copy of this event
	// on this record. The separator is cosmetic — hashtext collides freely
	// anyway, and a collision here costs only two unrelated events briefly
	// taking turns. (It must not be a NUL: PostgreSQL text cannot hold one.)
	lockKey := strings.Join([]string{
		scope.TenantID, event.OwnerID, event.ProviderEventID,
		event.SubjectType, event.SubjectID,
	}, "|")

	change := CalendarUnchanged
	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
			return fmt.Errorf("lock calendar event: %w", err)
		}
		var (
			previousKey        string
			previousActivityID string
			previousCreatedAt  time.Time
			previousErasedAt   *time.Time
			exists             bool
		)
		err := tx.QueryRow(ctx,
			`SELECT state_key, activity_id, created_at, erased_at FROM calendar_events
			  WHERE tenant_id = $1 AND owner_id = $2 AND provider_event_id = $3
			    AND subject_type = $4 AND subject_id = $5`,
			scope.TenantID, event.OwnerID, event.ProviderEventID,
			event.SubjectType, event.SubjectID).
			Scan(&previousKey, &previousActivityID, &previousCreatedAt, &previousErasedAt)
		switch {
		case err == nil:
			exists = true
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("read calendar event: %w", err)
		}
		if previousErasedAt != nil {
			// Erased. Leaving it entirely alone is the point: an event that
			// was erased and is later rescheduled must not post a fresh
			// timeline entry carrying the title that was just removed, and
			// the mirror must not be refreshed with it either. The
			// suppression list stops future collection; this stops
			// resurrection of this one.
			change = CalendarUnchanged
			return nil
		}

		change = ClassifyCalendarChange(previousKey, event)
		now := nowStamp()

		activityID := previousActivityID
		stateKey := previousKey
		if change != CalendarUnchanged {
			activity := activityFor(change)
			if activity == nil {
				return ErrInvalid
			}
			activity.CreatedAt, activity.UpdatedAt = now, now
			if _, err := tx.Exec(ctx,
				`INSERT INTO activities
				     (tenant_id, id, kind, subject_type, subject_id, notes, actor_id,
				      occurred_at, created_at, updated_at, custom_fields)
				 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'{}'::jsonb)`,
				scope.TenantID, activity.ID, activity.Kind, activity.SubjectType,
				activity.SubjectID, activity.Notes, activity.ActorID, activity.OccurredAt,
				now, now); err != nil {
				return fmt.Errorf("create calendar activity: %w", err)
			}
			if !exists {
				activityID = activity.ID
			}
			stateKey = event.MaterialStateKey()
		}

		createdAt := now
		if exists {
			createdAt = previousCreatedAt
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO calendar_events
			     (tenant_id, owner_id, provider_event_id, provider_etag, created_by,
			      subject_type, subject_id, calendar_id,
			      ical_uid, recurring_event_id, activity_id, title, status, starts_at, ends_at,
			      all_day, time_zone, organizer_address, attendee_count, state_key,
			      created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
			 ON CONFLICT (tenant_id, owner_id, provider_event_id, subject_type, subject_id)
			 DO UPDATE SET
			      provider_etag      = CASE WHEN EXCLUDED.provider_etag = '' THEN calendar_events.provider_etag ELSE EXCLUDED.provider_etag END,
			      created_by         = CASE WHEN EXCLUDED.created_by = '' THEN calendar_events.created_by ELSE EXCLUDED.created_by END,
			      calendar_id        = EXCLUDED.calendar_id,
			      ical_uid           = EXCLUDED.ical_uid,
			      recurring_event_id = EXCLUDED.recurring_event_id,
			      title              = EXCLUDED.title,
			      status             = EXCLUDED.status,
			      starts_at          = EXCLUDED.starts_at,
			      ends_at            = EXCLUDED.ends_at,
			      all_day            = EXCLUDED.all_day,
			      time_zone          = EXCLUDED.time_zone,
			      organizer_address  = EXCLUDED.organizer_address,
			      attendee_count     = EXCLUDED.attendee_count,
			      state_key          = EXCLUDED.state_key,
			      updated_at         = EXCLUDED.updated_at`,
			scope.TenantID, event.OwnerID, event.ProviderEventID, event.ProviderETag,
			event.CreatedBy, event.SubjectType, event.SubjectID, event.CalendarID,
			event.ICalUID, event.RecurringEventID,
			activityID, event.Title, event.Status, event.StartsAt, nullTimeValue(event.EndsAt),
			event.AllDay, event.TimeZone, event.OrganizerAddress, event.AttendeeCount,
			stateKey, createdAt, now); err != nil {
			return fmt.Errorf("mirror calendar event: %w", err)
		}
		return nil
	})
	if err != nil {
		return CalendarUnchanged, err
	}
	return change, nil
}

// GetManagedCalendarEvent returns one CRM-authored event belonging to the
// connected owner. Provider ids from another user's calendar and imported
// events intentionally look absent.
func (s *PgStore) GetManagedCalendarEvent(
	ctx context.Context, scope Scope, ownerID, providerEventID string,
) (*CalendarEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	row, err := scanCalendarEvent(s.db.QueryRow(ctx, scope,
		`SELECT `+calendarEventColumns+` FROM calendar_events
		  WHERE tenant_id = $1 AND owner_id = $2 AND provider_event_id = $3
		    AND created_by <> '' AND erased_at IS NULL
		  ORDER BY created_at LIMIT 1`, scope.TenantID, ownerID, providerEventID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get managed calendar event: %w", err)
	}
	return row, nil
}

// ListManagedCalendarEvents returns the caller's CRM-authored events in a
// bounded range. The owner predicate is authorization, not a UI filter.
func (s *PgStore) ListManagedCalendarEvents(
	ctx context.Context, scope Scope, ownerID string, from, to time.Time, limit int,
) ([]*CalendarEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	if from.IsZero() {
		from = time.Now().UTC().AddDate(-1, 0, 0)
	}
	if to.IsZero() {
		to = time.Now().UTC().AddDate(2, 0, 0)
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+calendarEventColumns+` FROM calendar_events
		  WHERE tenant_id = $1 AND owner_id = $2 AND created_by <> ''
		    AND erased_at IS NULL AND starts_at >= $3 AND starts_at < $4
		  ORDER BY starts_at, provider_event_id LIMIT $5`,
		scope.TenantID, ownerID, from.UTC(), to.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list managed calendar events: %w", err)
	}
	defer rows.Close()
	out := make([]*CalendarEvent, 0, 16)
	for rows.Next() {
		record, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan managed calendar event: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// ListCalendarEventsForSubject returns one record's meetings, newest first.
func (s *PgStore) ListCalendarEventsForSubject(
	ctx context.Context, scope Scope, subjectType, subjectID string, limit int,
) ([]*CalendarEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+calendarEventColumns+` FROM calendar_events
		  WHERE tenant_id = $1 AND subject_type = $2 AND subject_id = $3
		  ORDER BY starts_at DESC, provider_event_id
		  LIMIT $4`, scope.TenantID, subjectType, subjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list calendar events: %w", err)
	}
	defer rows.Close()

	out := make([]*CalendarEvent, 0, 8)
	for rows.Next() {
		record, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan calendar event: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// PurgeCalendarDataForOwner drops the cursor, and the mirror too when asked.
func (s *PgStore) PurgeCalendarDataForOwner(
	ctx context.Context, scope Scope, ownerID string, includeIndexedEvents bool,
) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}
	removed := 0
	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if includeIndexedEvents {
			tag, err := tx.Exec(ctx,
				`DELETE FROM calendar_events WHERE tenant_id = $1 AND owner_id = $2`,
				scope.TenantID, ownerID)
			if err != nil {
				return fmt.Errorf("purge calendar events: %w", err)
			}
			removed = int(tag.RowsAffected())
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM calendar_sync_state WHERE tenant_id = $1 AND owner_id = $2`,
			scope.TenantID, ownerID); err != nil {
			return fmt.Errorf("purge calendar sync state: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
