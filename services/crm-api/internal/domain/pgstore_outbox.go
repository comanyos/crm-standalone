package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// PgStore's half of the event outbox (Track A3). Mirrors leave's and hcm's
// pgstore_outbox.go.
//
// Known limitation, disclosed rather than hidden: enqueueOutboxEvent below
// runs its own transaction via scopedDB.Exec, not the same transaction as
// whatever business write asked for it. Track B, when it starts calling
// EnqueueEvent from a write path, should give that write its own
// begin/commit spanning both statements if it wants the "wrote the row and
// never enqueued the event" window closed — the same gap leave's
// pgstore_outbox.go discloses.

// enqueueOutboxEvent writes one event to the outbox.
func (s *PgStore) enqueueOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error {
	envelope, err := json.Marshal(ev.Envelope)
	if err != nil {
		return fmt.Errorf("%w: marshal envelope: %v", ErrInvalid, err)
	}
	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO event_outbox (
		     tenant_id, id,
		     event_type, entity_type, entity_id, source_version,
		     correlation_id, causation_id, actor, occurred_at,
		     envelope, status, created_at, updated_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now(), now())
		 ON CONFLICT (tenant_id, entity_type, entity_id, source_version) DO NOTHING`,
		scope.TenantID, ev.ID,
		ev.Envelope.Type, ev.Envelope.EntityType, ev.Envelope.EntityID, ev.Envelope.SourceVersion,
		ev.Envelope.CorrelationID, ev.Envelope.CausationID, ev.Envelope.Actor, ev.Envelope.OccurredAt,
		envelope, ev.Status)
	if err != nil {
		return fmt.Errorf("enqueue event: %w", err)
	}
	return nil
}

const outboxCols = `id, envelope, status, attempts, next_attempt_at, last_error, created_at, updated_at`

// qualifiedOutboxCols is the same list bound to the update target — the claim
// in DueOutboxEvents joins a CTE that also has an `id`, so the bare list is
// ambiguous there.
const qualifiedOutboxCols = `e.id, e.envelope, e.status, e.attempts, e.next_attempt_at, e.last_error, e.created_at, e.updated_at`

func scanOutboxEvent(row pgx.Row) (*OutboxEvent, error) {
	v := &OutboxEvent{}
	var envelope []byte
	var nextAttempt *time.Time
	err := row.Scan(&v.ID, &envelope, &v.Status, &v.Attempts,
		&nextAttempt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if nextAttempt != nil {
		v.NextAttemptAt = *nextAttempt
	}
	if err := json.Unmarshal(envelope, &v.Envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope of event %s: %w", v.ID, err)
	}
	return v, nil
}

func (s *PgStore) queryOutboxEvents(ctx context.Context, scope Scope, sql string, args ...any) ([]*OutboxEvent, error) {
	rows, err := s.db.Query(ctx, scope, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*OutboxEvent, 0, 8)
	for rows.Next() {
		v, err := scanOutboxEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DueOutboxTenants lists the tenants with events waiting. It runs on s.drain
// rather than s.db — see the interface comment in store.go and NewPgStoreWithDrain.
func (s *PgStore) DueOutboxTenants(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.drain.Query(ctx,
		`SELECT DISTINCT tenant_id FROM event_outbox
		 WHERE status IN ('pending', 'sent')
		   AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
		 ORDER BY tenant_id`,
		now)
	if err != nil {
		return nil, fmt.Errorf("due outbox tenants: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0, 8)
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// outboxClaimBatch bounds one drain pass.
const outboxClaimBatch = 100

// outboxLease is how long a claimed event stays invisible to other replicas.
const outboxLease = 30 * time.Second

// DueOutboxEvents claims the events eligible for delivery now, oldest first.
// FOR UPDATE SKIP LOCKED makes two replicas draining the same tenant take
// disjoint rows instead of racing for the same ones.
func (s *PgStore) DueOutboxEvents(ctx context.Context, scope Scope, now time.Time) ([]*OutboxEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := s.queryOutboxEvents(ctx, scope,
		`WITH claimed AS (
		     SELECT id FROM event_outbox
		      WHERE tenant_id = $1
		        AND status IN ('pending', 'sent')
		        AND (next_attempt_at IS NULL OR next_attempt_at <= $2)
		      ORDER BY created_at, id
		      LIMIT $4
		      FOR UPDATE SKIP LOCKED
		 )
		 UPDATE event_outbox e
		    SET next_attempt_at = $3
		   FROM claimed c
		  WHERE e.tenant_id = $1 AND e.id = c.id
		 RETURNING `+qualifiedOutboxCols,
		scope.TenantID, now, now.Add(outboxLease), outboxClaimBatch)
	if err != nil {
		return nil, fmt.Errorf("due outbox events: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// UpdateOutboxEvent records a delivery outcome.
func (s *PgStore) UpdateOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if err := ev.Validate(); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, scope,
		`UPDATE event_outbox
		 SET status = $3, attempts = $4, next_attempt_at = $5, last_error = $6, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, ev.ID, ev.Status, ev.Attempts,
		nullTime(ev.NextAttemptAt), ev.LastError)
	if err != nil {
		return fmt.Errorf("update outbox event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OutboxEventsFor returns every event ever enqueued about one entity id,
// oldest first.
func (s *PgStore) OutboxEventsFor(ctx context.Context, scope Scope, entityID string) ([]*OutboxEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := s.queryOutboxEvents(ctx, scope,
		`SELECT `+outboxCols+` FROM event_outbox
		 WHERE tenant_id = $1 AND entity_id = $2
		 ORDER BY source_version, id`,
		scope.TenantID, entityID)
	if err != nil {
		return nil, fmt.Errorf("outbox events for %s: %w", entityID, err)
	}
	return out, nil
}

// nullTime sends NULL for the zero time, so "eligible now" is NULL in the
// database rather than year one.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
