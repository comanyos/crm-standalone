package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// The workflow RPC surface. What is asserted here is the authoring contract
// from docs/WORKFLOW_AUTOMATION.md — that a rule cannot start active, that
// editing one never changes what is already running, and that a notification
// belongs to the principal who received it — rather than the plumbing, which
// the store's own parity tests cover.

func workflowCtx() context.Context { return ctxAs("tenant-wf", "org-wf") }

// draftRule creates a rule and returns it.
func draftRule(t *testing.T, s *CRMService, ctx context.Context, name string) *crmv1.WorkflowRule {
	t.Helper()
	res, err := s.CreateWorkflowRule(ctx, connect.NewRequest(&crmv1.CreateWorkflowRuleRequest{
		Name: name,
	}))
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	return res.Msg.GetRule()
}

func referralAction() []*crmv1.WorkflowAction {
	return []*crmv1.WorkflowAction{{
		Type: domain.ActionCreateTask, Title: "Call the referral",
		Assignee: domain.AssigneeRecordOwner,
	}}
}

// TestARuleCannotStartActive. Creating one is a draft whatever the caller
// wants, because there is nothing published for it to run.
func TestARuleCannotStartActive(t *testing.T) {
	s := newTestService()
	rule := draftRule(t, s, workflowCtx(), "Referral follow-up")

	if rule.GetStatus() != domain.WorkflowDraft {
		t.Errorf("a new rule is %q, want draft", rule.GetStatus())
	}
	if rule.GetActiveVersionId() != "" {
		t.Errorf("a new rule already points at version %q", rule.GetActiveVersionId())
	}

	_, err := s.SetWorkflowRuleStatus(workflowCtx(), connect.NewRequest(
		&crmv1.SetWorkflowRuleStatusRequest{Id: rule.GetId(), Status: domain.WorkflowActive}))
	if err == nil {
		t.Fatal("activating a rule with no published version succeeded; it would match events and do nothing")
	}
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestPublishingDoesNotChangeWhatIsRunning unless the author says so. An
// execution's ledger row names the exact text of the rule that ran, and that
// is only true if editing never rewrites a live version.
func TestPublishingDoesNotChangeWhatIsRunning(t *testing.T) {
	s := newTestService()
	ctx := workflowCtx()
	rule := draftRule(t, s, ctx, "Referral follow-up")

	first, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(
		&crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Actions: referralAction(), Activate: true,
		}))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if first.Msg.GetRule().GetStatus() != domain.WorkflowActive {
		t.Fatalf("publishing with activate left the rule %q", first.Msg.GetRule().GetStatus())
	}
	firstVersion := first.Msg.GetVersion().GetId()

	// A second publish without activate: a correction the author has not
	// switched on yet.
	second, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(
		&crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Actions: []*crmv1.WorkflowAction{{
				Type: domain.ActionCreateTask, Title: "Call the referral twice",
				Assignee: domain.AssigneeRecordOwner,
			}},
		}))
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if second.Msg.GetVersion().GetVersion() != 2 {
		t.Errorf("second version is numbered %d, want 2", second.Msg.GetVersion().GetVersion())
	}
	if got := second.Msg.GetRule().GetActiveVersionId(); got != firstVersion {
		t.Errorf("publishing changed the running version to %q; an author's unreviewed "+
			"edit started acting on records", got)
	}

	// And the first version's text is unchanged by the edit.
	versions, err := s.ListWorkflowRuleVersions(ctx, connect.NewRequest(
		&crmv1.ListWorkflowRuleVersionsRequest{RuleId: rule.GetId()}))
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions.Msg.GetVersions()) != 2 {
		t.Fatalf("rule has %d versions, want 2", len(versions.Msg.GetVersions()))
	}
	for _, version := range versions.Msg.GetVersions() {
		if version.GetId() != firstVersion {
			continue
		}
		if title := version.GetActions()[0].GetTitle(); title != "Call the referral" {
			t.Errorf("the published version now says %q; a version is meant to be immutable", title)
		}
	}
}

// TestRenamingCannotChangeWhatARuleDoes. UpdateWorkflowRule takes a name and
// a description, and the store's update replaces a whole row — so the thing
// worth asserting is that a rename leaves the lifecycle alone.
func TestRenamingCannotChangeWhatARuleDoes(t *testing.T) {
	s := newTestService()
	ctx := workflowCtx()
	rule := draftRule(t, s, ctx, "Referral follow-up")
	published, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(
		&crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Actions: referralAction(), Activate: true,
		}))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	renamed, err := s.UpdateWorkflowRule(ctx, connect.NewRequest(&crmv1.UpdateWorkflowRuleRequest{
		Id: rule.GetId(), Name: "Referral follow-up (EMEA)", Description: "Now with a region",
	}))
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Msg.GetRule().GetName() != "Referral follow-up (EMEA)" {
		t.Errorf("name = %q", renamed.Msg.GetRule().GetName())
	}
	if got := renamed.Msg.GetRule().GetStatus(); got != domain.WorkflowActive {
		t.Errorf("a rename left the rule %q, want it still active", got)
	}
	if got := renamed.Msg.GetRule().GetActiveVersionId(); got != published.Msg.GetVersion().GetId() {
		t.Errorf("a rename repointed the rule at %q", got)
	}
	if renamed.Msg.GetRule().GetCreatedBy() != "test-user" {
		t.Errorf("created_by = %q; a rename rewrote the rule's author",
			renamed.Msg.GetRule().GetCreatedBy())
	}
}

// TestArchivingIsTerminal. A retired rule that can be switched back on is one
// that can start acting on records long after everyone stopped thinking
// about it.
func TestArchivingIsTerminal(t *testing.T) {
	s := newTestService()
	ctx := workflowCtx()
	rule := draftRule(t, s, ctx, "Referral follow-up")
	if _, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(
		&crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Actions: referralAction(), Activate: true,
		})); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := s.SetWorkflowRuleStatus(ctx, connect.NewRequest(
		&crmv1.SetWorkflowRuleStatusRequest{
			Id: rule.GetId(), Status: domain.WorkflowArchived,
		})); err != nil {
		t.Fatalf("archive: %v", err)
	}

	for _, status := range []string{domain.WorkflowActive, domain.WorkflowPaused, domain.WorkflowDraft} {
		_, err := s.SetWorkflowRuleStatus(ctx, connect.NewRequest(
			&crmv1.SetWorkflowRuleStatusRequest{Id: rule.GetId(), Status: status}))
		if err == nil {
			t.Errorf("an archived rule was moved to %q", status)
			continue
		}
		if got := connectCode(t, err); got != connect.CodeFailedPrecondition {
			t.Errorf("archived -> %s: code = %v, want FailedPrecondition", status, got)
		}
	}

	// Nor can new behaviour be published into it.
	_, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(
		&crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Actions: referralAction(),
		}))
	if err == nil {
		t.Error("a version was published into an archived rule")
	}

	// And it is hidden from the default listing.
	listed, err := s.ListWorkflowRules(ctx, connect.NewRequest(&crmv1.ListWorkflowRulesRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range listed.Msg.GetRules() {
		if row.GetId() == rule.GetId() {
			t.Error("an archived rule appears in the default listing")
		}
	}
}

// TestAnUnknownTriggerIsRefusedAtAuthoringTime rather than silently never
// matching. A rule that will not save is better than one the author believes
// is working.
func TestAnUnknownTriggerIsRefusedAtAuthoringTime(t *testing.T) {
	s := newTestService()
	ctx := workflowCtx()
	rule := draftRule(t, s, ctx, "Wishful thinking")

	for _, tc := range []struct {
		name    string
		request *crmv1.PublishWorkflowRuleVersionRequest
	}{
		{"unknown trigger", &crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: "invoice.paid", Actions: referralAction(),
		}},
		{"unknown field", &crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Conditions: []*crmv1.WorkflowCondition{
				{Field: "sorce", Operator: domain.OpEquals, Value: "referral"},
			},
			Actions: referralAction(),
		}},
		{"operator the field's kind forbids", &crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Conditions: []*crmv1.WorkflowCondition{
				{Field: "score", Operator: domain.OpContains, Value: "5"},
			},
			Actions: referralAction(),
		}},
		{"no actions", &crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
		}},
		{"action with no assignee", &crmv1.PublishWorkflowRuleVersionRequest{
			RuleId: rule.GetId(), TriggerEventType: domain.EventLeadCreated,
			Actions: []*crmv1.WorkflowAction{{Type: domain.ActionCreateTask, Title: "Call"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(tc.request))
			if err == nil {
				t.Fatal("the version saved")
			}
			if got := connectCode(t, err); got != connect.CodeInvalidArgument {
				t.Errorf("code = %v, want InvalidArgument", got)
			}
		})
	}
}

// TestTheCatalogOffersExactlyWhatThePublisherAccepts. An editor built from
// this response must not be able to compose a rule the server refuses.
func TestTheCatalogOffersExactlyWhatThePublisherAccepts(t *testing.T) {
	s := newTestService()
	ctx := workflowCtx()
	res, err := s.DescribeWorkflowCatalog(ctx, connect.NewRequest(
		&crmv1.DescribeWorkflowCatalogRequest{}))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if len(res.Msg.GetTriggers()) == 0 {
		t.Fatal("the catalog offers no triggers")
	}

	for _, trigger := range res.Msg.GetTriggers() {
		for _, field := range trigger.GetFields() {
			for _, operator := range field.GetOperators() {
				value := "referral"
				if field.GetKind() == "number" {
					value = "42"
				}
				rule := draftRule(t, s, ctx, "catalog "+trigger.GetEventType()+" "+field.GetName())
				_, err := s.PublishWorkflowRuleVersion(ctx, connect.NewRequest(
					&crmv1.PublishWorkflowRuleVersionRequest{
						RuleId: rule.GetId(), TriggerEventType: trigger.GetEventType(),
						Conditions: []*crmv1.WorkflowCondition{
							{Field: field.GetName(), Operator: operator, Value: value},
						},
						Actions: referralAction(),
					}))
				if err != nil {
					t.Errorf("the catalog offers %s %s on %s, and publishing it failed: %v",
						trigger.GetEventType(), operator, field.GetName(), err)
				}
			}
		}
	}

	for _, actionType := range res.Msg.GetActionTypes() {
		if !strings.Contains(strings.Join(domain.WorkflowActionTypes(), ","), actionType) {
			t.Errorf("the catalog offers action %q, which the domain does not know", actionType)
		}
	}
	if res.Msg.GetMaxConditions() == 0 || res.Msg.GetMaxActions() == 0 || res.Msg.GetMaxTextLength() == 0 {
		t.Error("the catalog reports no authoring bounds, so an editor must guess at them")
	}
}

// TestAnExecutionLedgerNeedsARuleThatExists. An unknown id is a not-found
// rather than an empty ledger, which reads as "this rule has never run".
func TestAnExecutionLedgerNeedsARuleThatExists(t *testing.T) {
	s := newTestService()
	_, err := s.ListWorkflowExecutions(workflowCtx(), connect.NewRequest(
		&crmv1.ListWorkflowExecutionsRequest{RuleId: "no-such-rule"}))
	if err == nil {
		t.Fatal("an unknown rule returned a ledger")
	}
	if got := connectCode(t, err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestNotificationsBelongToTheirRecipient. Neither RPC takes a recipient, so
// what has to be checked is that the principal's own subject is what the
// store is asked for.
func TestNotificationsBelongToTheirRecipient(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := workflowCtx()
	scope := domain.Scope{TenantID: "tenant-wf", OrgID: "org-wf"}

	mine, err := store.CreateNotification(ctx, scope, &domain.Notification{
		RecipientID: "test-user", Kind: "workflow", Title: "Your referral is waiting",
	})
	if err != nil {
		t.Fatalf("seed own notification: %v", err)
	}
	theirs, err := store.CreateNotification(ctx, scope, &domain.Notification{
		RecipientID: "someone-else", Kind: "workflow", Title: "A colleague's private alert",
	})
	if err != nil {
		t.Fatalf("seed colleague notification: %v", err)
	}

	listed, err := s.ListNotifications(ctx, connect.NewRequest(&crmv1.ListNotificationsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, row := range listed.Msg.GetNotifications() {
		seen[row.GetId()] = true
	}
	if !seen[mine.ID] {
		t.Error("the caller's own notification is missing")
	}
	if seen[theirs.ID] {
		t.Error("a colleague's notification was returned to somebody else")
	}
	if listed.Msg.GetUnreadCount() != 1 {
		t.Errorf("unread count = %d, want 1", listed.Msg.GetUnreadCount())
	}

	if _, err := s.MarkNotificationRead(ctx, connect.NewRequest(
		&crmv1.MarkNotificationReadRequest{Id: theirs.ID})); err == nil {
		t.Error("a colleague's notification was marked read by somebody else")
	}

	read, err := s.MarkNotificationRead(ctx, connect.NewRequest(
		&crmv1.MarkNotificationReadRequest{Id: mine.ID}))
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if read.Msg.GetNotification().GetReadAt() == nil {
		t.Error("marking read left no timestamp")
	}

	after, err := s.ListNotifications(ctx, connect.NewRequest(
		&crmv1.ListNotificationsRequest{UnreadOnly: true}))
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	if len(after.Msg.GetNotifications()) != 0 || after.Msg.GetUnreadCount() != 0 {
		t.Errorf("after marking read the inbox still reports %d unread",
			after.Msg.GetUnreadCount())
	}
}
