package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// Two-organization isolation proof at the asynchronous-worker layer
// (task.md §1.3).
//
// The dispatcher is the one component in the CRM that legitimately looks
// across tenants: DueOutboxTenants asks "who has work?", which no single
// tenant's scope could answer. Everything after that answer must narrow back
// to one tenant. These tests pin that shape down — the cross-tenant read is
// confined to tenant *identifiers*, every read of tenant *data* travels under
// a real Scope, and one organization's event can never leave on another
// organization's subject.

// recordingStore watches the scopes the drain uses. It embeds domain.Store
// so it satisfies the interface's unexported members by promotion.
type recordingStore struct {
	domain.Store

	mu             sync.Mutex
	dueEventScopes []domain.Scope
	updateScopes   []domain.Scope
}

func (r *recordingStore) DueOutboxEvents(ctx context.Context, scope domain.Scope, now time.Time) ([]*domain.OutboxEvent, error) {
	r.mu.Lock()
	r.dueEventScopes = append(r.dueEventScopes, scope)
	r.mu.Unlock()
	return r.Store.DueOutboxEvents(ctx, scope, now)
}

func (r *recordingStore) UpdateOutboxEvent(ctx context.Context, scope domain.Scope, ev *domain.OutboxEvent) error {
	r.mu.Lock()
	r.updateScopes = append(r.updateScopes, scope)
	r.mu.Unlock()
	return r.Store.UpdateOutboxEvent(ctx, scope, ev)
}

// TestDrainReadsTenantDataOnlyUnderARealScope proves the narrowing: however
// many tenants the cross-tenant discovery query names, every subsequent read
// and write is scoped to exactly one of them.
func TestDrainReadsTenantDataOnlyUnderARealScope(t *testing.T) {
	ctx := context.Background()
	tenants := []string{"tenant-alpha", "tenant-beta", "tenant-gamma"}

	inner := domain.NewMemStore()
	for i, tenant := range tenants {
		enqueue(t, inner, tenant, fmt.Sprintf("opp-%d", i))
	}
	store := &recordingStore{Store: inner}

	if _, err := New(store, &fakePublisher{}, time.Second, quiet()).Once(ctx, time.Now()); err != nil {
		t.Fatalf("Once: %v", err)
	}

	if len(store.dueEventScopes) != len(tenants) {
		t.Fatalf("drain read events under %d scopes, want one per tenant (%d)",
			len(store.dueEventScopes), len(tenants))
	}
	seen := map[string]bool{}
	for _, scope := range append(append([]domain.Scope{}, store.dueEventScopes...), store.updateScopes...) {
		if !scope.Valid() {
			t.Error("the drain read or wrote outbox rows under an empty scope — " +
				"that query is unfiltered by tenant")
		}
		seen[scope.TenantID] = true
	}
	for _, tenant := range tenants {
		if !seen[tenant] {
			t.Errorf("no scoped read for %s; its events were never drained", tenant)
		}
	}
	if len(seen) != len(tenants) {
		t.Errorf("drain used %d distinct tenant scopes, want %d", len(seen), len(tenants))
	}
}

// TestPublishedEventsCarryOnlyTheirOwnTenant checks the published envelopes
// themselves: the subject a message lands on is derived from the envelope,
// so an envelope carrying the wrong tenant delivers one organization's data
// to another organization's subscribers.
func TestPublishedEventsCarryOnlyTheirOwnTenant(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()

	// entityOwner is the answer key: which tenant each entity id belongs to.
	entityOwner := map[string]string{}
	for i, tenant := range []string{"tenant-alpha", "tenant-beta", "tenant-gamma"} {
		entity := fmt.Sprintf("opp-%d", i)
		entityOwner[entity] = tenant
		enqueue(t, store, tenant, entity)
	}

	pub := &fakePublisher{}
	published, err := New(store, pub, time.Second, quiet()).Once(ctx, time.Now())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if published != len(entityOwner) {
		t.Fatalf("published %d events, want %d", published, len(entityOwner))
	}

	for _, env := range pub.sent {
		owner, known := entityOwner[env.EntityID]
		if !known {
			t.Errorf("published an event for unknown entity %q", env.EntityID)
			continue
		}
		if env.TenantID != owner {
			t.Errorf("entity %s belongs to %s but was published as %s",
				env.EntityID, owner, env.TenantID)
		}
		// Subject() is what the bus routes on; it must name the same tenant.
		if want := "crm.opportunity." + owner; env.Subject() != want {
			t.Errorf("entity %s published on subject %q, want %q",
				env.EntityID, env.Subject(), want)
		}
		if !strings.HasSuffix(env.Subject(), env.TenantID) {
			t.Errorf("subject %q does not name the envelope's own tenant %q",
				env.Subject(), env.TenantID)
		}
	}
}

// tenantFailingPublisher rejects one tenant's events and accepts the rest.
type tenantFailingPublisher struct {
	fakePublisher
	failFor string
}

func (p *tenantFailingPublisher) Publish(ctx context.Context, env domain.Envelope) error {
	if env.TenantID == p.failFor {
		return errors.New("bus rejected the message")
	}
	return p.fakePublisher.Publish(ctx, env)
}

// TestOneTenantsBusFailureDoesNotDisturbAnother covers the blast radius: a
// tenant whose delivery fails must be retried on its own, without dropping,
// duplicating, or reattributing anybody else's events.
func TestOneTenantsBusFailureDoesNotDisturbAnother(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	enqueue(t, store, "tenant-broken", "opp-broken")
	enqueue(t, store, "tenant-healthy", "opp-healthy")

	pub := &tenantFailingPublisher{failFor: "tenant-broken"}
	published, err := New(store, pub, time.Second, quiet()).Once(ctx, time.Now())
	if err == nil {
		t.Error("Once reported success while one tenant's delivery failed")
	}
	if published != 1 {
		t.Errorf("published %d events, want 1 — the healthy tenant's event", published)
	}
	for _, env := range pub.sent {
		if env.TenantID == "tenant-broken" {
			t.Error("an event from the failing tenant reached the bus anyway")
		}
	}

	// The failed event stays in its own tenant's outbox, unpublished.
	still, err := store.DueOutboxEvents(ctx, domain.Scope{TenantID: "tenant-broken"},
		time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("DueOutboxEvents(tenant-broken): %v", err)
	}
	if len(still) != 1 {
		t.Fatalf("%d events left queued for the failing tenant, want 1", len(still))
	}
	if still[0].Envelope.TenantID != "tenant-broken" {
		t.Errorf("the requeued event is labelled %q, want tenant-broken",
			still[0].Envelope.TenantID)
	}

	// And the healthy tenant's outbox is empty rather than holding a copy of
	// the failure.
	healthy, err := store.DueOutboxEvents(ctx, domain.Scope{TenantID: "tenant-healthy"},
		time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("DueOutboxEvents(tenant-healthy): %v", err)
	}
	for _, ev := range healthy {
		if ev.Envelope.EntityID == "opp-broken" {
			t.Error("another tenant's failed event was requeued into the healthy tenant")
		}
	}
}

// TestDueOutboxEventsIgnoresOtherTenantsRows is the store-level guarantee the
// dispatcher relies on: claiming work for one tenant must never claim
// another's, or two organizations' events would interleave in one batch.
func TestDueOutboxEventsIgnoresOtherTenantsRows(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	enqueue(t, store, "tenant-alpha", "opp-alpha")
	enqueue(t, store, "tenant-beta", "opp-beta")

	claimed, err := store.DueOutboxEvents(ctx, domain.Scope{TenantID: "tenant-alpha"}, time.Now())
	if err != nil {
		t.Fatalf("DueOutboxEvents: %v", err)
	}
	for _, ev := range claimed {
		if ev.Envelope.TenantID != "tenant-alpha" {
			t.Errorf("claiming alpha's work returned an event owned by %q", ev.Envelope.TenantID)
		}
	}

	if _, err := store.DueOutboxEvents(ctx, domain.Scope{}, time.Now()); !errors.Is(err, domain.ErrNoTenant) {
		t.Errorf("DueOutboxEvents with no scope: err = %v, want ErrNoTenant", err)
	}
}
