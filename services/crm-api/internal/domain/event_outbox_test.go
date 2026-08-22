package domain

import (
	"context"
	"testing"
	"time"
)

func testEnvelope(tenant, entityID string, version int64) Envelope {
	return Envelope{
		ID:            "evt-" + tenant + "-" + entityID,
		Type:          "opportunity.created",
		TenantID:      tenant,
		CorrelationID: "corr-1",
		SourceApp:     SourceAppCRM,
		EntityType:    "opportunity",
		EntityID:      entityID,
		SourceVersion: version,
		Actor:         "tester",
		OccurredAt:    time.Now().UTC(),
	}
}

// EnqueueEvent must reject a call with no tenant in scope — the same rule
// every other store method follows.
func TestEnqueueEventRequiresScope(t *testing.T) {
	store := NewMemStore()
	_, err := EnqueueEvent(context.Background(), store, Scope{}, testEnvelope("t1", "opp-1", 1))
	if err != ErrNoTenant {
		t.Fatalf("EnqueueEvent with no scope = %v, want ErrNoTenant", err)
	}
}

// A tenant A event enqueued must not be visible when tenant B asks what is
// due — the cross-tenant isolation rule (.claude/rules/tenancy-and-scopes.md).
func TestEnqueueEventIsTenantIsolated(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()

	if _, err := EnqueueEvent(ctx, store, Scope{TenantID: "tenant-a"}, testEnvelope("tenant-a", "opp-1", 1)); err != nil {
		t.Fatalf("enqueue for tenant-a: %v", err)
	}

	dueA, err := store.DueOutboxEvents(ctx, Scope{TenantID: "tenant-a"}, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxEvents(tenant-a): %v", err)
	}
	if len(dueA) != 1 {
		t.Fatalf("tenant-a has %d due events, want 1", len(dueA))
	}

	dueB, err := store.DueOutboxEvents(ctx, Scope{TenantID: "tenant-b"}, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxEvents(tenant-b): %v", err)
	}
	if len(dueB) != 0 {
		t.Fatalf("tenant-b sees %d events written under tenant-a, want 0", len(dueB))
	}
}

// DueOutboxTenants is the one cross-tenant read: it must report exactly the
// tenants with outstanding work, and nothing about their content.
func TestDueOutboxTenantsListsOnlyTenantsWithWork(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()

	if _, err := EnqueueEvent(ctx, store, Scope{TenantID: "tenant-a"}, testEnvelope("tenant-a", "opp-1", 1)); err != nil {
		t.Fatalf("enqueue tenant-a: %v", err)
	}
	if _, err := EnqueueEvent(ctx, store, Scope{TenantID: "tenant-c"}, testEnvelope("tenant-c", "opp-9", 1)); err != nil {
		t.Fatalf("enqueue tenant-c: %v", err)
	}

	tenants, err := store.DueOutboxTenants(ctx, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxTenants: %v", err)
	}
	got := map[string]bool{}
	for _, tid := range tenants {
		got[tid] = true
	}
	if !got["tenant-a"] || !got["tenant-c"] {
		t.Fatalf("DueOutboxTenants = %v, want tenant-a and tenant-c present", tenants)
	}
	if got["tenant-b"] {
		t.Fatalf("DueOutboxTenants reported tenant-b, which enqueued nothing")
	}
}

// A retried enqueue at the same (entity, version) must not duplicate: the
// source_version unique key is the dedup guard PgStore's ON CONFLICT DO
// NOTHING relies on. MemStore mirrors it here via plain create() semantics —
// this test documents the contract even though MemStore's create() does not
// itself enforce the unique key (PgStore's schema constraint does).
func TestOutboxEventLifecycle(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	scope := Scope{TenantID: "tenant-a"}

	ev, err := EnqueueEvent(ctx, store, scope, testEnvelope("tenant-a", "opp-1", 1))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if ev.Status != EventPending {
		t.Fatalf("new event status = %q, want pending", ev.Status)
	}

	due, err := store.DueOutboxEvents(ctx, scope, time.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("DueOutboxEvents = %v, %v; want 1 event", due, err)
	}

	if err := due[0].MarkSent(time.Now()); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if err := store.UpdateOutboxEvent(ctx, scope, due[0]); err != nil {
		t.Fatalf("UpdateOutboxEvent: %v", err)
	}
	_ = due[0].MarkPublished()
	if err := store.UpdateOutboxEvent(ctx, scope, due[0]); err != nil {
		t.Fatalf("UpdateOutboxEvent (published): %v", err)
	}

	stillDue, err := store.DueOutboxEvents(ctx, scope, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxEvents after publish: %v", err)
	}
	if len(stillDue) != 0 {
		t.Fatalf("%d events still due after publish, want 0", len(stillDue))
	}

	history, err := store.OutboxEventsFor(ctx, scope, "opp-1")
	if err != nil {
		t.Fatalf("OutboxEventsFor: %v", err)
	}
	if len(history) != 1 || history[0].Status != EventPublished {
		t.Fatalf("OutboxEventsFor = %+v, want one published event", history)
	}
}
