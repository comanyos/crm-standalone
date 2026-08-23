package gmail

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/pgtest"
)

// Synchronisation over real PostgreSQL.
//
// sync_test.go runs the same flow on MemStore, and that is not enough on its
// own: MemStore round-trips whole Go structs and serializes everything behind
// one mutex, so it cannot show that a cursor survives a real write, that
// ON CONFLICT DO NOTHING is what actually stops a duplicate, or that the
// Activity and its index row commit together. Each of those has already been
// a real defect in this integration once.

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
		pgtest.Unavailable(t,
			"skipping live Postgres test (cannot reach %s — run `make test-db-up`): %v", pgtest.DSN(), err)
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

func requireGmailSchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	defer pool.Close()

	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'gmail_sync_state' AND relkind = 'r')
		    AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'gmail_messages' AND relkind = 'r')`).
		Scan(&ok); err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot read the catalog): %v", err)
	}
	if !ok {
		pgtest.Unavailable(t, "the Gmail sync tables are not present — run `make test-db-up`")
	}
}

// livePgFixture seeds one connected mailbox and one Contact in a tenant of
// this run's own, since the test database outlives the run.
func livePgFixture(t *testing.T) (domain.Store, domain.Scope, *domain.Contact) {
	t.Helper()
	holdSchemaLock(t)
	requireGmailSchema(t)

	store, err := domain.NewPgStore(context.Background(), pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot open a store): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	stamp := time.Now().UnixNano()
	scope := domain.Scope{
		TenantID: fmt.Sprintf("tenant-gmail-pg-%d", stamp),
		OrgID:    fmt.Sprintf("org-gmail-pg-%d", stamp),
	}
	ctx := context.Background()

	if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID:            mailboxOwner,
		GoogleAccountEmail: mailboxAddress,
		Status:             domain.GoogleStatusConnected,
		GrantedScopes:      []string{"https://www.googleapis.com/auth/gmail.readonly"},
		TokenCiphertext:    []byte("sealed"),
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(ctx, scope, &domain.Contact{
		Email:     fmt.Sprintf("bea-%d@acme.example", stamp),
		AccountID: account.ID, FirstName: "Bea", LastName: "Buyer",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return store, scope, contact
}

// TestSyncSurvivesARealWriteAndRead walks a whole pass against PostgreSQL and
// checks what actually landed — the timeline entry, the index row, and every
// cursor field the next pass depends on.
func TestSyncSurvivesARealWriteAndRead(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	api := &stubAPI{
		profile: Profile{EmailAddress: mailboxAddress, HistoryID: "4242"},
		pages:   []stubPage{{ids: []string{"pg-m-1", "pg-m-noise"}}},
		messages: map[string]Message{
			"pg-m-1":     message("pg-m-1", contact.Email, mailboxAddress, "Renewal terms", at),
			"pg-m-noise": message("pg-m-noise", "stranger@nowhere.example", mailboxAddress, "Private", at),
		},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil, nil)

	result, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("sync over PostgreSQL: %v", err)
	}
	if result.EntriesCreated != 1 || result.MessagesMatched != 1 {
		t.Fatalf("created %d entries from %d matches, want 1 / 1", result.EntriesCreated, result.MessagesMatched)
	}

	// The Activity and its index row both committed.
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("timeline holds %d entries, want 1", len(activities))
	}
	entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("index holds %d rows, want 1", len(entries))
	}
	if entries[0].ActivityID != activities[0].ID {
		t.Error("the index row and the Activity it names disagree; one of the two writes did not commit")
	}

	// And the private message left nothing behind: the index holds exactly
	// the one matched message, and the entry above already confirmed which.
	if entries[0].ProviderMessageID != "pg-m-1" {
		t.Errorf("indexed %q; a message with no CRM counterparty reached PostgreSQL",
			entries[0].ProviderMessageID)
	}
	if entries[0].MessageSubject != "Renewal terms" {
		t.Errorf("stored subject = %q", entries[0].MessageSubject)
	}

	// Every cursor field the next pass needs survived the round trip.
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if state.LastHistoryID != "4242" {
		t.Errorf("LastHistoryID = %q, want 4242 — the next pass would re-backfill", state.LastHistoryID)
	}
	if !state.BackfillComplete {
		t.Error("BackfillComplete did not survive; the mailbox would backfill forever")
	}
	if state.MessagesIndexed != 1 || state.ActivitiesCreated != 1 {
		t.Errorf("counters = %d/%d, want 1/1", state.MessagesIndexed, state.ActivitiesCreated)
	}
	if state.LastMessageAt.IsZero() {
		t.Error("LastMessageAt did not survive; the UI cannot say how current the CRM is")
	}
	if !state.SyncingUntil.IsZero() {
		t.Error("the lease was not released, so the next pass would skip this mailbox")
	}
}

// TestReSyncOverPostgresCreatesNoDuplicates exercises the ON CONFLICT DO
// NOTHING that actually implements idempotency, rather than MemStore's map
// lookup.
func TestReSyncOverPostgresCreatesNoDuplicates(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)

	api := &stubAPI{
		profile:  Profile{HistoryID: "1"},
		pages:    []stubPage{{ids: []string{"pg-dup"}}},
		messages: map[string]Message{"pg-dup": message("pg-dup", contact.Email, mailboxAddress, "Hello", at)},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil, nil)

	if _, err := syncer.SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	state.BackfillComplete, state.BackfillPageToken = false, ""
	if _, err := store.SaveGmailSyncState(ctx, scope, state); err != nil {
		t.Fatalf("reset cursor: %v", err)
	}

	second, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.EntriesCreated != 0 {
		t.Errorf("a re-sync over PostgreSQL created %d entries, want 0", second.EntriesCreated)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Errorf("timeline holds %d entries after two passes over one message, want 1", len(activities))
	}
}

// TestDisconnectKeepsTheDeduplicationIndexOverPostgres. This is the
// consequence that is easy to get backwards: dropping the index on disconnect
// means a reconnect posts a second copy of every email.
func TestDisconnectKeepsTheDeduplicationIndexOverPostgres(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)

	api := &stubAPI{
		profile:  Profile{HistoryID: "1"},
		pages:    []stubPage{{ids: []string{"pg-keep"}}},
		messages: map[string]Message{"pg-keep": message("pg-keep", contact.Email, mailboxAddress, "Hello", at)},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil, nil)
	if _, err := syncer.SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// What a disconnect does: drop the cursor, keep the index.
	if _, err := store.PurgeGmailDataForOwner(ctx, scope, mailboxOwner, false); err != nil {
		t.Fatalf("stop sync: %v", err)
	}

	// Reconnect and sync again from scratch.
	again, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("sync after reconnecting: %v", err)
	}
	if again.EntriesCreated != 0 {
		t.Errorf("reconnecting created %d entries, want 0 — the contact's timeline now has duplicates",
			again.EntriesCreated)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Errorf("timeline holds %d entries after a disconnect and reconnect, want 1", len(activities))
	}
}

// TestErasureIsNotUndoneByTheNextPass is the end-to-end proof that this
// feature is not theatre, over the store where it actually has to hold.
//
// It is the whole loop: sync collects, an erasure removes and suppresses, and
// then a real pass runs again over the same mailbox — including the same
// message and a new one from the same person. Nothing may come back.
func TestErasureIsNotUndoneByTheNextPass(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)

	api := &stubAPI{
		profile: Profile{HistoryID: "1"},
		pages:   []stubPage{{ids: []string{"pg-erase-1"}}},
		messages: map[string]Message{
			"pg-erase-1": message("pg-erase-1", contact.Email, mailboxAddress, "Confidential terms", at),
		},
	}
	syncer := NewSyncer(store, api, staticToken{}, nil, nil)
	if _, err := syncer.SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("timeline holds %d entries before erasure, want 1", len(activities))
	}

	// The erasure, exactly as the RPC performs it.
	result, err := store.EraseSyncedContent(ctx, scope, domain.EraseSubject{
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		Actor: "admin-1", Reason: "erasure request",
		SuppressAddresses: []string{contact.Email},
	})
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if result.ActivitiesDeleted != 1 || result.AddressesSuppressed != 1 {
		t.Fatalf("erasure = %+v", result)
	}

	// Now a real pass, over the erased message and a new one from the same
	// person. Force a backfill so both are examined, as an expired history
	// cursor would.
	state, err := store.GetGmailSyncState(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	state.BackfillComplete, state.BackfillPageToken, state.LastHistoryID = false, "", ""
	if _, err := store.SaveGmailSyncState(ctx, scope, state); err != nil {
		t.Fatalf("reset cursor: %v", err)
	}
	api.pages = []stubPage{{ids: []string{"pg-erase-1", "pg-erase-2"}}}
	api.messages["pg-erase-2"] = message("pg-erase-2", contact.Email, mailboxAddress, "Another one", at)

	second, err := syncer.SyncMailbox(ctx, scope, mailboxOwner)
	if err != nil {
		t.Fatalf("sync after erasure: %v", err)
	}
	if second.EntriesCreated != 0 {
		t.Errorf("a pass after erasure created %d entries", second.EntriesCreated)
	}
	if second.MessagesMatched != 0 {
		t.Errorf("a pass after erasure matched %d messages from a suppressed address",
			second.MessagesMatched)
	}

	activities, err = store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 0 {
		t.Fatalf("erased content returned to the timeline: %d entries", len(activities))
	}

	// And the tombstone is still redacted — nothing refreshed its subject.
	entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	for _, entry := range entries {
		if entry.MessageSubject != "" {
			t.Errorf("a tombstone regained content: %q", entry.MessageSubject)
		}
	}
}

// TestRetentionOverPostgresAgesOutOldMailInsideTheSyncPass.
func TestRetentionOverPostgresAgesOutOldMailInsideTheSyncPass(t *testing.T) {
	store, scope, contact := livePgFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// One old message, seeded directly.
	activityID := domain.NewID()
	if _, err := store.RecordGmailActivity(ctx, scope, &domain.GmailMessage{
		OwnerID: mailboxOwner, ProviderMessageID: "pg-old",
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		ActivityID: activityID, MessageSubject: "Ancient", FromAddress: contact.Email,
		Direction: domain.GmailInbound, OccurredAt: now.Add(-100 * 24 * time.Hour),
	}, &domain.Activity{
		ID: activityID, Kind: domain.GmailActivityKind,
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		Notes: "Email received: Ancient", ActorID: mailboxOwner,
		OccurredAt: now.Add(-100 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed old message: %v", err)
	}

	api := &stubAPI{profile: Profile{HistoryID: "1"}, pages: []stubPage{{}}, messages: map[string]Message{}}
	syncer := NewSyncer(store, api, staticToken{},
		retainer{store: store, window: 90 * 24 * time.Hour}, nil)
	if _, err := syncer.SyncMailbox(ctx, scope, mailboxOwner); err != nil {
		t.Fatalf("sync: %v", err)
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 0 {
		t.Errorf("the retention window did not age out the old message over PostgreSQL: %d entries",
			len(activities))
	}
	entries, err := store.ListGmailMessagesForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(entries) != 1 || entries[0].ErasedAt.IsZero() {
		t.Errorf("the aged-out message left no tombstone, so it would be re-collected: %+v", entries)
	}
}
