package gmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The background poller.
//
// What is worth asserting here is not that a loop ticks: it is that one bad
// mailbox does not stop everyone else's mail from syncing, and that the only
// thing crossing a tenant boundary is a list of identifiers.

// pollerFixture builds a store with connected mailboxes in two tenants.
func pollerFixture(t *testing.T, tenants ...string) domain.Store {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	for _, tenant := range tenants {
		scope := domain.Scope{TenantID: tenant, OrgID: tenant}
		if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
			OwnerID:            mailboxOwner,
			GoogleAccountEmail: mailboxAddress,
			Status:             domain.GoogleStatusConnected,
			GrantedScopes:      []string{"https://www.googleapis.com/auth/gmail.readonly"},
			TokenCiphertext:    []byte("sealed"),
		}); err != nil {
			t.Fatalf("seed connection in %s: %v", tenant, err)
		}
		account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "o"})
		if err != nil {
			t.Fatalf("seed account in %s: %v", tenant, err)
		}
		if _, err := store.CreateContact(ctx, scope, &domain.Contact{
			Email: "bea@acme.example", AccountID: account.ID, FirstName: "Bea", LastName: "B",
		}); err != nil {
			t.Fatalf("seed contact in %s: %v", tenant, err)
		}
		if _, err := store.SaveGmailSyncState(ctx, scope, &domain.GmailSyncState{OwnerID: mailboxOwner}); err != nil {
			t.Fatalf("seed sync state in %s: %v", tenant, err)
		}
	}
	return store
}

// TestOneFailingMailboxDoesNotStopTheCycle. A single revoked credential must
// not stop every other user's mail from syncing, which is what an early
// return on the first error would do.
func TestOneFailingMailboxDoesNotStopTheCycle(t *testing.T) {
	ctx := context.Background()
	store := pollerFixture(t, "tenant-broken", "tenant-healthy")

	// Break the first tenant's connection by narrowing its consent.
	brokenScope := domain.Scope{TenantID: "tenant-broken", OrgID: "tenant-broken"}
	connection, err := store.GetGoogleConnection(ctx, brokenScope, mailboxOwner)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.GrantedScopes = []string{"https://www.googleapis.com/auth/calendar.events"}
	if _, err := store.SaveGoogleConnection(ctx, brokenScope, connection); err != nil {
		t.Fatalf("narrow consent: %v", err)
	}

	at := time.Now().UTC()
	api := &stubAPI{
		profile:  Profile{HistoryID: "1"},
		pages:    []stubPage{{ids: []string{"m-1"}}},
		messages: map[string]Message{"m-1": message("m-1", "bea@acme.example", mailboxAddress, "Hello", at)},
	}
	poller := NewPoller(store, NewSyncer(store, api, staticToken{}, nil, nil), 0, nil)

	entries, err := poller.Once(ctx, at)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if entries != 1 {
		t.Errorf("cycle wrote %d entries, want 1 — the healthy mailbox should still have synced", entries)
	}

	// Both mailboxes recorded an outcome, and the broken one records why.
	broken, err := store.GetGmailSyncState(ctx, brokenScope, mailboxOwner)
	if err != nil {
		t.Fatalf("read broken state: %v", err)
	}
	if broken.LastError != ReasonNotConnected {
		t.Errorf("broken mailbox reason = %q, want %q", broken.LastError, ReasonNotConnected)
	}
	healthy, err := store.GetGmailSyncState(ctx,
		domain.Scope{TenantID: "tenant-healthy", OrgID: "tenant-healthy"}, mailboxOwner)
	if err != nil {
		t.Fatalf("read healthy state: %v", err)
	}
	if healthy.LastError != "" {
		t.Errorf("healthy mailbox recorded an error: %q", healthy.LastError)
	}
	if healthy.ActivitiesCreated != 1 {
		t.Errorf("healthy mailbox created %d activities, want 1", healthy.ActivitiesCreated)
	}
}

// TestEachMailboxSyncsUnderItsOwnTenant. The poller discovers mailboxes
// across tenants; everything after that must be scoped, or one tenant's sync
// would write into another's records.
func TestEachMailboxSyncsUnderItsOwnTenant(t *testing.T) {
	ctx := context.Background()
	store := pollerFixture(t, "tenant-a", "tenant-b")
	at := time.Now().UTC()

	api := &stubAPI{
		profile:  Profile{HistoryID: "1"},
		pages:    []stubPage{{ids: []string{"m-1"}}},
		messages: map[string]Message{"m-1": message("m-1", "bea@acme.example", mailboxAddress, "Hello", at)},
	}
	poller := NewPoller(store, NewSyncer(store, api, staticToken{}, nil, nil), 0, nil)
	if _, err := poller.Once(ctx, at); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	// Each tenant's own contact got exactly one entry, on its own timeline.
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		scope := domain.Scope{TenantID: tenant, OrgID: tenant}
		contact, err := store.FindContactByEmail(ctx, scope, "bea@acme.example")
		if err != nil {
			t.Fatalf("find contact in %s: %v", tenant, err)
		}
		entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list entries in %s: %v", tenant, err)
		}
		if len(entries) != 1 {
			t.Errorf("tenant %s holds %d entries, want 1", tenant, len(entries))
		}
	}
}

// TestALeasedMailboxIsSkippedNotFailed. Two replicas polling is the expected
// deployment, so losing a lease race is routine, not an error.
func TestALeasedMailboxIsSkippedNotFailed(t *testing.T) {
	ctx := context.Background()
	store := pollerFixture(t, "tenant-a")
	at := time.Now().UTC()
	scope := domain.Scope{TenantID: "tenant-a", OrgID: "tenant-a"}

	if _, err := store.ClaimGmailSync(ctx, scope, mailboxOwner, at, time.Hour); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}

	api := &stubAPI{}
	poller := NewPoller(store, NewSyncer(store, api, staticToken{}, nil, nil), 0, nil)
	if _, err := poller.Once(ctx, at); err != nil {
		t.Fatalf("a cycle that lost a lease race reported failure: %v", err)
	}
	if api.listCalls != 0 {
		t.Error("the poller walked a mailbox another replica held")
	}
}

// TestRunStopsWithItsContext: a background loop that outlives shutdown keeps
// the process alive.
func TestRunStopsWithItsContext(t *testing.T) {
	store := pollerFixture(t, "tenant-a")
	poller := NewPoller(store, NewSyncer(store, &stubAPI{}, staticToken{}, nil, nil), time.Minute, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { poller.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
}

// TestStartDoesNothingWithoutAConfiguredSyncer, rather than starting a loop
// that fails on every tick.
func TestStartDoesNothingWithoutAConfiguredSyncer(t *testing.T) {
	if Start(context.Background(), domain.NewMemStore(), nil, 0, nil) {
		t.Error("Start claimed to run a poller with no syncer")
	}
}

// TestCycleStopsOnACancelledContext rather than working through every due
// mailbox during shutdown.
func TestCycleStopsOnACancelledContext(t *testing.T) {
	store := pollerFixture(t, "tenant-a", "tenant-b")
	poller := NewPoller(store, NewSyncer(store, &stubAPI{}, staticToken{}, nil, nil), 0, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := poller.Once(ctx, time.Now().UTC()); !errors.Is(err, context.Canceled) {
		t.Errorf("cycle on a cancelled context: %v, want context.Canceled", err)
	}
}
