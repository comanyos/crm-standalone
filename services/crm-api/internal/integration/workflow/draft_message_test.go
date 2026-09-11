package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/domain"
)

// draft_message end to end through the engine (task_crm.md §6).
//
// The action composes three earlier sections — §2 decides whether contacting
// somebody is permitted, §5 supplies the wording, §3 is what the rep taps —
// so these tests are mostly about the seam between them rather than about any
// one of them.

// draftFixture builds a store with an active rule that drafts an email to
// every new lead, and returns the store, the rule and the template.
func draftFixture(t *testing.T, body string) (domain.Store, *domain.MessageTemplate) {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := testScope()

	template, err := store.CreateMessageTemplate(ctx, scope, &domain.MessageTemplate{
		Name: "New lead intro", ChannelKind: domain.ChannelEmail,
		Subject: "About {{account.name}}", Body: body, CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	rule, err := store.CreateWorkflowRule(ctx, scope, &domain.WorkflowRule{
		Name: "Introduce ourselves", CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	version, err := store.CreateWorkflowRuleVersion(ctx, scope, &domain.WorkflowRuleVersion{
		RuleID:           rule.ID,
		TriggerEventType: domain.EventLeadCreated,
		Actions: []domain.WorkflowAction{{
			Type: domain.ActionDraftMessage, ChannelKind: domain.ChannelEmail, TemplateID: template.ID,
		}},
		CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	rule.Status, rule.ActiveVersionID = domain.WorkflowActive, version.ID
	if _, err := store.UpdateWorkflowRule(ctx, scope, rule); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return store, template
}

func leadWithEmail(t *testing.T, store domain.Store, email string) *domain.Lead {
	t.Helper()
	lead, err := store.CreateLead(context.Background(), testScope(), &domain.Lead{
		Company: "Example Ltd", Source: "capture", OwnerID: "rep-1", ContactEmail: email,
	})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	return lead
}

func pendingDrafts(t *testing.T, store domain.Store) []*domain.OutboundMessage {
	t.Helper()
	drafts, err := store.ListOutboundMessages(context.Background(), testScope(),
		domain.OutboundMessageFilter{}, 100)
	if err != nil {
		t.Fatalf("list drafts: %v", err)
	}
	return drafts
}

func TestARuleDraftsAPersonalisedMessage(t *testing.T) {
	store, _ := draftFixture(t, "Hi {{contact.first_name}}, about {{account.name}}.")
	ctx := context.Background()
	lead := leadWithEmail(t, store, "Alex@Example.com")

	if _, err := New(store, nil).RunTenant(ctx, testScope()); err != nil {
		t.Fatalf("run: %v", err)
	}

	drafts := pendingDrafts(t, store)
	if len(drafts) != 1 {
		t.Fatalf("drafted %d messages, want 1", len(drafts))
	}
	draft := drafts[0]

	if draft.Status != domain.OutboundPending {
		t.Errorf("status = %q, want %q — nothing here sends", draft.Status, domain.OutboundPending)
	}
	if draft.Address != "alex@example.com" {
		t.Errorf("address = %q, want it normalised", draft.Address)
	}
	// A Lead has no name, so the greeting collapses rather than reaching a
	// customer as "Hi ,".
	if draft.Body != "Hi, about Example Ltd." {
		t.Errorf("body = %q, want the empty name tidied away", draft.Body)
	}
	if draft.Subject != "About Example Ltd" {
		t.Errorf("subject = %q, want it rendered too", draft.Subject)
	}
	// Addressed to whoever owns the record when the rule ran, so the queue can
	// be filtered to "mine".
	if draft.AssigneeID != "rep-1" {
		t.Errorf("assignee = %q, want the lead's owner", draft.AssigneeID)
	}
	if draft.SubjectType != domain.SubjectLead || draft.SubjectID != lead.ID {
		t.Errorf("draft is about %s/%s, want the lead", draft.SubjectType, draft.SubjectID)
	}
}

func TestARuleDraftsWhatsAppForAPhoneOnlyPersonFirstLead(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := testScope()
	template, err := store.CreateMessageTemplate(ctx, scope, &domain.MessageTemplate{
		Name: "WhatsApp intro", ChannelKind: domain.ChannelWhatsApp,
		Body: "Hi {{contact.first_name}}, thanks for your enquiry.", CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := store.CreateWorkflowRule(ctx, scope, &domain.WorkflowRule{Name: "WhatsApp new leads", CreatedBy: "author-1"})
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.CreateWorkflowRuleVersion(ctx, scope, &domain.WorkflowRuleVersion{
		RuleID: rule.ID, TriggerEventType: domain.EventLeadCreated, CreatedBy: "author-1",
		Actions: []domain.WorkflowAction{{
			Type: domain.ActionDraftMessage, ChannelKind: domain.ChannelWhatsApp, TemplateID: template.ID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rule.Status, rule.ActiveVersionID = domain.WorkflowActive, version.ID
	if _, err := store.UpdateWorkflowRule(ctx, scope, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateLead(ctx, scope, &domain.Lead{
		FirstName: "Alex", Phone: "+1 (555) 010-0000", OwnerID: "rep-1", Source: "Meta Lead Ads",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	drafts := pendingDrafts(t, store)
	if len(drafts) != 1 || drafts[0].ChannelKind != domain.ChannelWhatsApp ||
		drafts[0].Address != "+15550100000" || drafts[0].Body != "Hi Alex, thanks for your enquiry." {
		t.Fatalf("WhatsApp drafts = %+v", drafts)
	}
}

// TestARuleWillNotDraftForSomebodyWhoOptedOut is the reason §2 had to exist
// before this section could.
func TestARuleWillNotDraftForSomebodyWhoOptedOut(t *testing.T) {
	store, _ := draftFixture(t, "Hi {{contact.first_name}},")
	ctx := context.Background()
	scope := testScope()

	if _, err := store.RecordConsent(ctx, scope, &domain.MessagingConsent{
		ChannelKind: domain.ChannelEmail, Address: "alex@example.com",
		State: domain.ConsentWithdrawn, Source: domain.ConsentSourceInbound,
	}); err != nil {
		t.Fatalf("record withdrawal: %v", err)
	}
	leadWithEmail(t, store, "alex@example.com")

	// A failed execution is reported as a run error — see
	// TestADeletedRecordFailsTerminally for the same shape.
	if _, err := New(store, nil).RunTenant(ctx, scope); err == nil {
		t.Fatal("drafting for somebody who opted out reported success")
	}

	if drafts := pendingDrafts(t, store); len(drafts) != 0 {
		t.Fatalf("drafted %d messages for somebody who opted out", len(drafts))
	}

	// And it fails terminally rather than retrying: a withdrawal does not
	// become a grant by waiting, and three attempts to contact somebody who
	// said no is precisely what the ledger exists to prevent.
	executions, err := store.ListWorkflowExecutions(ctx, scope, "", 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("recorded %d executions, want 1", len(executions))
	}
	execution := executions[0]
	if execution.Status != domain.ExecutionFailed {
		t.Errorf("status = %q, want %q", execution.Status, domain.ExecutionFailed)
	}
	if execution.ErrorCategory != ReasonConsentWithdrawn {
		t.Errorf("reason = %q, want %q", execution.ErrorCategory, ReasonConsentWithdrawn)
	}
	if execution.Attempts > 1 {
		t.Errorf("retried %d times against a withdrawal", execution.Attempts)
	}
	// The reason is a bounded code, so no part of the message or the person's
	// details can reach the ledger.
	if strings.Contains(execution.ErrorCategory, "@") {
		t.Errorf("the ledger reason carries an address: %q", execution.ErrorCategory)
	}
}

func TestARuleWillNotDraftWithoutAnAddress(t *testing.T) {
	store, _ := draftFixture(t, "Hi {{contact.first_name}},")
	ctx := context.Background()
	scope := testScope()

	// A lead captured from a form that asked for a company and nothing else.
	if _, err := store.CreateLead(ctx, scope, &domain.Lead{
		Company: "Example Ltd", Source: "capture", OwnerID: "rep-1",
	}); err != nil {
		t.Fatalf("create lead: %v", err)
	}

	if _, err := New(store, nil).RunTenant(ctx, scope); err == nil {
		t.Fatal("drafting to a lead with no email reported success")
	}
	if drafts := pendingDrafts(t, store); len(drafts) != 0 {
		t.Fatalf("drafted %d messages with no address", len(drafts))
	}

	executions, err := store.ListWorkflowExecutions(ctx, scope, "", 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if executions[0].ErrorCategory != ReasonNoAddress {
		t.Errorf("reason = %q, want %q", executions[0].ErrorCategory, ReasonNoAddress)
	}
	if executions[0].Attempts > 1 {
		t.Error("retried against a lead that has no email; an address does not appear by waiting")
	}
}

// TestARedeliveredEventDraftsOnce covers the window the execution-keyed
// idempotency exists for: the same event reaching the engine twice must not
// mean a customer gets the same message twice.
func TestARedeliveredEventDraftsOnce(t *testing.T) {
	store, _ := draftFixture(t, "Hi {{contact.first_name}},")
	ctx := context.Background()
	scope := testScope()
	leadWithEmail(t, store, "alex@example.com")

	engine := New(store, nil)
	for i := 0; i < 3; i++ {
		if _, err := engine.RunTenant(ctx, scope); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		// Rewind the cursor so the same event is read again, which is what an
		// at-least-once outbox does on its own.
		cursor, err := store.GetWorkflowCursor(ctx, scope)
		if err != nil {
			t.Fatalf("read cursor: %v", err)
		}
		cursor.LastEventID = ""
		cursor.LastEventCreatedAt = cursor.LastEventCreatedAt.Add(-1)
		if err := store.SaveWorkflowCursor(ctx, scope, cursor); err != nil {
			t.Fatalf("rewind cursor: %v", err)
		}
	}

	if drafts := pendingDrafts(t, store); len(drafts) != 1 {
		t.Errorf("three deliveries of one event drafted %d messages, want 1", len(drafts))
	}
}

func TestADeletedTemplateFailsTerminally(t *testing.T) {
	store, template := draftFixture(t, "Hi {{contact.first_name}},")
	ctx := context.Background()
	scope := testScope()

	// Deleted after the rule was published. It will not come back, so the
	// rule should surface in the ledger rather than retry three times first.
	if err := store.DeleteMessageTemplate(ctx, scope, template.ID); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	leadWithEmail(t, store, "alex@example.com")

	if _, err := New(store, nil).RunTenant(ctx, scope); err == nil {
		t.Fatal("drafting from a deleted template reported success")
	}
	executions, err := store.ListWorkflowExecutions(ctx, scope, "", 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if executions[0].ErrorCategory != ReasonInvalidAction {
		t.Errorf("reason = %q, want %q", executions[0].ErrorCategory, ReasonInvalidAction)
	}
	if executions[0].Attempts > 1 {
		t.Errorf("retried %d times against a deleted template", executions[0].Attempts)
	}
}

// TestDraftingCannotLoop re-asserts the structural property after adding an
// action. A draft is not an event any trigger listens for; if it ever became
// one, a rule could draft in response to its own draft.
func TestDraftingCannotLoop(t *testing.T) {
	if err := domain.AssertTriggersCannotLoop(); err != nil {
		t.Fatalf("adding draft_message made a trigger reachable from an action: %v", err)
	}
}
