package dispatch

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/platform"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakePublisher is a domain.EventPublisher stand-in for a fake bus: it
// remembers what it was handed and never talks to a real NATS connection.
type fakePublisher struct {
	mu   sync.Mutex
	sent []domain.Envelope
}

func (f *fakePublisher) Publish(_ context.Context, env domain.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, env)
	return nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func enqueue(t *testing.T, store domain.Store, tenant, entityID string) {
	t.Helper()
	env := domain.Envelope{
		ID:            "evt-" + tenant + "-" + entityID,
		Type:          "opportunity.created",
		TenantID:      tenant,
		CorrelationID: "corr-1",
		SourceApp:     domain.SourceAppCRM,
		EntityType:    "opportunity",
		EntityID:      entityID,
		SourceVersion: 1,
		Actor:         "tester",
		OccurredAt:    time.Now().UTC(),
	}
	if _, err := domain.EnqueueEvent(context.Background(), store, domain.Scope{TenantID: tenant}, env); err != nil {
		t.Fatalf("enqueue(%s): %v", tenant, err)
	}
}

// A dispatcher drain publishes an enqueued event via a fake publisher and
// marks it published — the seam Track B will call once a state transition
// decides to publish.
func TestOnceDeliversAnEnqueuedEventAndMarksItPublished(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	enqueue(t, store, "tenant-a", "opp-1")

	pub := &fakePublisher{}
	d := New(store, pub, time.Second, quiet())

	n, err := d.Once(ctx, time.Now())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if n != 1 {
		t.Fatalf("published %d events, want 1", n)
	}
	if pub.count() != 1 {
		t.Fatalf("fake publisher recorded %d events, want 1", pub.count())
	}

	history, err := store.OutboxEventsFor(ctx, domain.Scope{TenantID: "tenant-a"}, "opp-1")
	if err != nil {
		t.Fatalf("OutboxEventsFor: %v", err)
	}
	if len(history) != 1 || history[0].Status != domain.EventPublished {
		t.Fatalf("event history = %+v, want one published event", history)
	}
}

// The dispatcher must drain every tenant with work, and no tenant's event
// must appear on another tenant's envelope.
func TestOnceDrainsEveryTenantWithoutMixing(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	enqueue(t, store, "tenant-a", "opp-1")
	enqueue(t, store, "tenant-b", "opp-2")

	pub := &fakePublisher{}
	d := New(store, pub, time.Second, quiet())

	n, err := d.Once(ctx, time.Now())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if n != 2 {
		t.Fatalf("published %d events, want 2", n)
	}

	seen := map[string]string{}
	for _, env := range pub.sent {
		seen[env.TenantID] = env.EntityID
	}
	if seen["tenant-a"] != "opp-1" || seen["tenant-b"] != "opp-2" {
		t.Fatalf("events crossed tenants: %+v", seen)
	}
}

// Nothing enqueued must drain cleanly, not error.
func TestOnceWithNothingDueIsANoOp(t *testing.T) {
	store := domain.NewMemStore()
	pub := &fakePublisher{}
	d := New(store, pub, time.Second, quiet())

	n, err := d.Once(context.Background(), time.Now())
	if err != nil || n != 0 {
		t.Fatalf("Once on an empty outbox = %d, %v; want 0, nil", n, err)
	}
}

// Start must refuse to run a dispatcher against no bus, and say so rather
// than silently draining events into nowhere.
func TestStartDoesNothingWithoutABusConfigured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := domain.NewMemStore()
	enqueue(t, store, "tenant-a", "opp-1")

	if Start(ctx, store, platform.NewPublisher(nil), time.Millisecond, quiet()) {
		t.Fatal("Start ran a dispatcher with no bus configured")
	}

	time.Sleep(20 * time.Millisecond)
	due, err := store.DueOutboxEvents(ctx, domain.Scope{TenantID: "tenant-a"}, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxEvents: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("%d events still queued, want 1 — something drained them into nowhere", len(due))
	}
}

func TestSubjectConvention(t *testing.T) {
	env := domain.Envelope{
		SourceApp:  domain.SourceAppCRM,
		EntityType: "opportunity",
		TenantID:   "tenant-a",
	}
	if got, want := env.Subject(), "crm.opportunity.tenant-a"; got != want {
		t.Errorf("Subject() = %q, want %q", got, want)
	}
}
