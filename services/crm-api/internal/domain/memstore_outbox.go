package domain

import (
	"context"
	"sort"
	"time"
)

// MemStore's half of the event outbox (Track A3). Mirrors leave's and hcm's
// memstore_outbox.go. MemStore has no transactions: its maps are mutex-guarded
// so a write cannot be observed half-done by another goroutine, but that is a
// property of the map, not a durability guarantee. PgStore's is the real one.

// enqueueOutboxEvent stores one event for later delivery.
func (s *MemStore) enqueueOutboxEvent(_ context.Context, scope Scope, ev *OutboxEvent) error {
	_, err := s.outbox.create(scope, ev)
	return err
}

// DueOutboxTenants lists the tenants with events waiting. See the interface
// comment in store.go for why this one call is allowed to cross tenants.
func (s *MemStore) DueOutboxTenants(_ context.Context, now time.Time) ([]string, error) {
	s.outbox.mu.RLock()
	defer s.outbox.mu.RUnlock()

	out := make([]string, 0, len(s.outbox.rows))
	for tenant, rows := range s.outbox.rows {
		for _, ev := range rows {
			if ev.Deliverable(now) {
				out = append(out, tenant)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// DueOutboxEvents returns the events eligible for delivery now, oldest first.
func (s *MemStore) DueOutboxEvents(_ context.Context, scope Scope, now time.Time) ([]*OutboxEvent, error) {
	all, err := s.outbox.all(scope)
	if err != nil {
		return nil, err
	}
	return DueEvents(all, now), nil
}

// UpdateOutboxEvent records a delivery outcome.
func (s *MemStore) UpdateOutboxEvent(_ context.Context, scope Scope, ev *OutboxEvent) error {
	_, err := s.outbox.update(scope, ev)
	return err
}

// OutboxEventsFor returns every event ever enqueued about one entity id,
// oldest first.
func (s *MemStore) OutboxEventsFor(_ context.Context, scope Scope, entityID string) ([]*OutboxEvent, error) {
	all, err := s.outbox.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*OutboxEvent, 0, 2)
	for _, ev := range all {
		if ev.Envelope.EntityID == entityID {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Envelope.SourceVersion != out[j].Envelope.SourceVersion {
			return out[i].Envelope.SourceVersion < out[j].Envelope.SourceVersion
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
