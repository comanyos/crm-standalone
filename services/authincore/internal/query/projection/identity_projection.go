// Package projection contains async event handlers that keep read models up to date.
// They consume the eventstore and update relational tables.
package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityProjection keeps the identities read table in sync with events.
type IdentityProjection struct {
	db  *pgxpool.Pool
	log *slog.Logger
}

func NewIdentityProjection(db *pgxpool.Pool, log *slog.Logger) *IdentityProjection {
	return &IdentityProjection{db: db, log: log}
}

// Handle processes a single event from the eventstore.
func (p *IdentityProjection) Handle(ctx context.Context, ev *eventstore.Event) error {
	switch ev.EventType {
	case "identity.created":
		return p.handleCreated(ctx, ev)
	case "identity.updated":
		return p.handleUpdated(ctx, ev)
	case "identity.deactivated", "identity.reactivated":
		return p.handleStateChange(ctx, ev)
	case "identity.deleted":
		return p.handleDeleted(ctx, ev)
	default:
		return nil // not our event
	}
}

func (p *IdentityProjection) handleCreated(ctx context.Context, ev *eventstore.Event) error {
	var payload struct {
		SchemaID string          `json:"schema_id"`
		Traits   json.RawMessage `json:"traits"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return fmt.Errorf("identity projection created unmarshal: %w", err)
	}
	_, err := p.db.Exec(ctx,
		`INSERT INTO identities (id, org_id, schema_id, traits, state, available_aal, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, 1, 0, $5, $5)
		 ON CONFLICT (id) DO NOTHING`,
		ev.AggregateID, ev.OrgID, payload.SchemaID, payload.Traits, ev.CreatedAt,
	)
	return err
}

func (p *IdentityProjection) handleUpdated(ctx context.Context, ev *eventstore.Event) error {
	var payload struct {
		Traits json.RawMessage `json:"traits"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return nil // best-effort
	}
	_, err := p.db.Exec(ctx,
		`UPDATE identities SET traits = $1, updated_at = $2 WHERE id = $3`,
		payload.Traits, ev.CreatedAt, ev.AggregateID,
	)
	return err
}

func (p *IdentityProjection) handleStateChange(ctx context.Context, ev *eventstore.Event) error {
	var payload struct {
		State int `json:"state"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return nil
	}
	_, err := p.db.Exec(ctx,
		`UPDATE identities SET state = $1, updated_at = $2 WHERE id = $3`,
		payload.State, ev.CreatedAt, ev.AggregateID,
	)
	return err
}

func (p *IdentityProjection) handleDeleted(ctx context.Context, ev *eventstore.Event) error {
	_, err := p.db.Exec(ctx, `DELETE FROM identities WHERE id = $1`, ev.AggregateID)
	return err
}

// --- Projection runner ---

type Runner struct {
	es          eventstore.Querier
	projections []handler
	log         *slog.Logger
}

type handler interface {
	Handle(ctx context.Context, ev *eventstore.Event) error
}

func NewRunner(es eventstore.Querier, log *slog.Logger, projections ...handler) *Runner {
	return &Runner{es: es, projections: projections, log: log}
}

// Catchup replays events from position to bring projections up to date.
func (r *Runner) Catchup(ctx context.Context, fromPosition uint64) error {
	filter := eventstore.Filter{FromPosition: fromPosition, Limit: 1000}
	events, err := r.es.Search(ctx, filter)
	if err != nil {
		return err
	}
	for _, ev := range events {
		for _, p := range r.projections {
			if err := p.Handle(ctx, ev); err != nil {
				r.log.Error("projection handle error", "event_type", ev.EventType, "event_id", ev.ID, "err", err)
			}
		}
	}
	r.log.Info("projection catchup complete", "events_processed", len(events))
	return nil
}

// lastPosition returns the highest position already processed (stored in projection_state table).
func (r *Runner) LastPosition(ctx context.Context, db *pgxpool.Pool, name string) (uint64, error) {
	var pos uint64
	err := db.QueryRow(ctx, `SELECT COALESCE(last_position, 0) FROM projection_state WHERE name = $1`, name).Scan(&pos)
	if err != nil {
		return 0, nil // table or row may not exist yet
	}
	return pos, nil
}

func UUIDFromString(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}
