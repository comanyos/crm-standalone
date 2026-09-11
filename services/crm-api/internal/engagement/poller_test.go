package engagement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

type fakeDirectory struct {
	recipients []string
	sent       []string
	fail       bool
}

func (f *fakeDirectory) AlertRecipientIDs(
	_ context.Context, _ string, owner string, escalate bool,
) ([]string, error) {
	if len(f.recipients) > 0 {
		if escalate {
			return append([]string{owner}, f.recipients...), nil
		}
		if owner == "" {
			return append([]string(nil), f.recipients...), nil
		}
	}
	return []string{owner}, nil
}

func (f *fakeDirectory) SendUserEmail(
	_ context.Context, userID, _, _ string,
) error {
	f.sent = append(f.sent, userID)
	if f.fail {
		return errors.New("mail unavailable")
	}
	return nil
}

func TestRunOnceCreatesInstantAndSLAAlertsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "org-1", OrgID: "org-1"}
	lead, err := store.CreateLead(ctx, scope, &domain.Lead{
		FirstName: "Asha", OwnerID: "owner-1", Status: domain.LeadStatusNew,
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := &fakeDirectory{recipients: []string{"manager-1"}}
	poller := New(store, directory, 15*time.Minute, "https://crmapp.example", time.Second, nil)
	now := lead.CreatedAt.Add(20 * time.Minute)
	poller.now = func() time.Time { return now }

	if err := poller.RunOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := poller.RunOnce(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(directory.sent) != 3 {
		t.Fatalf("emails = %v, want initial owner plus owner/manager SLA", directory.sent)
	}
	ownerInbox, err := store.ListNotifications(ctx, scope, "owner-1", false, 20)
	if err != nil {
		t.Fatal(err)
	}
	managerInbox, err := store.ListNotifications(ctx, scope, "manager-1", false, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerInbox) != 2 || len(managerInbox) != 1 {
		t.Fatalf("owner/manager inbox sizes = %d/%d, want 2/1", len(ownerInbox), len(managerInbox))
	}
	if ownerInbox[0].SubjectID != lead.ID || managerInbox[0].SubjectID != lead.ID {
		t.Fatal("alert notification is not linked to its lead")
	}
}

func TestEmailDeliveryUsesDurableBackoff(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "org-1", OrgID: "org-1"}
	lead, err := store.CreateLead(ctx, scope, &domain.Lead{Company: "Retry Co", OwnerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	directory := &fakeDirectory{fail: true}
	poller := New(store, directory, time.Hour, "https://crmapp.example", time.Second, nil)
	now := lead.CreatedAt
	poller.now = func() time.Time { return now }

	if err := poller.RunOnce(ctx); err != nil {
		t.Fatalf("failed delivery should be recorded, not fail the pass: %v", err)
	}
	if len(directory.sent) != 1 {
		t.Fatalf("attempts = %d, want 1", len(directory.sent))
	}
	if err := poller.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(directory.sent) != 1 {
		t.Fatal("delivery retried before its durable backoff elapsed")
	}

	directory.fail = false
	now = now.Add(time.Minute)
	if err := poller.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(directory.sent) != 2 {
		t.Fatalf("attempts after backoff = %d, want 2", len(directory.sent))
	}
	if err := poller.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(directory.sent) != 2 {
		t.Fatal("a delivered notification was sent again")
	}
}
