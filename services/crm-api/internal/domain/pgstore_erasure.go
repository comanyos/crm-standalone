package domain

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of erasure and suppression
// (migrations/0022_sync_erasure.sql).

// EraseSyncedContent removes synced timeline entries, redacts their index
// rows, and suppresses addresses, in one transaction.
//
// One transaction because a partial erasure is worse than none. Redacting the
// index without deleting the Activities reports success while the content is
// still on the customer's timeline; deleting the Activities without redacting
// the index leaves the subject lines in a table nobody thinks to look in; and
// doing either without the suppression leaves collection running, so the next
// poll undoes the work.
func (s *PgStore) EraseSyncedContent(
	ctx context.Context, scope Scope, in EraseSubject,
) (ErasureResult, error) {
	if !scope.Valid() {
		return ErasureResult{}, ErrNoTenant
	}
	if err := in.Validate(); err != nil {
		return ErasureResult{}, err
	}

	var result ErasureResult
	now := nowStamp()

	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// The two index tables are redacted first, each returning the
		// Activity ids it owned. RETURNING is what makes this exact: only
		// entries this erasure actually redacted contribute ids, so a
		// concurrent erasure cannot make this one delete an Activity it did
		// not claim.
		messageActivities, redactedMessages, err := redactGmailIndex(ctx, tx, scope, in, now)
		if err != nil {
			return err
		}
		result.MessagesRedacted = redactedMessages

		eventActivities, redactedEvents, err := redactCalendarIndex(ctx, tx, scope, in, now)
		if err != nil {
			return err
		}
		result.EventsRedacted = redactedEvents

		doomed := make([]string, 0, len(messageActivities)+len(eventActivities))
		doomed = append(doomed, messageActivities...)
		doomed = append(doomed, eventActivities...)
		if len(doomed) > 0 {
			// The kind filter is not belt and braces: an id that turns out to
			// name a hand-logged activity must survive, because that is the
			// organization's own record rather than collected data.
			tag, err := tx.Exec(ctx,
				`DELETE FROM activities
				  WHERE tenant_id = $1 AND id = ANY($2) AND kind = ANY($3)`,
				scope.TenantID, doomed, SyncedActivityKinds())
			if err != nil {
				return fmt.Errorf("delete synced activities: %w", err)
			}
			result.ActivitiesDeleted = int(tag.RowsAffected())
		}

		for _, address := range in.NormalizedSuppressions() {
			tag, err := tx.Exec(ctx,
				`INSERT INTO sync_suppressions (tenant_id, address, reason, created_by, created_at)
				 VALUES ($1,$2,$3,$4,$5)
				 ON CONFLICT (tenant_id, address) DO NOTHING`,
				scope.TenantID, address, in.Reason, in.Actor, now)
			if err != nil {
				return fmt.Errorf("suppress address: %w", err)
			}
			result.AddressesSuppressed += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return ErasureResult{}, err
	}
	return result, nil
}

// redactGmailIndex turns matching message rows into tombstones and returns
// the Activity ids they named.
func redactGmailIndex(
	ctx context.Context, tx pgx.Tx, scope Scope, in EraseSubject, now time.Time,
) ([]string, int, error) {
	rows, err := tx.Query(ctx,
		`UPDATE gmail_messages
		    SET message_subject = '', from_address = '', thread_id = '', erased_at = $1
		  WHERE tenant_id = $2
		    AND erased_at IS NULL
		    AND ($3::text IS NULL OR (subject_type = $3 AND subject_id = $4))
		    AND ($5::text IS NULL OR owner_id = $5)
		    AND ($6::timestamptz IS NULL OR occurred_at < $6)
		RETURNING activity_id`,
		now, scope.TenantID,
		nullString(in.SubjectType), in.SubjectID,
		nullString(in.OwnerID), nullTimeValue(in.Before))
	if err != nil {
		return nil, 0, fmt.Errorf("redact gmail index: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, fmt.Errorf("scan redacted message: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return ids, len(ids), nil
}

// redactCalendarIndex is redactGmailIndex's twin.
func redactCalendarIndex(
	ctx context.Context, tx pgx.Tx, scope Scope, in EraseSubject, now time.Time,
) ([]string, int, error) {
	rows, err := tx.Query(ctx,
		`UPDATE calendar_events
		    SET title = '', organizer_address = '', ical_uid = '',
		        recurring_event_id = '', erased_at = $1
		  WHERE tenant_id = $2
		    AND erased_at IS NULL
		    AND ($3::text IS NULL OR (subject_type = $3 AND subject_id = $4))
		    AND ($5::text IS NULL OR owner_id = $5)
		    AND ($6::timestamptz IS NULL OR starts_at < $6)
		RETURNING activity_id`,
		now, scope.TenantID,
		nullString(in.SubjectType), in.SubjectID,
		nullString(in.OwnerID), nullTimeValue(in.Before))
	if err != nil {
		return nil, 0, fmt.Errorf("redact calendar index: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, fmt.Errorf("scan redacted event: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return ids, len(ids), nil
}

// nullString sends NULL for an empty filter, so the query's "this predicate
// does not apply" case is expressed once in SQL rather than by building the
// statement in Go.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// SuppressSyncAddress adds one address to the never-match list.
func (s *PgStore) SuppressSyncAddress(ctx context.Context, scope Scope, v *SyncSuppression) error {
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
	if record.CreatedAt.IsZero() {
		record.CreatedAt = nowStamp()
	}
	if _, err := s.db.Exec(ctx, scope,
		`INSERT INTO sync_suppressions (tenant_id, address, reason, created_by, created_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (tenant_id, address) DO UPDATE SET
		      reason = EXCLUDED.reason, created_by = EXCLUDED.created_by`,
		scope.TenantID, record.Address, record.Reason, record.CreatedBy, record.CreatedAt); err != nil {
		return fmt.Errorf("suppress sync address: %w", err)
	}
	return nil
}

// ReleaseSyncAddress removes one, for an address suppressed in error.
func (s *PgStore) ReleaseSyncAddress(ctx context.Context, scope Scope, address string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM sync_suppressions WHERE tenant_id = $1 AND address = $2`,
		scope.TenantID, NormalizeEmail(address))
	if err != nil {
		return fmt.Errorf("release sync address: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SuppressedSyncAddresses returns the tenant's whole list as a set.
func (s *PgStore) SuppressedSyncAddresses(
	ctx context.Context, scope Scope,
) (map[string]bool, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT address FROM sync_suppressions WHERE tenant_id = $1`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("read sync suppressions: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool, 8)
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, fmt.Errorf("scan suppression: %w", err)
		}
		out[address] = true
	}
	return out, rows.Err()
}

// ListSyncSuppressions returns the tenant's list, ordered by address.
func (s *PgStore) ListSyncSuppressions(
	ctx context.Context, scope Scope,
) ([]*SyncSuppression, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT address, reason, created_by, created_at FROM sync_suppressions
		  WHERE tenant_id = $1 ORDER BY address`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list sync suppressions: %w", err)
	}
	defer rows.Close()

	out := make([]*SyncSuppression, 0, 8)
	for rows.Next() {
		var v SyncSuppression
		if err := rows.Scan(&v.Address, &v.Reason, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan suppression: %w", err)
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}
