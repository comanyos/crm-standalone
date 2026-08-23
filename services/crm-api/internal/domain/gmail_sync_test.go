package domain

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Gmail synchronisation storage, asserted against both implementations.
//
// Two properties here are not conveniences. A mailbox lease that two replicas
// can both take means two passes walking one mailbox and racing to post the
// same timeline entries. A timeline entry that can be written twice means a
// customer's record accumulates a duplicate every time sync runs. Both are
// invisible in a single-threaded test, so both are exercised concurrently.

func eachGmailStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

var gmailRun = strconv.FormatInt(time.Now().UnixNano(), 36)

func gmailScope(label string) Scope {
	return Scope{
		TenantID: "tenant-gmail-" + label + "-" + gmailRun,
		OrgID:    "org-gmail-" + label + "-" + gmailRun,
	}
}

// gmailFixture seeds the CRM records a synced message can attach to. Sync
// never invents a subject: a message only becomes a timeline entry because it
// matched something that already existed.
func gmailFixture(t *testing.T, s Store, scope Scope) (*Account, *Contact) {
	t.Helper()
	account, err := s.CreateAccount(context.Background(), scope, &Account{
		Name: "Acme " + gmailRun, OwnerID: "owner-1",
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return account, gmailContact(t, s, scope, account, "buyer")
}

func gmailContact(t *testing.T, s Store, scope Scope, account *Account, local string) *Contact {
	t.Helper()
	contact, err := s.CreateContact(context.Background(), scope, &Contact{
		Email: local + "-" + gmailRun + "@acme.example", AccountID: account.ID,
		FirstName: "Bea", LastName: "Buyer",
	})
	if err != nil {
		t.Fatalf("seed contact %s: %v", local, err)
	}
	return contact
}

func gmailEntry(owner, messageID string, contact *Contact, at time.Time) (*GmailMessage, *Activity) {
	activityID := newID()
	msg := &GmailMessage{
		OwnerID:           owner,
		ProviderMessageID: messageID,
		SubjectType:       SubjectContact,
		SubjectID:         contact.ID,
		ThreadID:          "thread-" + messageID,
		ActivityID:        activityID,
		MessageSubject:    "Renewal terms",
		FromAddress:       contact.Email,
		Direction:         GmailInbound,
		OccurredAt:        at.UTC().Truncate(storedTimePrecision),
	}
	activity := &Activity{
		ID:          activityID,
		Kind:        GmailActivityKind,
		SubjectType: SubjectContact,
		SubjectID:   contact.ID,
		Notes:       "Email: Renewal terms",
		ActorID:     owner,
		OccurredAt:  at.UTC().Truncate(storedTimePrecision),
	}
	return msg, activity
}

// TestGmailTimelineEntryIsWrittenOnce is the idempotency contract. Sync
// re-reads messages routinely — a resumed backfill, an overlapping history
// window, a retried pass — so writing an entry twice is not a rare edge.
func TestGmailTimelineEntryIsWrittenOnce(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("once-" + name)
		_, contact := gmailFixture(t, s, scope)

		msg, activity := gmailEntry("user-1", "msg-once", contact, time.Now())
		created, err := s.RecordGmailActivity(ctx, scope, msg, activity)
		if err != nil {
			t.Fatalf("first record: %v", err)
		}
		if !created {
			t.Fatal("the first record of a message reported that it already existed")
		}

		// The same message again, with a different activity id — which is
		// what a second pass would generate.
		again, activityAgain := gmailEntry("user-1", "msg-once", contact, time.Now())
		created, err = s.RecordGmailActivity(ctx, scope, again, activityAgain)
		if err != nil {
			t.Fatalf("second record: %v", err)
		}
		if created {
			t.Error("re-syncing a message posted a second timeline entry")
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("read timeline: %v", err)
		}
		if len(activities) != 1 {
			t.Errorf("contact timeline holds %d entries for one email, want 1", len(activities))
		}
	})
}

// TestConcurrentSyncPassesPostOneEntry is the same property under the access
// pattern that produces it: two replicas whose leases overlapped, or a pass
// retried while the first was still running.
func TestConcurrentSyncPassesPostOneEntry(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("race-" + name)
		_, contact := gmailFixture(t, s, scope)

		const racers = 8
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			created int
		)
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				msg, activity := gmailEntry("user-1", "msg-race", contact, time.Now())
				ok, err := s.RecordGmailActivity(ctx, scope, msg, activity)
				if err == nil && ok {
					mu.Lock()
					created++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if created != 1 {
			t.Errorf("%d of %d concurrent passes posted the entry, want exactly 1", created, racers)
		}
		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("read timeline: %v", err)
		}
		if len(activities) != 1 {
			t.Errorf("contact timeline holds %d entries, want 1", len(activities))
		}
	})
}

// TestOneMessageReachesEveryMatchedRecord: an email addressed to two Contacts
// belongs on both timelines, and each of those entries is separately
// idempotent.
func TestOneMessageReachesEveryMatchedRecord(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("fanout-" + name)
		account, first := gmailFixture(t, s, scope)
		second := gmailContact(t, s, scope, account, "second")

		for _, contact := range []*Contact{first, second} {
			msg, activity := gmailEntry("user-1", "msg-fanout", contact, time.Now())
			created, err := s.RecordGmailActivity(ctx, scope, msg, activity)
			if err != nil {
				t.Fatalf("record for %s: %v", contact.ID, err)
			}
			if !created {
				t.Errorf("the entry for contact %s was treated as a duplicate of the other's", contact.ID)
			}
		}

		for _, contact := range []*Contact{first, second} {
			entries, err := s.ListGmailMessagesForSubject(ctx, scope, SubjectContact, contact.ID, 0)
			if err != nil {
				t.Fatalf("list for %s: %v", contact.ID, err)
			}
			if len(entries) != 1 {
				t.Errorf("contact %s has %d entries, want 1", contact.ID, len(entries))
			}
		}
	})
}

// TestGmailEntryRequiresARealSubjectInThisTenant. Sync matches participants
// to Contacts, and a matcher that could name a record in another tenant would
// post a customer's correspondence onto a stranger's timeline.
func TestGmailEntryRequiresARealSubjectInThisTenant(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := gmailScope("subject-victim-" + name)
		attacker := gmailScope("subject-attacker-" + name)
		_, contact := gmailFixture(t, s, victim)

		msg, activity := gmailEntry("user-1", "msg-cross", contact, time.Now())
		if _, err := s.RecordGmailActivity(ctx, attacker, msg, activity); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cross-tenant subject: %v, want ErrInvalid", err)
		}

		msg, activity = gmailEntry("user-1", "msg-missing", contact, time.Now())
		msg.SubjectID, activity.SubjectID = "contact-that-does-not-exist", "contact-that-does-not-exist"
		if _, err := s.RecordGmailActivity(ctx, victim, msg, activity); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown subject: %v, want ErrInvalid", err)
		}
	})
}

// TestGmailMessagesAreTenantScoped: correspondence metadata is exactly the
// kind of row a cross-tenant read must never return.
func TestGmailMessagesAreTenantScoped(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := gmailScope("msg-victim-" + name)
		attacker := gmailScope("msg-attacker-" + name)
		_, contact := gmailFixture(t, s, victim)

		msg, activity := gmailEntry("user-1", "msg-private", contact, time.Now())
		if _, err := s.RecordGmailActivity(ctx, victim, msg, activity); err != nil {
			t.Fatalf("seed: %v", err)
		}

		entries, err := s.ListGmailMessagesForSubject(ctx, attacker, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list as attacker: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("another tenant read %d correspondence rows", len(entries))
		}
		if _, err := s.GetGmailSyncState(ctx, attacker, "user-1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-tenant sync state read: %v, want ErrNotFound", err)
		}
	})
}

// TestGmailLeaseAdmitsOnePass. Without this two replicas walk the same
// mailbox at once: correct, thanks to the entry keys, but at twice the Gmail
// quota and with every write contended.
func TestGmailLeaseAdmitsOnePass(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("lease-" + name)
		now := time.Now().UTC()

		if _, err := s.ClaimGmailSync(ctx, scope, "user-1", now, time.Minute); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if _, err := s.ClaimGmailSync(ctx, scope, "user-1", now, time.Minute); !errors.Is(err, ErrConflict) {
			t.Fatalf("second claim: %v, want ErrConflict", err)
		}
		// The lease expires rather than needing release, so a replica that
		// died mid-pass cannot make the mailbox permanently unsyncable.
		if _, err := s.ClaimGmailSync(ctx, scope, "user-1", now.Add(2*time.Minute), time.Minute); err != nil {
			t.Fatalf("claim after the lease expired: %v", err)
		}
	})
}

// TestConcurrentClaimsAdmitOneWinner is the lease under contention.
func TestConcurrentClaimsAdmitOneWinner(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("lease-race-" + name)
		now := time.Now().UTC()

		const racers = 8
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners int
		)
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				if _, err := s.ClaimGmailSync(ctx, scope, "user-1", now, time.Minute); err == nil {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if winners != 1 {
			t.Errorf("%d of %d concurrent claims succeeded, want exactly 1", winners, racers)
		}
	})
}

// TestSyncStateRoundTripsEveryCursorField is the round-trip test that the
// Google connection needed and did not have: a field the schema does not
// store survives in MemStore and vanishes against PostgreSQL. Losing a
// backfill page token silently restarts a large mailbox from the beginning
// on every pass, forever.
func TestSyncStateRoundTripsEveryCursorField(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("cursor-" + name)
		now := time.Now().UTC().Truncate(storedTimePrecision)

		want := &GmailSyncState{
			OwnerID:           "user-1",
			LastHistoryID:     "987654321",
			BackfillPageToken: "page-token-abc",
			BackfillComplete:  true,
			SyncingUntil:      now.Add(time.Minute),
			LastSyncedAt:      now,
			LastMessageAt:     now.Add(-time.Hour),
			MessagesIndexed:   42,
			ActivitiesCreated: 17,
			LastError:         "history_expired",
		}
		if _, err := s.SaveGmailSyncState(ctx, scope, want); err != nil {
			t.Fatalf("save: %v", err)
		}
		got, err := s.GetGmailSyncState(ctx, scope, "user-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}

		if got.LastHistoryID != want.LastHistoryID {
			t.Errorf("LastHistoryID = %q, want %q — incremental sync would restart", got.LastHistoryID, want.LastHistoryID)
		}
		if got.BackfillPageToken != want.BackfillPageToken {
			t.Errorf("BackfillPageToken = %q, want %q — a large mailbox would never finish backfilling",
				got.BackfillPageToken, want.BackfillPageToken)
		}
		if got.BackfillComplete != want.BackfillComplete {
			t.Errorf("BackfillComplete = %v, want %v", got.BackfillComplete, want.BackfillComplete)
		}
		if !got.LastSyncedAt.Equal(want.LastSyncedAt) {
			t.Errorf("LastSyncedAt = %v, want %v", got.LastSyncedAt, want.LastSyncedAt)
		}
		if !got.LastMessageAt.Equal(want.LastMessageAt) {
			t.Errorf("LastMessageAt = %v, want %v", got.LastMessageAt, want.LastMessageAt)
		}
		if !got.SyncingUntil.Equal(want.SyncingUntil) {
			t.Errorf("SyncingUntil = %v, want %v", got.SyncingUntil, want.SyncingUntil)
		}
		if got.MessagesIndexed != want.MessagesIndexed || got.ActivitiesCreated != want.ActivitiesCreated {
			t.Errorf("counters = %d/%d, want %d/%d",
				got.MessagesIndexed, got.ActivitiesCreated, want.MessagesIndexed, want.ActivitiesCreated)
		}
		if got.LastError != want.LastError {
			t.Errorf("LastError = %q, want %q", got.LastError, want.LastError)
		}
	})
}

// TestPurgeErasesTheIndexAndKeepsTheHistory. Disconnecting a mailbox stops
// future sync and erases what sync used; it does not retroactively delete
// customer correspondence the organization has since worked from.
func TestPurgeErasesTheIndexAndKeepsTheHistory(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("purge-" + name)
		_, contact := gmailFixture(t, s, scope)

		msg, activity := gmailEntry("user-1", "msg-purge", contact, time.Now())
		if _, err := s.RecordGmailActivity(ctx, scope, msg, activity); err != nil {
			t.Fatalf("seed entry: %v", err)
		}
		if _, err := s.SaveGmailSyncState(ctx, scope, &GmailSyncState{
			OwnerID: "user-1", LastHistoryID: "1", BackfillComplete: true,
		}); err != nil {
			t.Fatalf("seed state: %v", err)
		}

		// Disconnecting drops the cursor but keeps the index. That is what
		// makes reconnecting safe: without the index, a re-sync would post a
		// second copy of every email to every timeline.
		removed, err := s.PurgeGmailDataForOwner(ctx, scope, "user-1", false)
		if err != nil {
			t.Fatalf("clear cursor: %v", err)
		}
		if removed != 0 {
			t.Errorf("clearing the cursor removed %d index rows, want 0 — "+
				"reconnecting would then duplicate every timeline entry", removed)
		}
		if _, err := s.GetGmailSyncState(ctx, scope, "user-1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("sync state survived: %v", err)
		}
		entries, err := s.ListGmailMessagesForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list entries: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("the deduplication index holds %d rows after a disconnect, want 1", len(entries))
		}

		// Full erasure removes the index as well.
		removed, err = s.PurgeGmailDataForOwner(ctx, scope, "user-1", true)
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if removed != 1 {
			t.Errorf("purged %d index rows, want 1", removed)
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("read timeline: %v", err)
		}
		if len(activities) != 1 {
			t.Errorf("purging the mailbox deleted %d timeline entries the CRM had already recorded",
				1-len(activities))
		}
	})
}

// TestGmailStoreRejectsAnEmptyScope covers the code path where a caller
// forgets the tenant — the shape of the bug that turns a scoped query global.
func TestGmailStoreRejectsAnEmptyScope(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		var none Scope
		now := time.Now().UTC()

		if _, err := s.GetGmailSyncState(ctx, none, "u"); !errors.Is(err, ErrNoTenant) {
			t.Errorf("GetGmailSyncState: %v, want ErrNoTenant", err)
		}
		if _, err := s.SaveGmailSyncState(ctx, none, &GmailSyncState{OwnerID: "u"}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("SaveGmailSyncState: %v, want ErrNoTenant", err)
		}
		if _, err := s.ClaimGmailSync(ctx, none, "u", now, time.Minute); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ClaimGmailSync: %v, want ErrNoTenant", err)
		}
		if _, err := s.RecordGmailActivity(ctx, none, &GmailMessage{}, &Activity{}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("RecordGmailActivity: %v, want ErrNoTenant", err)
		}
		if _, err := s.ListGmailMessagesForSubject(ctx, none, SubjectContact, "c", 0); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListGmailMessagesForSubject: %v, want ErrNoTenant", err)
		}
		if _, err := s.PurgeGmailDataForOwner(ctx, none, "u", true); !errors.Is(err, ErrNoTenant) {
			t.Errorf("PurgeGmailDataForOwner: %v, want ErrNoTenant", err)
		}
	})
}

// TestDueGmailMailboxesReturnsOnlyIdentifiers pins the third cross-tenant
// read this system has.
func TestDueGmailMailboxesReturnsOnlyIdentifiers(t *testing.T) {
	eachGmailStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := gmailScope("due-" + name)
		now := time.Now().UTC()

		if _, err := s.SaveGmailSyncState(ctx, scope, &GmailSyncState{OwnerID: "user-due"}); err != nil {
			t.Fatalf("seed: %v", err)
		}

		refs, err := s.DueGmailMailboxes(ctx, now, 500)
		if err != nil {
			t.Fatalf("due mailboxes: %v", err)
		}
		found := false
		for _, ref := range refs {
			if ref.TenantID == scope.TenantID && ref.OwnerID == "user-due" {
				found = true
			}
		}
		if !found {
			t.Error("a mailbox that has never synced was not reported as due")
		}

		// A claimed mailbox drops out until its lease expires.
		if _, err := s.ClaimGmailSync(ctx, scope, "user-due", now, time.Hour); err != nil {
			t.Fatalf("claim: %v", err)
		}
		refs, err = s.DueGmailMailboxes(ctx, now, 500)
		if err != nil {
			t.Fatalf("due mailboxes after claim: %v", err)
		}
		for _, ref := range refs {
			if ref.TenantID == scope.TenantID && ref.OwnerID == "user-due" {
				t.Error("a mailbox under a live lease was offered to a second replica")
			}
		}
	})
}
