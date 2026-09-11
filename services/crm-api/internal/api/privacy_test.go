package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
)

// The data-subject RPCs at the handler boundary.

// TestErasureIsServableWithoutAConfiguredIntegration. A deployment that
// switched Google off still holds what it collected, and "we cannot delete
// your data because the feature that collected it is disabled" is not an
// answer anyone can give.
func TestErasureIsServableWithoutAConfiguredIntegration(t *testing.T) {
	store := domain.NewMemStore()
	// Deliberately nil for every integration, including privacy.
	s := NewCRMService(config.Config{}, store, nil, nil, nil, nil, nil, nil, nil)
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	account, err := store.CreateAccount(context.Background(), scope,
		&domain.Account{Name: "Acme", OwnerID: "o"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(context.Background(), scope, &domain.Contact{
		Email: "ada@acme.example", AccountID: account.ID, FirstName: "Ada", LastName: "A",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	resp, err := s.EraseSyncedContentForContact(ctx, connect.NewRequest(
		&crmv1.EraseSyncedContentForContactRequest{ContactId: contact.ID, Reason: "request"}))
	if err != nil {
		t.Fatalf("erase with no integration configured: %v", err)
	}
	if resp.Msg.GetResult().GetAddressesSuppressed() != 1 {
		t.Errorf("the contact's address was not suppressed: %+v", resp.Msg.GetResult())
	}
}

// TestErasureReportsCountsAndNoContent.
func TestErasureReportsCountsAndNoContent(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	account, err := store.CreateAccount(context.Background(), scope,
		&domain.Account{Name: "Acme", OwnerID: "o"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(context.Background(), scope, &domain.Contact{
		Email: "ada@acme.example", AccountID: account.ID, FirstName: "Ada", LastName: "A",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	activityID := domain.NewID()
	if _, err := store.RecordGmailActivity(context.Background(), scope, &domain.GmailMessage{
		OwnerID: "user-1", ProviderMessageID: "m-1",
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		ActivityID: activityID, MessageSubject: "Confidential renewal terms",
		FromAddress: contact.Email, Direction: domain.GmailInbound,
		OccurredAt: time.Now().UTC(),
	}, &domain.Activity{
		ID: activityID, Kind: domain.GmailActivityKind,
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		Notes: "Email received: Confidential renewal terms", ActorID: "user-1",
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	resp, err := s.EraseSyncedContentForContact(ctx, connect.NewRequest(
		&crmv1.EraseSyncedContentForContactRequest{ContactId: contact.ID}))
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	result := resp.Msg.GetResult()
	if result.GetActivitiesDeleted() != 1 || result.GetMessagesRedacted() != 1 {
		t.Errorf("result = %+v", result)
	}
	// The response says what was removed, never what it said.
	if strings.Contains(resp.Msg.String(), "Confidential") {
		t.Errorf("the erasure response quotes the content it erased: %q", resp.Msg.String())
	}
}

// TestSuppressionRoundTripsThroughTheRPCs.
func TestSuppressionRoundTripsThroughTheRPCs(t *testing.T) {
	s, _ := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")

	if _, err := s.SuppressSyncAddress(ctx, connect.NewRequest(&crmv1.SuppressSyncAddressRequest{
		Address: "Ada@Acme.Example", Reason: "asked not to be tracked",
	})); err != nil {
		t.Fatalf("suppress: %v", err)
	}

	list, err := s.ListSyncSuppressions(ctx, connect.NewRequest(&crmv1.ListSyncSuppressionsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	rows := list.Msg.GetSuppressions()
	if len(rows) != 1 || rows[0].GetAddress() != "ada@acme.example" {
		t.Fatalf("suppressions = %+v, want the normalized address", rows)
	}
	if rows[0].GetCreatedBy() != principalSubject(ctx) {
		t.Errorf("created_by = %q, want the calling principal", rows[0].GetCreatedBy())
	}

	if _, err := s.ReleaseSyncAddress(ctx, connect.NewRequest(
		&crmv1.ReleaseSyncAddressRequest{Address: "ada@acme.example"})); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := s.ReleaseSyncAddress(ctx, connect.NewRequest(
		&crmv1.ReleaseSyncAddressRequest{Address: "ada@acme.example"})); err == nil {
		t.Error("releasing an address twice reported success")
	} else if got := connectCode(t, err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestSuppressingSomethingThatIsNotAnAddressIsRejected: a malformed entry
// would silently match nothing, which looks exactly like working.
func TestSuppressingSomethingThatIsNotAnAddressIsRejected(t *testing.T) {
	s, _ := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")

	_, err := s.SuppressSyncAddress(ctx, connect.NewRequest(
		&crmv1.SuppressSyncAddressRequest{Address: "everyone"}))
	if err == nil {
		t.Fatal("a malformed suppression was accepted")
	}
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}
