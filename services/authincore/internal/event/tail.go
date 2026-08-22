// Package event provides an event tail worker that polls new events and
// delivers them to registered projection handlers.
package event

import (
	"context"
	"log/slog"
	"time"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler processes a single event from the tail.
type Handler interface {
	Handle(ctx context.Context, ev *eventstore.Event) error
}

// Tail polls the events table for new rows and fans out to handlers.
// It persists its checkpoint in projection_state so restarts are safe.
type Tail struct {
	es       eventstore.Querier
	db       *pgxpool.Pool
	handlers []Handler
	name     string
	interval time.Duration
	log      *slog.Logger
}

func NewTail(
	es eventstore.Querier,
	db *pgxpool.Pool,
	name string,
	interval time.Duration,
	log *slog.Logger,
	handlers ...Handler,
) *Tail {
	if interval == 0 {
		interval = 500 * time.Millisecond
	}
	return &Tail{es: es, db: db, name: name, interval: interval, log: log, handlers: handlers}
}

// Run blocks until ctx is cancelled, polling for new events each interval.
func (t *Tail) Run(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	pos, err := t.loadPosition(ctx)
	if err != nil {
		t.log.Warn("event tail: failed to load checkpoint, starting from 0", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pos = t.poll(ctx, pos)
		}
	}
}

func (t *Tail) poll(ctx context.Context, fromPos uint64) uint64 {
	events, err := t.es.Search(ctx, eventstore.Filter{FromPosition: fromPos, Limit: 500})
	if err != nil {
		t.log.Warn("event tail: search error", "err", err)
		return fromPos
	}
	newPos := fromPos
	for _, ev := range events {
		handlerErr := false
		for _, h := range t.handlers {
			if err := h.Handle(ctx, ev); err != nil {
				t.log.Error("event tail: handler error",
					"handler", t.name, "event_type", ev.EventType, "event_id", ev.ID, "err", err)
				handlerErr = true
			}
		}
		// Stop at the first failure — checkpoint is not advanced past failing
		// events so they are re-delivered on the next poll cycle.
		if handlerErr {
			break
		}
		if ev.Position > newPos {
			newPos = ev.Position
		}
	}
	// Only write to DB when the checkpoint actually moved forward.
	if newPos > fromPos {
		if err := t.savePosition(ctx, newPos); err != nil {
			t.log.Warn("event tail: save checkpoint failed", "err", err)
		}
	}
	return newPos
}

func (t *Tail) loadPosition(ctx context.Context) (uint64, error) {
	var pos uint64
	err := t.db.QueryRow(ctx,
		`SELECT last_position FROM projection_state WHERE name = $1`, t.name,
	).Scan(&pos)
	if err != nil {
		return 0, nil // row not yet created
	}
	return pos, nil
}

func (t *Tail) savePosition(ctx context.Context, pos uint64) error {
	_, err := t.db.Exec(ctx,
		`INSERT INTO projection_state (name, last_position)
		 VALUES ($1, $2)
		 ON CONFLICT (name) DO UPDATE SET last_position = EXCLUDED.last_position`,
		t.name, pos,
	)
	return err
}
