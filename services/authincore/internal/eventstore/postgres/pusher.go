package postgres

import (
	"context"
	"fmt"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pusher appends events to the events table within the caller's transaction.
// If the context carries a pgx Tx, that transaction is reused so that the
// relational write (SoR) and the event append are atomic.
type Pusher struct {
	pool *pgxpool.Pool
}

func NewPusher(pool *pgxpool.Pool) *Pusher {
	return &Pusher{pool: pool}
}

const insertEventSQL = `
INSERT INTO events (
    id, aggregate_type, aggregate_id, org_id, event_type,
    payload, editor_actor, sequence, created_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7,
    (SELECT COALESCE(MAX(sequence), 0) + 1 FROM events
     WHERE aggregate_type = $2 AND aggregate_id = $3),
    $8
)
RETURNING sequence, position`

func (p *Pusher) Push(ctx context.Context, events ...*eventstore.Event) error {
	if tx, ok := database.TxFromContext(ctx); ok {
		for _, ev := range events {
			row := tx.QueryRow(ctx, insertEventSQL, ev.ID, ev.AggregateType, ev.AggregateID, ev.OrgID, ev.EventType, ev.Payload, ev.EditorActor, ev.CreatedAt)
			if err := row.Scan(&ev.Sequence, &ev.Position); err != nil {
				return fmt.Errorf("eventstore insert event %s: %w", ev.EventType, err)
			}
		}
		return nil
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("eventstore acquire conn: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("eventstore begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, ev := range events {
		row := tx.QueryRow(ctx, insertEventSQL,
			ev.ID, ev.AggregateType, ev.AggregateID, ev.OrgID, ev.EventType,
			ev.Payload, ev.EditorActor, ev.CreatedAt,
		)
		if err := row.Scan(&ev.Sequence, &ev.Position); err != nil {
			return fmt.Errorf("eventstore insert event %s: %w", ev.EventType, err)
		}
	}
	return tx.Commit(ctx)
}
