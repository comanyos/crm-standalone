// Package eventstore provides the append-only audit and replay log.
// Relational tables are the system of record for reads; events are the audit trail.
// Both are written in the same Postgres transaction — no dual-write risk.
package eventstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AggregateType identifies the domain object that owns an event.
type AggregateType string

const (
	AggregateIdentity    AggregateType = "identity"
	AggregateSession     AggregateType = "session"
	AggregateOrg         AggregateType = "org"
	AggregateProject     AggregateType = "project"
	AggregateApplication AggregateType = "application"
	AggregateFlow        AggregateType = "flow"
)

// Event is an immutable fact appended to the store.
type Event struct {
	ID            uuid.UUID
	AggregateType AggregateType
	AggregateID   uuid.UUID
	OrgID         uuid.UUID
	EventType     string
	Payload       json.RawMessage
	EditorActor   string // identity_id or system
	Sequence      uint64 // monotonically increasing per aggregate
	Position      uint64 // global position across all aggregates
	CreatedAt     time.Time
}

// Command is the intent to mutate state. Handlers produce Events.
type Command interface {
	AggregateType() AggregateType
	AggregateID() uuid.UUID
	OrgID() uuid.UUID
}

// Pusher appends events; implemented by postgres/Pusher.
type Pusher interface {
	Push(ctx context.Context, events ...*Event) error
}

// Querier reads events for replay or audit.
type Querier interface {
	// ByAggregate returns all events for a specific aggregate in sequence order.
	ByAggregate(ctx context.Context, aggType AggregateType, aggID uuid.UUID) ([]*Event, error)
	// Search returns events matching the filter (for projections / audit queries).
	Search(ctx context.Context, filter Filter) ([]*Event, error)
}

// Filter restricts event queries.
type Filter struct {
	OrgID         *uuid.UUID
	AggregateType *AggregateType
	AggregateID   *uuid.UUID
	EventTypes    []string
	FromPosition  uint64
	Limit         int
}

// Eventstore composes both interfaces; the postgres implementation satisfies both.
type Eventstore interface {
	Pusher
	Querier
}

// NewEvent is a convenience constructor for command handlers.
func NewEvent(aggType AggregateType, aggID, orgID uuid.UUID, eventType string, payload any, actor string) (*Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Event{
		ID:            uuid.New(),
		AggregateType: aggType,
		AggregateID:   aggID,
		OrgID:         orgID,
		EventType:     eventType,
		Payload:       raw,
		EditorActor:   actor,
		CreatedAt:     time.Now().UTC(),
	}, nil
}
