package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of Gmail synchronisation
// (migrations/0020_gmail_sync.sql).

const gmailSyncStateColumns = `owner_id, last_history_id, backfill_page_token, backfill_complete,
	syncing_until, last_synced_at, last_message_at, messages_indexed, activities_created,
	last_error, created_at, updated_at`

func scanGmailSyncState(row pgx.Row) (*GmailSyncState, error) {
	var (
		v         GmailSyncState
		syncing   *time.Time
		lastSync  *time.Time
		lastMsg   *time.Time
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(&v.OwnerID, &v.LastHistoryID, &v.BackfillPageToken, &v.BackfillComplete,
		&syncing, &lastSync, &lastMsg, &v.MessagesIndexed, &v.ActivitiesCreated,
		&v.LastError, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	v.SyncingUntil = timeOrZero(syncing)
	v.LastSyncedAt = timeOrZero(lastSync)
	v.LastMessageAt = timeOrZero(lastMsg)
	v.CreatedAt, v.UpdatedAt = createdAt, updatedAt
	return &v, nil
}

// GetGmailSyncState reads one mailbox's cursor.
func (s *PgStore) GetGmailSyncState(
	ctx context.Context, scope Scope, ownerID string,
) (*GmailSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanGmailSyncState(s.db.QueryRow(ctx, scope,
		`SELECT `+gmailSyncStateColumns+` FROM gmail_sync_state
		  WHERE tenant_id = $1 AND owner_id = $2`, scope.TenantID, ownerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get gmail sync state: %w", err)
	}
	return out, nil
}

// SaveGmailSyncState upserts one mailbox's cursor.
func (s *PgStore) SaveGmailSyncState(
	ctx context.Context, scope Scope, v *GmailSyncState,
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
	now := nowStamp()

	out, err := scanGmailSyncState(s.db.QueryRow(ctx, scope,
		`INSERT INTO gmail_sync_state
		     (tenant_id, owner_id, last_history_id, backfill_page_token, backfill_complete,
		      syncing_until, last_synced_at, last_message_at, messages_indexed,
		      activities_created, last_error, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		 ON CONFLICT (tenant_id, owner_id) DO UPDATE SET
		      last_history_id     = EXCLUDED.last_history_id,
		      backfill_page_token = EXCLUDED.backfill_page_token,
		      backfill_complete   = EXCLUDED.backfill_complete,
		      syncing_until       = EXCLUDED.syncing_until,
		      last_synced_at      = EXCLUDED.last_synced_at,
		      last_message_at     = EXCLUDED.last_message_at,
		      messages_indexed    = EXCLUDED.messages_indexed,
		      activities_created  = EXCLUDED.activities_created,
		      last_error          = EXCLUDED.last_error,
		      updated_at          = EXCLUDED.updated_at
		 RETURNING `+gmailSyncStateColumns,
		scope.TenantID, record.OwnerID, record.LastHistoryID, record.BackfillPageToken,
		record.BackfillComplete, nullTimeValue(record.SyncingUntil),
		nullTimeValue(record.LastSyncedAt), nullTimeValue(record.LastMessageAt),
		record.MessagesIndexed, record.ActivitiesCreated, record.LastError, now))
	if err != nil {
		return nil, fmt.Errorf("save gmail sync state: %w", err)
	}
	return out, nil
}

// ClaimGmailSync leases a mailbox for one pass.
//
// The claim is one statement whose WHERE clause does the testing, for the
// same reason ConsumeGoogleOAuthState is: reading the lease and then writing
// it would let two replicas both see it free. The INSERT half creates the row
// on first sight, so a newly connected mailbox does not need a separate
// registration step that could be forgotten.
func (s *PgStore) ClaimGmailSync(
	ctx context.Context, scope Scope, ownerID string, now time.Time, lease time.Duration,
) (*GmailSyncState, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	stamp := now.UTC().Truncate(storedTimePrecision)
	until := stamp.Add(lease)

	out, err := scanGmailSyncState(s.db.QueryRow(ctx, scope,
		`INSERT INTO gmail_sync_state (tenant_id, owner_id, syncing_until, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$4)
		 ON CONFLICT (tenant_id, owner_id) DO UPDATE SET
		      syncing_until = EXCLUDED.syncing_until,
		      updated_at    = EXCLUDED.updated_at
		 WHERE gmail_sync_state.syncing_until IS NULL
		    OR gmail_sync_state.syncing_until <= $4
		 RETURNING `+gmailSyncStateColumns,
		scope.TenantID, ownerID, until, stamp))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The DO UPDATE's WHERE excluded the row: someone else holds a
			// live lease on this mailbox.
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("claim gmail sync: %w", err)
	}
	return out, nil
}

// DueGmailMailboxes lists unclaimed mailboxes, least recently synced first.
//
// It runs on the drain pool for the same reason DueOutboxTenants does — there
// is no tenant to bind, so RLS would filter the answer away — and is held to
// the same discipline: it returns identifiers and nothing else. Migration
// 0020 grants crm_drain SELECT on this table only, never on gmail_messages,
// which holds correspondence metadata.
func (s *PgStore) DueGmailMailboxes(
	ctx context.Context, now time.Time, limit int,
) ([]GmailMailboxRef, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.drain.Query(ctx,
		`SELECT tenant_id, owner_id FROM gmail_sync_state
		  WHERE syncing_until IS NULL OR syncing_until <= $1
		  ORDER BY last_synced_at NULLS FIRST, tenant_id, owner_id
		  LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("due gmail mailboxes: %w", err)
	}
	defer rows.Close()

	out := make([]GmailMailboxRef, 0, 8)
	for rows.Next() {
		var ref GmailMailboxRef
		if err := rows.Scan(&ref.TenantID, &ref.OwnerID); err != nil {
			return nil, fmt.Errorf("scan gmail mailbox: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// RecordGmailActivity writes one timeline entry: the index row and the
// Activity, in one transaction.
//
// The index row goes first, with ON CONFLICT DO NOTHING, so the primary key
// is what decides whether this entry already exists. Two replicas racing on
// the same message therefore serialize on that key rather than both deciding
// the entry is new. The Activity is written in the same transaction, so
// neither half can survive without the other: an orphaned Activity would be
// re-created on the next pass, and an orphaned index row is a timeline entry
// that silently never appears.
func (s *PgStore) RecordGmailActivity(
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

	created := false
	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO gmail_messages
			     (tenant_id, owner_id, provider_message_id, subject_type, subject_id,
			      thread_id, activity_id, message_subject, from_address, direction,
			      occurred_at, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			 ON CONFLICT DO NOTHING`,
			scope.TenantID, msg.OwnerID, msg.ProviderMessageID, msg.SubjectType, msg.SubjectID,
			msg.ThreadID, msg.ActivityID, msg.MessageSubject, msg.FromAddress, msg.Direction,
			msg.OccurredAt, nowStamp())
		if err != nil {
			return fmt.Errorf("index gmail message: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Already on this timeline. The normal outcome of re-syncing.
			return nil
		}

		now := nowStamp()
		activity.ID = msg.ActivityID
		activity.CreatedAt, activity.UpdatedAt = now, now
		if _, err := tx.Exec(ctx,
			`INSERT INTO activities
			     (tenant_id, id, kind, subject_type, subject_id, notes, actor_id,
			      occurred_at, created_at, updated_at, custom_fields)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'{}'::jsonb)`,
			scope.TenantID, activity.ID, activity.Kind, activity.SubjectType, activity.SubjectID,
			activity.Notes, activity.ActorID, activity.OccurredAt, now, now); err != nil {
			return fmt.Errorf("create gmail activity: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// ListGmailMessagesForSubject returns one record's synced correspondence,
// newest first.
func (s *PgStore) ListGmailMessagesForSubject(
	ctx context.Context, scope Scope, subjectType, subjectID string, limit int,
) ([]*GmailMessage, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT owner_id, provider_message_id, subject_type, subject_id, thread_id,
		        activity_id, message_subject, from_address, direction, occurred_at,
		        created_at, erased_at
		   FROM gmail_messages
		  WHERE tenant_id = $1 AND subject_type = $2 AND subject_id = $3
		  ORDER BY occurred_at DESC, provider_message_id
		  LIMIT $4`, scope.TenantID, subjectType, subjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list gmail messages: %w", err)
	}
	defer rows.Close()

	out := make([]*GmailMessage, 0, 8)
	for rows.Next() {
		var (
			v      GmailMessage
			erased *time.Time
		)
		if err := rows.Scan(&v.OwnerID, &v.ProviderMessageID, &v.SubjectType, &v.SubjectID,
			&v.ThreadID, &v.ActivityID, &v.MessageSubject, &v.FromAddress, &v.Direction,
			&v.OccurredAt, &v.CreatedAt, &erased); err != nil {
			return nil, fmt.Errorf("scan gmail message: %w", err)
		}
		v.ErasedAt = timeOrZero(erased)
		out = append(out, &v)
	}
	return out, rows.Err()
}

// PurgeGmailDataForOwner drops one mailbox's cursor, and the index rows too
// when asked. See the Store interface for why disconnecting does not ask.
func (s *PgStore) PurgeGmailDataForOwner(
	ctx context.Context, scope Scope, ownerID string, includeIndexedMessages bool,
) (int, error) {
	if !scope.Valid() {
		return 0, ErrNoTenant
	}
	removed := 0
	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if includeIndexedMessages {
			tag, err := tx.Exec(ctx,
				`DELETE FROM gmail_messages WHERE tenant_id = $1 AND owner_id = $2`,
				scope.TenantID, ownerID)
			if err != nil {
				return fmt.Errorf("purge gmail messages: %w", err)
			}
			removed = int(tag.RowsAffected())
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM gmail_sync_state WHERE tenant_id = $1 AND owner_id = $2`,
			scope.TenantID, ownerID); err != nil {
			return fmt.Errorf("purge gmail sync state: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
