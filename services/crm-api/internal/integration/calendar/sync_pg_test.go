package calendar

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/pgtest"
)

// Calendar synchronisation over real PostgreSQL.
//
// MemStore serializes everything behind one mutex, so it cannot show that the
// advisory lock is what stops two first-sightings both posting, nor that the
// mirror's timestamps and zone survive a real write. Both have already been
// defects in this integration once.

const migrationLockKey = 0x63726D5F6D696701

func holdSchemaLock(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		pgtest.Unavailable(t, "skipping live Postgres test (cannot reach %s): %v", pgtest.DSN(), err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire for schema lock: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, int64(migrationLockKey)); err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("take shared schema lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock_shared($1)`, int64(migrationLockKey))
		conn.Release()
		pool.Close()
	})
}

func requireCalendarSchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	defer pool.Close()

	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'calendar_sync_state' AND relkind = 'r')
		    AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'calendar_events' AND relkind = 'r')`).
		Scan(&ok); err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot read the catalog): %v", err)
	}
	if !ok {
		pgtest.Unavailable(t, "the Calendar sync tables are not present — run `make test-db-up`")
	}
}

func livePgFixture(t *testing.T) (domain.Store, domain.Scope, *domain.Contact) {
	t.Helper()
	holdSchemaLock(t)
	requireCalendarSchema(t)

	store, err := domain.NewPgStore(context.Background(), pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot open a store): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	stamp := time.Now().UnixNano()
	scope := domain.Scope{
		TenantID: fmt.Sprintf("tenant-cal-pg-%d", stamp),
		OrgID:    fmt.Sprintf("org-cal-pg-%d", stamp),
	}
	ctx := context.Background()

	if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID: owner, GoogleAccountEmail: ownerAddress,
		Status:          domain.GoogleStatusConnected,
		GrantedScopes:   []string{"https://www.googleapis.com/auth/calendar.events"},
		TokenCiphertext: []byte("sealed"),
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(ctx, scope, &domain.Contact{
		Email:     fmt.Sprintf("ada-%d@acme.example", stamp),
		AccountID: account.ID, FirstName: "Ada", LastName: "Attendee",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return store, scope, contact
}

// TestCalendarSyncSurvivesARealWriteAndRead walks a whole pass and checks
// what landed, including the zone and the cursor the next pass depends on.
func TestCalendarSyncSurvivesARealWriteAndRead(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()

	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	allDay := Event{
		ID: "pg-e-1", ICalUID: "pg-e-1@google.com", Status: "confirmed", Title: "Customer on site",
		Organizer: ownerAddress,
		Attendees: []Attendee{{Email: ownerAddress, Self: true}, {Email: contact.Email}},
		StartsAt:  time.Date(2026, 8, 1, 0, 0, 0, 0, kolkata), AllDay: true, TimeZone: "Asia/Kolkata",
	}
	noise := event("pg-e-noise", "Private appointment", "clinic@hospital.example",
		time.Now().UTC().Add(time.Hour))

	api := &stubAPI{pages: []Page{{Events: []Event{allDay, noise}, NextSyncToken: "tok-pg"}}}
	result, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner)
	if err != nil {
		t.Fatalf("sync over PostgreSQL: %v", err)
	}
	if result.EntriesCreated != 1 || result.EventsMatched != 1 {
		t.Fatalf("created %d entries from %d matches, want 1 / 1", result.EntriesCreated, result.EventsMatched)
	}

	events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if len(events) != 1 || events[0].ProviderEventID != "pg-e-1" {
		t.Fatalf("mirror = %+v; the unmatched event should have left no row", events)
	}
	stored := events[0]
	if !stored.AllDay || stored.TimeZone != "Asia/Kolkata" {
		t.Errorf("all-day/zone did not survive: allDay=%v zone=%q", stored.AllDay, stored.TimeZone)
	}
	if stored.StartsAt.UTC().Day() != 31 {
		t.Errorf("start = %v; the fixture expects 31 July in UTC", stored.StartsAt.UTC())
	}
	if local := stored.LocalStart(); local.Day() != 1 || local.Month() != time.August {
		t.Errorf("LocalStart = %v, want 1 August in the event's own zone", local)
	}
	if stored.ICalUID != "pg-e-1@google.com" {
		t.Errorf("iCalUID = %q; it cannot be backfilled without re-listing", stored.ICalUID)
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("timeline holds %d entries, want 1", len(activities))
	}
	if !strings.Contains(activities[0].Notes, "1 Aug 2026") {
		t.Errorf("timeline says %q; it must read as the day the event is in its own zone",
			activities[0].Notes)
	}

	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if state.SyncToken != "tok-pg" {
		t.Errorf("SyncToken = %q, want tok-pg — the next pass would re-list the window", state.SyncToken)
	}
	if !state.BackfillComplete {
		t.Error("BackfillComplete did not survive")
	}
	if !state.SyncingUntil.IsZero() {
		t.Error("the lease was not released")
	}
}

// TestRescheduleOverPostgresPostsOnce exercises the advisory-locked
// read-decide-write rather than MemStore's mutex.
func TestRescheduleOverPostgresPostsOnce(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	api := &stubAPI{pages: []Page{{
		Events: []Event{event("pg-move", "Review", contact.Email, start)}, NextSyncToken: "t1",
	}}}
	syncer := NewSyncer(store, api, staticToken{}, nil)
	if _, err := syncer.SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	// Same state twice, then moved twice.
	for _, tc := range []struct {
		name  string
		at    time.Time
		token string
		want  int
	}{
		{"unchanged", start, "t2", 0},
		{"moved", start.Add(2 * time.Hour), "t3", 1},
		{"moved again, same state", start.Add(2 * time.Hour), "t4", 0},
	} {
		api.pages = []Page{{
			Events: []Event{event("pg-move", "Review", contact.Email, tc.at)}, NextSyncToken: tc.token,
		}}
		result, err := syncer.SyncCalendar(ctx, scope, owner)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if result.EntriesCreated != tc.want {
			t.Errorf("%s created %d entries, want %d", tc.name, result.EntriesCreated, tc.want)
		}
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 2 {
		t.Errorf("timeline holds %d entries, want 2 (scheduled, rescheduled)", len(activities))
	}
}

// TestCalendarDisconnectKeepsTheMirrorOverPostgres: without the mirror, a
// reconnect reposts every meeting.
func TestCalendarDisconnectKeepsTheMirrorOverPostgres(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(24 * time.Hour)

	api := &stubAPI{pages: []Page{{
		Events: []Event{event("pg-keep", "Review", contact.Email, start)}, NextSyncToken: "t1",
	}}}
	syncer := NewSyncer(store, api, staticToken{}, nil)
	if _, err := syncer.SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	if _, err := store.PurgeCalendarDataForOwner(ctx, scope, owner, false); err != nil {
		t.Fatalf("stop sync: %v", err)
	}

	api.pages = []Page{{
		Events: []Event{event("pg-keep", "Review", contact.Email, start)}, NextSyncToken: "t2",
	}}
	again, err := syncer.SyncCalendar(ctx, scope, owner)
	if err != nil {
		t.Fatalf("sync after reconnecting: %v", err)
	}
	if again.EntriesCreated != 0 {
		t.Errorf("reconnecting created %d entries, want 0 — the timeline now has duplicates",
			again.EntriesCreated)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Errorf("timeline holds %d entries after disconnect and reconnect, want 1", len(activities))
	}
}
