package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Erasure and suppression, against both stores.
//
// The property that matters most is the one a plain DELETE would fail: an
// erased message must not come back. That needs the tombstone to survive
// while its content does not, and it is tested by re-running the write that
// created it.

func eachErasureStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

func erasureScope(label string) Scope {
	return Scope{
		TenantID: "tenant-erase-" + label + "-" + gmailRun,
		OrgID:    "org-erase-" + label + "-" + gmailRun,
	}
}

// seedSyncedContent puts one email and one meeting on a contact's timeline.
func seedSyncedContent(t *testing.T, s Store, scope Scope, contact *Contact, at time.Time) {
	t.Helper()
	ctx := context.Background()

	msg, activity := gmailEntry("user-1", "erase-msg", contact, at)
	if _, err := s.RecordGmailActivity(ctx, scope, msg, activity); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	event := calendarEvent("user-1", "erase-evt", contact, at)
	if _, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event)); err != nil {
		t.Fatalf("seed meeting: %v", err)
	}
}

// TestErasureRemovesContentAndLeavesADeduplicationTombstone is the core of
// the feature. Deleting the index rows outright would make the next poll
// re-create everything it had just removed.
func TestErasureRemovesContentAndLeavesADeduplicationTombstone(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("core-" + name)
		_, contact := gmailFixture(t, s, scope)
		at := time.Now().UTC().Add(-time.Hour)
		seedSyncedContent(t, s, scope, contact, at)

		result, err := s.EraseSyncedContent(ctx, scope, EraseSubject{
			SubjectType: SubjectContact, SubjectID: contact.ID,
			Actor: "admin-1", Reason: "erasure request",
			SuppressAddresses: []string{contact.Email},
		})
		if err != nil {
			t.Fatalf("erase: %v", err)
		}
		if result.ActivitiesDeleted != 2 {
			t.Errorf("deleted %d activities, want 2 (one email, one meeting)", result.ActivitiesDeleted)
		}
		if result.MessagesRedacted != 1 || result.EventsRedacted != 1 {
			t.Errorf("redacted %d messages and %d events, want 1 and 1",
				result.MessagesRedacted, result.EventsRedacted)
		}
		if result.AddressesSuppressed != 1 {
			t.Errorf("suppressed %d addresses, want 1", result.AddressesSuppressed)
		}

		// The timeline is empty.
		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 0 {
			t.Errorf("timeline still holds %d entries after erasure", len(activities))
		}

		// The index rows survive as tombstones, with nothing readable left.
		messages, err := s.ListGmailMessagesForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		if len(messages) != 1 {
			t.Fatalf("index holds %d rows, want 1 tombstone", len(messages))
		}
		if messages[0].MessageSubject != "" || messages[0].FromAddress != "" {
			t.Errorf("the tombstone still carries content: %+v", messages[0])
		}
		if messages[0].ErasedAt.IsZero() {
			t.Error("the tombstone is not marked erased")
		}
		if messages[0].ProviderMessageID == "" {
			t.Error("the tombstone lost its provider id, so deduplication no longer works")
		}

		events, err := s.ListCalendarEventsForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("mirror holds %d rows, want 1 tombstone", len(events))
		}
		if events[0].Title != "" || events[0].OrganizerAddress != "" {
			t.Errorf("the meeting tombstone still carries content: %+v", events[0])
		}
	})
}

// TestErasedContentDoesNotComeBack is the same property from the other side:
// re-running the write that created the entry must find the tombstone and do
// nothing.
func TestErasedContentDoesNotComeBack(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("return-" + name)
		_, contact := gmailFixture(t, s, scope)
		at := time.Now().UTC().Add(-time.Hour)
		seedSyncedContent(t, s, scope, contact, at)

		if _, err := s.EraseSyncedContent(ctx, scope, EraseSubject{
			SubjectType: SubjectContact, SubjectID: contact.ID, Actor: "admin-1",
		}); err != nil {
			t.Fatalf("erase: %v", err)
		}

		// Exactly what the next sync pass would do.
		msg, activity := gmailEntry("user-1", "erase-msg", contact, at)
		created, err := s.RecordGmailActivity(ctx, scope, msg, activity)
		if err != nil {
			t.Fatalf("re-record message: %v", err)
		}
		if created {
			t.Error("a re-sync re-created an erased email")
		}

		// And the meeting, including the case that would otherwise resurrect
		// it: Google reports the same event rescheduled.
		moved := calendarEvent("user-1", "erase-evt", contact, at.Add(3*time.Hour))
		change, err := s.RecordCalendarEvent(ctx, scope, moved, activityForEvent(moved))
		if err != nil {
			t.Fatalf("re-record meeting: %v", err)
		}
		if change != CalendarUnchanged {
			t.Errorf("a rescheduled erased meeting posted %v, resurrecting content that was erased", change)
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 0 {
			t.Errorf("erased content returned to the timeline: %d entries", len(activities))
		}

		// The tombstone must also stay redacted — a resurrecting update that
		// refreshed the title would put the content back in the mirror even
		// if it posted nothing.
		events, err := s.ListCalendarEventsForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		if events[0].Title != "" {
			t.Errorf("a rescheduled erased meeting refreshed the mirror title to %q", events[0].Title)
		}
	})
}

// TestErasureNeverTouchesWhatAPersonAuthored. A note somebody typed about a
// customer is this organization's own record. Deleting it because a third
// party asked would destroy business records under cover of a privacy
// request.
func TestErasureNeverTouchesWhatAPersonAuthored(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("authored-" + name)
		_, contact := gmailFixture(t, s, scope)
		at := time.Now().UTC().Add(-time.Hour)
		seedSyncedContent(t, s, scope, contact, at)

		handLogged, err := s.CreateActivity(ctx, scope, &Activity{
			Kind: "call", SubjectType: SubjectContact, SubjectID: contact.ID,
			Notes: "Called to discuss renewal", ActorID: "rep-1", OccurredAt: at,
		})
		if err != nil {
			t.Fatalf("log a call by hand: %v", err)
		}

		if _, err := s.EraseSyncedContent(ctx, scope, EraseSubject{
			SubjectType: SubjectContact, SubjectID: contact.ID, Actor: "admin-1",
		}); err != nil {
			t.Fatalf("erase: %v", err)
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 1 || activities[0].ID != handLogged.ID {
			t.Fatalf("timeline = %+v, want only the hand-logged call", activities)
		}
		if activities[0].Notes != "Called to discuss renewal" {
			t.Error("the hand-logged call was altered by an erasure")
		}
	})
}

// TestSuppressionStopsTheMatcher: the store half of "erasure that does not
// stop collection is theatre". The sync half is tested in the sync packages.
func TestSuppressionStopsTheMatcher(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("suppress-" + name)

		if err := s.SuppressSyncAddress(ctx, scope, &SyncSuppression{
			Address: "  Ada@Acme.Example  ", Reason: "erasure request", CreatedBy: "admin-1",
		}); err != nil {
			t.Fatalf("suppress: %v", err)
		}

		suppressed, err := s.SuppressedSyncAddresses(ctx, scope)
		if err != nil {
			t.Fatalf("read suppressions: %v", err)
		}
		// Normalized on the way in, because the matcher normalizes too. Two
		// normalizations that disagreed by a capital letter would make
		// suppression silently stop working.
		if !suppressed["ada@acme.example"] {
			t.Errorf("suppression set = %v, want the normalized address", suppressed)
		}

		rows, err := s.ListSyncSuppressions(ctx, scope)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(rows) != 1 || rows[0].Reason != "erasure request" || rows[0].CreatedBy != "admin-1" {
			t.Errorf("suppressions = %+v", rows)
		}

		if err := s.ReleaseSyncAddress(ctx, scope, "ADA@ACME.EXAMPLE"); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := s.ReleaseSyncAddress(ctx, scope, "ada@acme.example"); !errors.Is(err, ErrNotFound) {
			t.Errorf("releasing twice: %v, want ErrNotFound", err)
		}
	})
}

// TestRetentionErasesOnlyWhatIsOldEnough.
func TestRetentionErasesOnlyWhatIsOldEnough(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("retention-" + name)
		_, contact := gmailFixture(t, s, scope)
		now := time.Now().UTC()

		old, oldActivity := gmailEntry("user-1", "old-msg", contact, now.Add(-100*24*time.Hour))
		if _, err := s.RecordGmailActivity(ctx, scope, old, oldActivity); err != nil {
			t.Fatalf("seed old: %v", err)
		}
		recent, recentActivity := gmailEntry("user-1", "recent-msg", contact, now.Add(-time.Hour))
		if _, err := s.RecordGmailActivity(ctx, scope, recent, recentActivity); err != nil {
			t.Fatalf("seed recent: %v", err)
		}

		result, err := s.EraseSyncedContent(ctx, scope, EraseSubject{
			OwnerID: "user-1", Before: now.Add(-90 * 24 * time.Hour), Actor: "system",
		})
		if err != nil {
			t.Fatalf("retention sweep: %v", err)
		}
		if result.MessagesRedacted != 1 || result.ActivitiesDeleted != 1 {
			t.Errorf("swept %d messages and %d activities, want 1 and 1",
				result.MessagesRedacted, result.ActivitiesDeleted)
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 1 {
			t.Fatalf("timeline holds %d entries, want the recent one only", len(activities))
		}
		if activities[0].ID != recentActivity.ID {
			t.Error("the retention sweep erased the recent message and kept the old one")
		}
	})
}

// TestErasingTwiceIsANoOp. An operator repeating a request, or a retention
// sweep running every pass, must not report work it did not do.
func TestErasingTwiceIsANoOp(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("twice-" + name)
		_, contact := gmailFixture(t, s, scope)
		seedSyncedContent(t, s, scope, contact, time.Now().UTC().Add(-time.Hour))

		in := EraseSubject{SubjectType: SubjectContact, SubjectID: contact.ID, Actor: "admin-1"}
		first, err := s.EraseSyncedContent(ctx, scope, in)
		if err != nil {
			t.Fatalf("first erase: %v", err)
		}
		if first.Total() == 0 {
			t.Fatal("the first erasure did nothing")
		}
		second, err := s.EraseSyncedContent(ctx, scope, in)
		if err != nil {
			t.Fatalf("second erase: %v", err)
		}
		if second.Total() != 0 {
			t.Errorf("a repeated erasure reported %+v, want nothing left to do", second)
		}
	})
}

// TestAnUnscopedErasureIsRefused. The difference between "erase this contact"
// and "erase every synced record in the tenant" is one unset field.
func TestAnUnscopedErasureIsRefused(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("unscoped-" + name)

		if _, err := s.EraseSyncedContent(ctx, scope, EraseSubject{Actor: "admin-1"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("an erasure naming nothing: %v, want ErrInvalid", err)
		}
		// Half a subject is refused too: a type with no id would otherwise
		// match every record of that type.
		if _, err := s.EraseSyncedContent(ctx, scope, EraseSubject{
			SubjectType: SubjectContact, Actor: "admin-1",
		}); !errors.Is(err, ErrInvalid) {
			t.Errorf("an erasure naming a type but no id: %v, want ErrInvalid", err)
		}
	})
}

// TestErasureIsTenantScoped: one organization must not be able to destroy
// another's records, or read whose addresses are suppressed.
func TestErasureIsTenantScoped(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := erasureScope("erase-victim-" + name)
		attacker := erasureScope("erase-attacker-" + name)
		_, contact := gmailFixture(t, s, victim)
		seedSyncedContent(t, s, victim, contact, time.Now().UTC().Add(-time.Hour))

		// The attacker names the victim's contact id directly.
		result, err := s.EraseSyncedContent(ctx, attacker, EraseSubject{
			SubjectType: SubjectContact, SubjectID: contact.ID, Actor: "attacker",
		})
		if err != nil {
			t.Fatalf("cross-tenant erase: %v", err)
		}
		if result.Total() != 0 {
			t.Errorf("a cross-tenant erasure destroyed %+v", result)
		}
		activities, err := s.ListActivitiesForSubject(ctx, victim, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("victim timeline: %v", err)
		}
		if len(activities) != 2 {
			t.Errorf("the victim's timeline holds %d entries, want 2 — a cross-tenant erasure landed",
				len(activities))
		}

		// Suppression lists do not cross either.
		if err := s.SuppressSyncAddress(ctx, victim, &SyncSuppression{
			Address: "private@victim.example",
		}); err != nil {
			t.Fatalf("seed suppression: %v", err)
		}
		suppressed, err := s.SuppressedSyncAddresses(ctx, attacker)
		if err != nil {
			t.Fatalf("read as attacker: %v", err)
		}
		if suppressed["private@victim.example"] {
			t.Error("one organization's suppression list is visible to another")
		}
	})
}

// TestErasureRejectsAnEmptyScope.
func TestErasureRejectsAnEmptyScope(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		var none Scope

		if _, err := s.EraseSyncedContent(ctx, none, EraseSubject{OwnerID: "u"}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("EraseSyncedContent: %v, want ErrNoTenant", err)
		}
		if err := s.SuppressSyncAddress(ctx, none, &SyncSuppression{Address: "a@b.example"}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("SuppressSyncAddress: %v, want ErrNoTenant", err)
		}
		if err := s.ReleaseSyncAddress(ctx, none, "a@b.example"); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ReleaseSyncAddress: %v, want ErrNoTenant", err)
		}
		if _, err := s.SuppressedSyncAddresses(ctx, none); !errors.Is(err, ErrNoTenant) {
			t.Errorf("SuppressedSyncAddresses: %v, want ErrNoTenant", err)
		}
		if _, err := s.ListSyncSuppressions(ctx, none); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListSyncSuppressions: %v, want ErrNoTenant", err)
		}
	})
}

// TestASuppressionMustBeAnAddress: a blank or malformed entry would silently
// match nothing, which looks the same as working.
func TestASuppressionMustBeAnAddress(t *testing.T) {
	eachErasureStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := erasureScope("bad-suppress-" + name)

		for _, address := range []string{"", "   ", "not-an-address"} {
			if err := s.SuppressSyncAddress(ctx, scope, &SyncSuppression{
				Address: address,
			}); !errors.Is(err, ErrInvalid) {
				t.Errorf("suppressing %q: %v, want ErrInvalid", address, err)
			}
		}
	})
}
