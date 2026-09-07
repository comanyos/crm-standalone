package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// The drafting queue's own rule: consent is re-checked between drafting and
// sending, because that gap is where a withdrawal lands.

func seedDraft(t *testing.T, store domain.Store, scope domain.Scope, address string) *domain.OutboundMessage {
	t.Helper()
	draft, _, err := store.DraftOutboundMessage(context.Background(), scope, &domain.OutboundMessage{
		SubjectType: domain.SubjectLead, SubjectID: "lead-1",
		ChannelKind: domain.ChannelEmail, Address: address,
		Body: "Hi, about your enquiry.", Status: domain.OutboundPending,
		AssigneeID: "test-user",
	})
	if err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	return draft
}

// TestAWithdrawalAfterDraftingBlocksSending is the reason the queue re-checks
// rather than trusting the draft.
//
// A rule can draft on Monday and a rep can open the queue on Wednesday. If
// consent were only checked when the message was written, the CRM would hand
// somebody a ready-made message to a person who opted out in between — and it
// would look, to the rep, like the system had approved it.
func TestAWithdrawalAfterDraftingBlocksSending(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	draft := seedDraft(t, store, scope, "alex@example.com")

	// Permitted at first.
	res, err := s.ListOutboundMessages(ctx, connect.NewRequest(&crmv1.ListOutboundMessagesRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Msg.GetMessages()) != 1 || !res.Msg.GetMessages()[0].GetMayStillSend() {
		t.Fatalf("queue = %+v, want one sendable draft", res.Msg.GetMessages())
	}

	if _, err := store.RecordConsent(context.Background(), scope, &domain.MessagingConsent{
		ChannelKind: domain.ChannelEmail, Address: "alex@example.com",
		State: domain.ConsentWithdrawn, Source: domain.ConsentSourceInbound,
	}); err != nil {
		t.Fatalf("record withdrawal: %v", err)
	}

	res, err = s.ListOutboundMessages(ctx, connect.NewRequest(&crmv1.ListOutboundMessagesRequest{}))
	if err != nil {
		t.Fatalf("list after withdrawal: %v", err)
	}
	message := res.Msg.GetMessages()[0]
	if message.GetMayStillSend() {
		t.Error("a draft to somebody who opted out is still offered for sending")
	}
	if message.GetRefusalReason() != domain.RefusalConsentWithdrawn {
		t.Errorf("refusal = %q, want %q", message.GetRefusalReason(), domain.RefusalConsentWithdrawn)
	}
	// Still listed, deliberately: a draft that vanished would look to the rep
	// like the CRM lost it, and they would rewrite it by hand.
	if message.GetStatus() != domain.OutboundPending {
		t.Errorf("status = %q, want it still pending and visibly refused", message.GetStatus())
	}

	// And the button is refused too, not only greyed out — the list is a
	// read, and consent can change between rendering it and pressing send.
	_, err = s.MarkOutboundMessageSent(ctx, connect.NewRequest(&crmv1.MarkOutboundMessageSentRequest{
		Id: draft.ID,
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("marking sent: %v (code %v), want FailedPrecondition", err, connect.CodeOf(err))
	}
}

func TestMarkingSentRecordsWhoSentIt(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}
	draft := seedDraft(t, store, scope, "alex@example.com")

	sent, err := s.MarkOutboundMessageSent(ctx, connect.NewRequest(&crmv1.MarkOutboundMessageSentRequest{
		Id: draft.ID,
	}))
	if err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	// The actor comes from the token, never from the request: a client must
	// not be able to attribute a send to somebody else.
	if got := sent.Msg.GetMessage().GetSentBy(); got != principalSubject(ctx) {
		t.Errorf("sent_by = %q, want the authenticated principal %q", got, principalSubject(ctx))
	}
	if sent.Msg.GetMessage().GetStatus() != domain.OutboundSent {
		t.Errorf("status = %q, want %q", sent.Msg.GetMessage().GetStatus(), domain.OutboundSent)
	}

	// Two reps working the same queue must not both claim one draft.
	_, err = s.MarkOutboundMessageSent(ctx, connect.NewRequest(&crmv1.MarkOutboundMessageSentRequest{
		Id: draft.ID,
	}))
	// Aborted is this codebase's mapping for domain.ErrConflict — the same
	// code a concurrent optimistic-concurrency failure returns.
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("second claim: %v (code %v), want Aborted", err, connect.CodeOf(err))
	}
}

func TestDiscardingADraftLeavesItAuditable(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}
	draft := seedDraft(t, store, scope, "alex@example.com")

	res, err := s.DiscardOutboundMessage(ctx, connect.NewRequest(&crmv1.DiscardOutboundMessageRequest{
		Id: draft.ID,
	}))
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if res.Msg.GetMessage().GetStatus() != domain.OutboundDiscarded {
		t.Errorf("status = %q, want %q", res.Msg.GetMessage().GetStatus(), domain.OutboundDiscarded)
	}
	// Discarded rather than deleted: "an automation drafted this and somebody
	// decided not to send it" is a fact worth keeping.
	if res.Msg.GetMessage().GetDiscardReason() != domain.DiscardReasonManual {
		t.Errorf("reason = %q, want %q", res.Msg.GetMessage().GetDiscardReason(), domain.DiscardReasonManual)
	}
	if _, err := store.GetOutboundMessage(context.Background(), scope, draft.ID); err != nil {
		t.Errorf("a discarded draft is gone from the store: %v", err)
	}
}

func TestTheQueueCanBeFilteredToMine(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	seedDraft(t, store, scope, "mine@example.com")
	if _, _, err := store.DraftOutboundMessage(context.Background(), scope, &domain.OutboundMessage{
		SubjectType: domain.SubjectLead, SubjectID: "lead-2",
		ChannelKind: domain.ChannelEmail, Address: "theirs@example.com",
		Body: "Hi.", Status: domain.OutboundPending, AssigneeID: "somebody-else",
	}); err != nil {
		t.Fatalf("seed second draft: %v", err)
	}

	res, err := s.ListOutboundMessages(ctx, connect.NewRequest(&crmv1.ListOutboundMessagesRequest{
		MineOnly: true,
	}))
	if err != nil {
		t.Fatalf("list mine: %v", err)
	}
	if len(res.Msg.GetMessages()) != 1 || res.Msg.GetMessages()[0].GetAddress() != "mine@example.com" {
		t.Errorf("mine_only returned %+v, want only the caller's own draft", res.Msg.GetMessages())
	}
	// The owner comes from the token, so a client cannot ask for somebody
	// else's queue by naming them.
	if res.Msg.GetMessages()[0].GetAssigneeId() != principalSubject(ctx) {
		t.Errorf("assignee = %q, want the caller", res.Msg.GetMessages()[0].GetAssigneeId())
	}
}

// TestSendingIsRefusedWithoutAGoogleIntegration covers the shape of the
// answer, not only the fact of it. A deployment that cannot send should say
// so and point at what to do instead, rather than failing in a way that reads
// like a bug.
func TestSendingIsRefusedWithoutAGoogleIntegration(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}
	draft := seedDraft(t, store, scope, "alex@example.com")

	_, err := s.SendOutboundMessage(ctx, connect.NewRequest(&crmv1.SendOutboundMessageRequest{
		Id: draft.ID,
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("send with no integration: %v (code %v), want FailedPrecondition",
			err, connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "mark it sent") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}

	// And it changed nothing: a refused send must leave the draft sendable by
	// hand, not consumed.
	after, err := store.GetOutboundMessage(context.Background(), scope, draft.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if after.Status != domain.OutboundPending {
		t.Errorf("status = %q after a refused send, want it still pending", after.Status)
	}
}

// TestSendingRefusesANonEmailDraft: SMS and WhatsApp have no provider here,
// so those drafts stay one-tap-by-hand rather than failing at Gmail.
func TestSendingRefusesANonEmailDraft(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	draft, _, err := store.DraftOutboundMessage(context.Background(), scope, &domain.OutboundMessage{
		SubjectType: domain.SubjectContact, SubjectID: "c-1",
		ChannelKind: domain.ChannelWhatsApp, Address: "+441632960001",
		Body: "Hi", Status: domain.OutboundPending,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err = s.SendOutboundMessage(ctx, connect.NewRequest(&crmv1.SendOutboundMessageRequest{
		Id: draft.ID,
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("sending a WhatsApp draft: %v (code %v), want FailedPrecondition",
			err, connect.CodeOf(err))
	}
}

// TestSendingRefusesAWithdrawnContact is the consent check at its last useful
// moment — after this point a message has left the building.
func TestSendingRefusesAWithdrawnContact(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}
	draft := seedDraft(t, store, scope, "alex@example.com")

	if _, err := store.RecordConsent(context.Background(), scope, &domain.MessagingConsent{
		ChannelKind: domain.ChannelEmail, Address: "alex@example.com",
		State: domain.ConsentWithdrawn, Source: domain.ConsentSourceInbound,
	}); err != nil {
		t.Fatalf("record withdrawal: %v", err)
	}

	_, err := s.SendOutboundMessage(ctx, connect.NewRequest(&crmv1.SendOutboundMessageRequest{
		Id: draft.ID,
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("send after a withdrawal: %v (code %v), want FailedPrecondition",
			err, connect.CodeOf(err))
	}
	// The consent check must come before the "can this deployment send"
	// check would matter — a refusal for the wrong reason would let a
	// configured deployment through.
	if !strings.Contains(err.Error(), domain.RefusalConsentWithdrawn) {
		t.Errorf("refused for the wrong reason: %v", err)
	}

	after, err := store.GetOutboundMessage(context.Background(), scope, draft.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if after.Status != domain.OutboundPending {
		t.Errorf("status = %q, want the draft untouched", after.Status)
	}
}
