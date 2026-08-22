package postgres

import (
	"context"
	"fmt"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Querier struct {
	pool *pgxpool.Pool
}

func NewQuerier(pool *pgxpool.Pool) *Querier {
	return &Querier{pool: pool}
}

func (q *Querier) ByAggregate(ctx context.Context, aggType eventstore.AggregateType, aggID uuid.UUID) ([]*eventstore.Event, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT id, aggregate_type, aggregate_id, org_id, event_type,
		        payload, editor_actor, sequence, position, created_at
		 FROM events
		 WHERE aggregate_type = $1 AND aggregate_id = $2
		 ORDER BY sequence ASC`,
		aggType, aggID,
	)
	if err != nil {
		return nil, fmt.Errorf("eventstore by_aggregate query: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (q *Querier) Search(ctx context.Context, f eventstore.Filter) ([]*eventstore.Event, error) {
	query := `SELECT id, aggregate_type, aggregate_id, org_id, event_type,
	                 payload, editor_actor, sequence, position, created_at
	          FROM events WHERE 1=1`
	args := []any{}
	i := 1

	if f.OrgID != nil {
		query += fmt.Sprintf(" AND org_id = $%d", i)
		args = append(args, *f.OrgID)
		i++
	}
	if f.AggregateType != nil {
		query += fmt.Sprintf(" AND aggregate_type = $%d", i)
		args = append(args, *f.AggregateType)
		i++
	}
	if f.AggregateID != nil {
		query += fmt.Sprintf(" AND aggregate_id = $%d", i)
		args = append(args, *f.AggregateID)
		i++
	}
	if f.FromPosition > 0 {
		query += fmt.Sprintf(" AND position > $%d", i)
		args = append(args, f.FromPosition)
		i++
	}
	query += " ORDER BY position ASC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", i)
		args = append(args, f.Limit)
	}

	rows, err := q.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("eventstore search: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

func scanEvents(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]*eventstore.Event, error) {
	var out []*eventstore.Event
	for rows.Next() {
		ev := &eventstore.Event{}
		if err := rows.Scan(
			&ev.ID, &ev.AggregateType, &ev.AggregateID, &ev.OrgID, &ev.EventType,
			&ev.Payload, &ev.EditorActor, &ev.Sequence, &ev.Position, &ev.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
