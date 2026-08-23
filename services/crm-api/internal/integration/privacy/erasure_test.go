package privacy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The privacy service: the three writes plus the audit event that a
// data-subject request has to be, done together.

func fixture(t *testing.T) (domain.Store, domain.Scope, *domain.Contact) {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "o"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(ctx, scope, &domain.Contact{
		Email: "ada@acme.example", AccountID: account.ID, FirstName: "Ada", LastName: "A",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return store, scope, contact
}

func seedMessage(t *testing.T, store domain.Store, scope domain.Scope, contact *domain.Contact, id string, at time.Time) {
	t.Helper()
	activityID := domain.NewID()
	if _, err := store.RecordGmailActivity(context.Background(), scope, &domain.GmailMessage{
		OwnerID: "user-1", ProviderMessageID: id,
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		ActivityID: activityID, MessageSubject: "Renewal terms", FromAddress: contact.Email,
		Direction: domain.GmailInbound, OccurredAt: at,
	}, &domain.Activity{
		ID: activityID, Kind: domain.GmailActivityKind,
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		Notes: "Email received: Renewal terms", ActorID: "user-1", OccurredAt: at,
	}); err != nil {
		t.Fatalf("seed message %s: %v", id, err)
	}
}

// TestErasingForAContactAlsoStopsCollecting is the whole point of routing
// this through a service rather than leaving it to call sites. Erasing
// without suppressing reads as success and is undone by the next poll.
func TestErasingForAContactAlsoStopsCollecting(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()
	seedMessage(t, store, scope, contact, "m-1", time.Now().UTC().Add(-time.Hour))

	result, err := New(store, 0, nil).EraseForContact(ctx, scope, contact.ID, "admin-1", "erasure request")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if result.ActivitiesDeleted != 1 {
		t.Errorf("deleted %d activities, want 1", result.ActivitiesDeleted)
	}
	if result.AddressesSuppressed != 1 {
		t.Fatalf("suppressed %d addresses, want 1 — the next poll would re-index this person",
			result.AddressesSuppressed)
	}

	suppressed, err := store.SuppressedSyncAddresses(ctx, scope)
	if err != nil {
		t.Fatalf("read suppressions: %v", err)
	}
	if !suppressed["ada@acme.example"] {
		t.Error("the contact's address was not suppressed")
	}
}

// TestAnErasureIsAudited. An erasure that leaves no trace cannot be shown to
// have happened, which is the first thing an auditor asks.
func TestAnErasureIsAudited(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()
	seedMessage(t, store, scope, contact, "m-1", time.Now().UTC().Add(-time.Hour))

	if _, err := New(store, 0, nil).EraseForContact(ctx, scope, contact.ID, "admin-1", "GDPR article 17"); err != nil {
		t.Fatalf("erase: %v", err)
	}

	events, err := store.OutboxEventsFor(ctx, scope, contact.ID)
	if err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	var found *domain.OutboxEvent
	for _, event := range events {
		if event.Envelope.Type == domain.EventSyncContentErased {
			found = event
		}
	}
	if found == nil {
		t.Fatal("the erasure recorded no audit event")
	}
	if found.Envelope.Actor != "admin-1" {
		t.Errorf("audit actor = %q, want the administrator who asked", found.Envelope.Actor)
	}

	// The audit record must not quote what it erased.
	payload := string(found.Envelope.Payload)
	if strings.Contains(payload, "Renewal terms") {
		t.Errorf("the audit event quotes the erased subject line: %s", payload)
	}
	if !strings.Contains(payload, "GDPR article 17") {
		t.Errorf("the audit event does not record why: %s", payload)
	}
	if !strings.Contains(payload, `"activities_deleted":1`) {
		t.Errorf("the audit event does not record what was removed: %s", payload)
	}
}

// TestNothingToEraseIsNotAudited, so the erasures that did something are not
// buried under a retention sweep's worth of empty records.
func TestNothingToEraseIsNotAudited(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()

	result, err := New(store, 0, nil).EraseForContact(ctx, scope, contact.ID, "admin-1", "")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	// The address is still suppressed, so this is not a no-op — but with no
	// content to remove there is nothing to audit beyond that.
	if result.ActivitiesDeleted != 0 {
		t.Errorf("deleted %d activities from an empty timeline", result.ActivitiesDeleted)
	}

	events, err := store.OutboxEventsFor(ctx, scope, contact.ID)
	if err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	// One event, because suppressing the address is itself something that
	// happened; a second call would record nothing.
	if len(events) != 1 {
		t.Fatalf("recorded %d audit events, want 1", len(events))
	}
	if _, err := New(store, 0, nil).EraseForContact(ctx, scope, contact.ID, "admin-1", ""); err != nil {
		t.Fatalf("second erase: %v", err)
	}
	events, err = store.OutboxEventsFor(ctx, scope, contact.ID)
	if err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("a repeated erasure with nothing to do recorded another audit event: %d", len(events))
	}
}

// TestRetentionIsOffByDefault. A CRM that silently deleted an organization's
// correspondence history after ninety days because nobody configured this
// would be losing records it was never told to lose.
func TestRetentionIsOffByDefault(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()
	seedMessage(t, store, scope, contact, "m-ancient", time.Now().UTC().Add(-10*365*24*time.Hour))

	service := New(store, 0, nil)
	if service.RetentionEnabled() {
		t.Fatal("retention is enabled with no window configured")
	}
	result, err := service.ApplyRetention(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("an unconfigured retention window erased %+v", result)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Error("a decade-old message was deleted with no retention window configured")
	}
}

// TestRetentionErasesWhatIsOlderThanTheWindow, and audits it as automatic so
// an auditor can tell it from a request.
func TestRetentionErasesWhatIsOlderThanTheWindow(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedMessage(t, store, scope, contact, "m-old", now.Add(-100*24*time.Hour))
	seedMessage(t, store, scope, contact, "m-recent", now.Add(-time.Hour))

	service := New(store, 90, nil)
	if service.RetentionDays() != 90 {
		t.Errorf("RetentionDays = %d, want 90", service.RetentionDays())
	}
	result, err := service.ApplyRetention(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("retention: %v", err)
	}
	if result.ActivitiesDeleted != 1 {
		t.Errorf("swept %d activities, want 1", result.ActivitiesDeleted)
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Fatalf("timeline holds %d entries, want the recent one", len(activities))
	}

	// Audited as automatic: "we deleted this because it aged out" is a
	// different fact from "we deleted this because we were asked".
	events, err := store.OutboxEventsFor(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded %d audit events, want 1", len(events))
	}
	if !strings.Contains(string(events[0].Envelope.Payload), `"automatic":true`) {
		t.Errorf("the retention sweep was not recorded as automatic: %s", events[0].Envelope.Payload)
	}
	if events[0].Envelope.Actor != "system" {
		t.Errorf("audit actor = %q, want system", events[0].Envelope.Actor)
	}
}

// TestErasingForAMailboxSuppressesNobody. "Undo what my mailbox put into this
// CRM" is not "stop tracking these people": suppressing every correspondent
// of a departing employee would quietly stop their colleagues' syncs from
// recording the same customers.
func TestErasingForAMailboxSuppressesNobody(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()
	seedMessage(t, store, scope, contact, "m-1", time.Now().UTC().Add(-time.Hour))

	result, err := New(store, 0, nil).EraseForMailbox(ctx, scope, "user-1", "admin-1", "employee left")
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if result.ActivitiesDeleted != 1 {
		t.Errorf("deleted %d activities, want 1", result.ActivitiesDeleted)
	}
	if result.AddressesSuppressed != 0 {
		t.Errorf("suppressed %d addresses; erasing a mailbox must not stop colleagues "+
			"from recording the same customers", result.AddressesSuppressed)
	}
}

// TestSuppressingWithoutErasing, for somebody who asks not to be tracked from
// now on but does not ask for history to go.
func TestSuppressingWithoutErasing(t *testing.T) {
	store, scope, contact := fixture(t)
	ctx := context.Background()
	seedMessage(t, store, scope, contact, "m-1", time.Now().UTC().Add(-time.Hour))

	service := New(store, 0, nil)
	if err := service.Suppress(ctx, scope, "Ada@Acme.Example", "admin-1", "asked not to be tracked"); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	suppressed, err := store.SuppressedSyncAddresses(ctx, scope)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !suppressed["ada@acme.example"] {
		t.Error("the address was not suppressed, or was not normalized")
	}
	// History is untouched, which is the difference from an erasure.
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 1 {
		t.Error("suppressing an address deleted history it was not asked to delete")
	}

	rows, err := service.List(ctx, scope)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Reason != "asked not to be tracked" {
		t.Errorf("suppressions = %+v", rows)
	}
	if err := service.Release(ctx, scope, "ada@acme.example"); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// TestErasingAnUnknownContactFails rather than silently erasing nothing,
// which would let a typo look like a completed request.
func TestErasingAnUnknownContactFails(t *testing.T) {
	store, scope, _ := fixture(t)
	if _, err := New(store, 0, nil).EraseForContact(
		context.Background(), scope, "contact-that-does-not-exist", "admin-1", ""); err == nil {
		t.Fatal("erasing an unknown contact reported success")
	}
}
