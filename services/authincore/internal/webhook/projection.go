package webhook

import (
	"context"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
)

// Projection implements event.Handler, fanning out domain events to webhooks.
type Projection struct {
	dispatcher *Dispatcher
}

// NewProjection returns a new webhook event projection.
func NewProjection(dispatcher *Dispatcher) *Projection {
	return &Projection{dispatcher: dispatcher}
}

// Handle dispatches a domain event to all matching webhooks for the event's org.
func (p *Projection) Handle(ctx context.Context, ev *eventstore.Event) error {
	if ev.OrgID == uuid.Nil {
		return nil
	}
	return p.dispatcher.Dispatch(ctx, ev.OrgID, ev.EventType, map[string]any{
		"event_id":       ev.ID.String(),
		"aggregate_type": string(ev.AggregateType),
		"aggregate_id":   ev.AggregateID.String(),
		"event_type":     ev.EventType,
		"created_at":     ev.CreatedAt,
	})
}
