package domain

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/santoshguru/crm/internal/tracing"
)

// The transactional outbox (Track A3). Mirrors leave's event_outbox.go /
// hcm's outbox.go: delivery is at-least-once, and effectively-once
// application is the consumer's job (a keyed, version-guarded upsert), not
// the transport's.
//
// One state transition publishes today: opportunity_events.go enqueues
// opportunity.closed_won the instant a deal is won. Every other transition
// still writes nothing, so the outbox is a live path with narrow coverage,
// not an unused seam — extending it is task_v2 6.5.

// Outbox event states.
//
//	pending   — written, not yet delivered
//	sent      — a delivery attempt is in flight
//	published — the bus accepted it; terminal
//	failed    — retries exhausted; needs a human
const (
	EventPending   = "pending"
	EventSent      = "sent"
	EventPublished = "published"
	EventFailed    = "failed"
)

var eventStatuses = map[string]bool{
	EventPending:   true,
	EventSent:      true,
	EventPublished: true,
	EventFailed:    true,
}

// MaxEventAttempts bounds retries. Past it the event goes to `failed` and
// stops, because an event retrying for ever is an outage nobody is paged for.
const MaxEventAttempts = 8

// OutboxEvent is one durable event awaiting delivery to the bus.
type OutboxEvent struct {
	ID       string   `json:"id"`
	Envelope Envelope `json:"envelope"`

	Status   string `json:"status"`
	Attempts int    `json:"attempts"`

	// NextAttemptAt gates redelivery. Zero means "eligible now".
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EntityID returns the record's identifier.
func (e *OutboxEvent) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *OutboxEvent) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *OutboxEvent) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Created returns the record's creation time.
func (e *OutboxEvent) Created() time.Time { return e.CreatedAt }

// Validate rejects an outbox event that cannot be stored.
func (e *OutboxEvent) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no outbox event", ErrInvalid)
	}
	if e.Status == "" {
		e.Status = EventPending
	}
	if !eventStatuses[e.Status] {
		return fmt.Errorf("%w: outbox status %q is not a known state", ErrInvalid, e.Status)
	}
	return e.Envelope.Validate()
}

// NewOutboxEvent wraps an envelope for durable delivery.
func NewOutboxEvent(env Envelope) (*OutboxEvent, error) {
	ev := &OutboxEvent{
		ID:       env.ID,
		Envelope: env,
		Status:   EventPending,
	}
	if err := ev.Validate(); err != nil {
		return nil, err
	}
	return ev, nil
}

// Terminal reports whether the event will never change again.
func (e *OutboxEvent) Terminal() bool {
	return e != nil && (e.Status == EventPublished || e.Status == EventFailed)
}

// Deliverable reports whether the event may be sent at the given moment.
func (e *OutboxEvent) Deliverable(now time.Time) bool {
	if e == nil || e.Terminal() {
		return false
	}
	if e.Status != EventPending && e.Status != EventSent {
		return false
	}
	return e.NextAttemptAt.IsZero() || !now.Before(e.NextAttemptAt)
}

// EventBackoff returns the delay before attempt n, capped. Exponential from one
// second: 1s, 2s, 4s … up to five minutes.
func EventBackoff(attempts int) time.Duration {
	const ceiling = 5 * time.Minute
	if attempts < 1 {
		attempts = 1
	}
	d := time.Second
	for i := 1; i < attempts && d < ceiling; i++ {
		d *= 2
	}
	if d > ceiling {
		d = ceiling
	}
	return d
}

// MarkSent records that a delivery attempt is being made now. Attempts is
// incremented HERE and nowhere else.
func (e *OutboxEvent) MarkSent(now time.Time) error {
	if e.Terminal() {
		return fmt.Errorf("%w: event %s is %s", ErrConflict, e.ID, e.Status)
	}
	e.Attempts++
	e.Status = EventSent
	e.NextAttemptAt = now.Add(EventBackoff(e.Attempts))
	return nil
}

// MarkPublished records that the bus accepted the event. Idempotent.
func (e *OutboxEvent) MarkPublished() error {
	if e.Status == EventPublished {
		return nil
	}
	e.Status = EventPublished
	e.LastError = ""
	e.NextAttemptAt = time.Time{}
	return nil
}

// MarkRetry records a transient failure. Past MaxEventAttempts the event fails
// for a human rather than retrying for ever.
func (e *OutboxEvent) MarkRetry(now time.Time, cause string) error {
	if e.Terminal() {
		return fmt.Errorf("%w: event %s is %s", ErrConflict, e.ID, e.Status)
	}
	e.LastError = cause
	if e.Attempts >= MaxEventAttempts {
		e.Status = EventFailed
		e.NextAttemptAt = time.Time{}
		return nil
	}
	e.Status = EventPending
	e.NextAttemptAt = now.Add(EventBackoff(e.Attempts))
	return nil
}

// DueEvents returns the events eligible for delivery now, oldest first.
func DueEvents(events []*OutboxEvent, now time.Time) []*OutboxEvent {
	out := make([]*OutboxEvent, 0, len(events))
	for _, e := range events {
		if e.Deliverable(now) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ---------------------------------------------------------------------------
// Enqueueing and draining
// ---------------------------------------------------------------------------

// EventPublisher delivers an envelope to the bus.
type EventPublisher interface {
	Publish(ctx context.Context, env Envelope) error
}

// EventOutboxStore is what EnqueueEvent and DrainEventOutbox need from a Store.
type EventOutboxStore interface {
	eventEnqueuer
	DueOutboxEvents(ctx context.Context, scope Scope, now time.Time) ([]*OutboxEvent, error)
	UpdateOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error
}

// eventEnqueuer is the write half on its own. EnqueueEvent takes this rather
// than the whole EventOutboxStore so a caller that can only write — notably
// PgStore's transaction-scoped writer, which enqueues in the same
// transaction as the business row that justified the event — can still use
// it. The method is unexported, so only this package can implement it.
type eventEnqueuer interface {
	enqueueOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error
}

// EnqueueEvent wraps env in a durable OutboxEvent and writes it. This is the
// seam Track B calls once a state transition decides it must publish; A3
// does not call it from anywhere itself.
func EnqueueEvent(ctx context.Context, store eventEnqueuer, scope Scope, env Envelope) (*OutboxEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if env.TenantID == "" {
		env.TenantID = scope.TenantID
	}
	if env.TraceParent == "" {
		env.TraceParent = tracing.TraceParent(ctx)
	}
	ev, err := NewOutboxEvent(env)
	if err != nil {
		return nil, err
	}
	if err := store.enqueueOutboxEvent(ctx, scope, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// DrainEventOutbox delivers every due event for a scope, reports how many it
// published, and returns the last publish failure it saw.
func DrainEventOutbox(
	ctx context.Context,
	store EventOutboxStore,
	scope Scope,
	publisher EventPublisher,
	now time.Time,
) (published int, err error) {
	stats, err := DrainEventOutboxWithStats(ctx, store, scope, publisher, now)
	return stats.Published, err
}

// OutboxDrainStats records delivery outcomes without carrying an event,
// tenant, payload, or error value into the metrics layer.
type OutboxDrainStats struct{ Published, Retried, Failed int }

// DrainEventOutboxWithStats is DrainEventOutbox with safe aggregate outcomes
// for the worker's Prometheus counters.
func DrainEventOutboxWithStats(
	ctx context.Context,
	store EventOutboxStore,
	scope Scope,
	publisher EventPublisher,
	now time.Time,
) (stats OutboxDrainStats, err error) {
	due, err := store.DueOutboxEvents(ctx, scope, now)
	if err != nil {
		return stats, err
	}

	var lastPubErr error
	for _, ev := range due {
		if err := ev.MarkSent(now); err != nil {
			continue
		}
		if err := store.UpdateOutboxEvent(ctx, scope, ev); err != nil {
			return stats, err
		}

		pubErr := publisher.Publish(ctx, ev.Envelope)
		if pubErr != nil {
			lastPubErr = pubErr
		}
		switch {
		case pubErr == nil:
			_ = ev.MarkPublished()
			stats.Published++
		case errors.Is(pubErr, ErrInvalid):
			ev.Attempts = MaxEventAttempts
			_ = ev.MarkRetry(now, pubErr.Error())
		default:
			_ = ev.MarkRetry(now, pubErr.Error())
		}
		if pubErr != nil {
			if ev.Status == EventFailed {
				stats.Failed++
			} else {
				stats.Retried++
			}
		}

		if err := store.UpdateOutboxEvent(ctx, scope, ev); err != nil {
			return stats, err
		}
	}
	return stats, lastPubErr
}

func cloneOutboxEvent(v *OutboxEvent) *OutboxEvent {
	if v == nil {
		return nil
	}
	c := *v
	if v.Envelope.Payload != nil {
		c.Envelope.Payload = append([]byte(nil), v.Envelope.Payload...)
	}
	return &c
}
