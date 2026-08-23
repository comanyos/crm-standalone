package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

func pollerFixture(t *testing.T, tenants ...string) domain.Store {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	for _, tenant := range tenants {
		scope := domain.Scope{TenantID: tenant, OrgID: tenant}
		if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
			OwnerID: owner, GoogleAccountEmail: ownerAddress,
			Status:          domain.GoogleStatusConnected,
			GrantedScopes:   []string{"https://www.googleapis.com/auth/calendar.events"},
			TokenCiphertext: []byte("sealed"),
		}); err != nil {
			t.Fatalf("seed connection in %s: %v", tenant, err)
		}
		account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "o"})
		if err != nil {
			t.Fatalf("seed account in %s: %v", tenant, err)
		}
		if _, err := store.CreateContact(ctx, scope, &domain.Contact{
			Email: "ada@acme.example", AccountID: account.ID, FirstName: "Ada", LastName: "A",
		}); err != nil {
			t.Fatalf("seed contact in %s: %v", tenant, err)
		}
		if _, err := store.SaveCalendarSyncState(ctx, scope, &domain.CalendarSyncState{OwnerID: owner}); err != nil {
			t.Fatalf("seed sync state in %s: %v", tenant, err)
		}
	}
	return store
}

// TestOneFailingCalendarDoesNotStopTheCycle.
func TestOneFailingCalendarDoesNotStopTheCycle(t *testing.T) {
	ctx := context.Background()
	store := pollerFixture(t, "tenant-broken", "tenant-healthy")

	brokenScope := domain.Scope{TenantID: "tenant-broken", OrgID: "tenant-broken"}
	connection, err := store.GetGoogleConnection(ctx, brokenScope, owner)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.GrantedScopes = []string{"https://www.googleapis.com/auth/gmail.readonly"}
	if _, err := store.SaveGoogleConnection(ctx, brokenScope, connection); err != nil {
		t.Fatalf("narrow consent: %v", err)
	}

	at := time.Now().UTC()
	api := &stubAPI{pages: []Page{{
		Events:        []Event{event("e-1", "Review", "ada@acme.example", at.Add(time.Hour))},
		NextSyncToken: "tok",
	}}}
	poller := NewPoller(store, NewSyncer(store, api, staticToken{}, nil, nil), 0, nil)

	entries, err := poller.Once(ctx, at)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if entries != 1 {
		t.Errorf("cycle wrote %d entries, want 1 — the healthy calendar should still have synced", entries)
	}
	broken, err := store.GetCalendarSyncState(ctx, brokenScope, owner)
	if err != nil {
		t.Fatalf("read broken state: %v", err)
	}
	if broken.LastError != ReasonNotConnected {
		t.Errorf("broken calendar reason = %q, want %q", broken.LastError, ReasonNotConnected)
	}
}

// TestEachCalendarSyncsUnderItsOwnTenant.
func TestEachCalendarSyncsUnderItsOwnTenant(t *testing.T) {
	ctx := context.Background()
	store := pollerFixture(t, "tenant-a", "tenant-b")
	at := time.Now().UTC()

	api := &stubAPI{pages: []Page{{
		Events:        []Event{event("e-1", "Review", "ada@acme.example", at.Add(time.Hour))},
		NextSyncToken: "tok",
	}}}
	poller := NewPoller(store, NewSyncer(store, api, staticToken{}, nil, nil), 0, nil)
	if _, err := poller.Once(ctx, at); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		scope := domain.Scope{TenantID: tenant, OrgID: tenant}
		contact, err := store.FindContactByEmail(ctx, scope, "ada@acme.example")
		if err != nil {
			t.Fatalf("find contact in %s: %v", tenant, err)
		}
		events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list in %s: %v", tenant, err)
		}
		if len(events) != 1 {
			t.Errorf("tenant %s holds %d events, want 1", tenant, len(events))
		}
	}
}

// TestALeasedCalendarIsSkippedNotFailed.
func TestALeasedCalendarIsSkippedNotFailed(t *testing.T) {
	ctx := context.Background()
	store := pollerFixture(t, "tenant-a")
	at := time.Now().UTC()
	scope := domain.Scope{TenantID: "tenant-a", OrgID: "tenant-a"}

	if _, err := store.ClaimCalendarSync(ctx, scope, owner, at, time.Hour); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}
	api := &stubAPI{}
	poller := NewPoller(store, NewSyncer(store, api, staticToken{}, nil, nil), 0, nil)
	if _, err := poller.Once(ctx, at); err != nil {
		t.Fatalf("a cycle that lost a lease race reported failure: %v", err)
	}
	if len(api.optsSeen) != 0 {
		t.Error("the poller walked a calendar another replica held")
	}
}

// TestRunStopsWithItsContext.
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

// TestStartDoesNothingWithoutAConfiguredSyncer.
func TestStartDoesNothingWithoutAConfiguredSyncer(t *testing.T) {
	if Start(context.Background(), domain.NewMemStore(), nil, 0, nil) {
		t.Error("Start claimed to run a poller with no syncer")
	}
}

// TestCycleStopsOnACancelledContext.
func TestCycleStopsOnACancelledContext(t *testing.T) {
	store := pollerFixture(t, "tenant-a", "tenant-b")
	poller := NewPoller(store, NewSyncer(store, &stubAPI{}, staticToken{}, nil, nil), 0, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := poller.Once(ctx, time.Now().UTC()); !errors.Is(err, context.Canceled) {
		t.Errorf("cycle on a cancelled context: %v, want context.Canceled", err)
	}
}
