package workflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/secrets"
)

func activeRuleWithActions(t *testing.T, store domain.Store, actions ...domain.WorkflowAction) *domain.WorkflowRule {
	t.Helper()
	ctx, scope := context.Background(), testScope()
	rule, err := store.CreateWorkflowRule(ctx, scope, &domain.WorkflowRule{Name: "Advanced flow", CreatedBy: "author"})
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.CreateWorkflowRuleVersion(ctx, scope, &domain.WorkflowRuleVersion{
		RuleID: rule.ID, TriggerEventType: domain.EventLeadCreated, Actions: actions, CreatedBy: "author",
	})
	if err != nil {
		t.Fatal(err)
	}
	rule.Status, rule.ActiveVersionID = domain.WorkflowActive, version.ID
	rule, err = store.UpdateWorkflowRule(ctx, scope, rule)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func TestApprovalPausesAndResumesASequence(t *testing.T) {
	ctx, scope := context.Background(), testScope()
	store := domain.NewMemStore()
	rule := activeRuleWithActions(t, store,
		domain.WorkflowAction{Type: domain.ActionRequestApproval, Title: "Manager review", Body: "Check the lead", Assignee: domain.AssigneeFixedUser, AssigneeID: "manager-1"},
		domain.WorkflowAction{Type: domain.ActionCreateTask, Title: "Call approved lead", Assignee: domain.AssigneeRecordOwner},
	)
	createLead(t, store, "referral")
	engine := New(store, nil)
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatalf("request approval: %v", err)
	}
	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 10)
	if err != nil || len(executions) != 1 {
		t.Fatalf("executions = %+v, err = %v", executions, err)
	}
	if executions[0].Status != domain.ExecutionWaitingApproval || executions[0].ActionsCompleted != 0 {
		t.Fatalf("approval did not pause execution: %+v", executions[0])
	}
	approvals, err := store.ListWorkflowApprovals(ctx, scope, "manager-1", domain.ApprovalPending, 10)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals = %+v, err = %v", approvals, err)
	}
	if _, err := store.DecideWorkflowApproval(ctx, scope, approvals[0].ID, "manager-1", domain.ApprovalApproved, "Looks good", time.Now()); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatalf("resume: %v", err)
	}
	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 10}, domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil || len(tasks) != 1 || tasks[0].Title != "Call approved lead" {
		t.Fatalf("tasks after approval = %+v, err = %v", tasks, err)
	}
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatalf("idempotent pass: %v", err)
	}
	tasks, _, _ = store.ListTasks(ctx, scope, domain.Page{Size: 10}, domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if len(tasks) != 1 {
		t.Fatalf("approval replay created %d tasks, want one", len(tasks))
	}
}

func TestDueScheduleUsesTheExecutionLedgerOnce(t *testing.T) {
	ctx, scope := context.Background(), testScope()
	store := domain.NewMemStore()
	lead := createLead(t, store, "manual")
	events, err := store.OutboxEventsAfter(ctx, scope, domain.WorkflowCursor{}, 100)
	if err != nil || len(events) == 0 {
		t.Fatalf("seed outbox: %v, %+v", err, events)
	}
	last := events[len(events)-1]
	if err := store.SaveWorkflowCursor(ctx, scope, &domain.WorkflowCursor{LastEventCreatedAt: last.CreatedAt, LastEventID: last.ID}); err != nil {
		t.Fatal(err)
	}
	rule := activeRuleWithActions(t, store,
		domain.WorkflowAction{Type: domain.ActionCreateTask, Title: "Scheduled check-in", Assignee: domain.AssigneeRecordOwner},
	)
	due := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	schedule, err := store.CreateWorkflowSchedule(ctx, scope, &domain.WorkflowSchedule{
		RuleID: rule.ID, SubjectType: domain.SubjectLead, SubjectID: lead.ID, TimeZone: "UTC",
		Recurrence: domain.ScheduleDaily, Interval: 1, Status: domain.ScheduleActive,
		NextRunAt: due, CreatedBy: "author",
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(store, nil)
	engine.now = func() time.Time { return due.Add(time.Minute) }
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetWorkflowSchedule(ctx, scope, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.NextRunAt.Equal(due.AddDate(0, 0, 1)) || updated.LastRunAt.IsZero() {
		t.Fatalf("schedule was not advanced: %+v", updated)
	}
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	tasks, _, _ := store.ListTasks(ctx, scope, domain.Page{Size: 10}, domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if len(tasks) != 1 {
		t.Fatalf("two passes created %d scheduled tasks, want one", len(tasks))
	}
}

func TestOverdueRecurringScheduleDoesNotCreateACatchUpStorm(t *testing.T) {
	ctx, scope := context.Background(), testScope()
	store := domain.NewMemStore()
	lead := createLead(t, store, "manual")
	events, err := store.OutboxEventsAfter(ctx, scope, domain.WorkflowCursor{}, 100)
	if err != nil || len(events) == 0 {
		t.Fatalf("seed outbox: %v, %+v", err, events)
	}
	last := events[len(events)-1]
	if err := store.SaveWorkflowCursor(ctx, scope, &domain.WorkflowCursor{LastEventCreatedAt: last.CreatedAt, LastEventID: last.ID}); err != nil {
		t.Fatal(err)
	}
	rule := activeRuleWithActions(t, store,
		domain.WorkflowAction{Type: domain.ActionCreateTask, Title: "Current check-in", Assignee: domain.AssigneeRecordOwner},
	)
	due := time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)
	schedule, err := store.CreateWorkflowSchedule(ctx, scope, &domain.WorkflowSchedule{
		RuleID: rule.ID, SubjectType: domain.SubjectLead, SubjectID: lead.ID, TimeZone: "UTC",
		Recurrence: domain.ScheduleDaily, Interval: 1, Status: domain.ScheduleActive,
		NextRunAt: due, CreatedBy: "author",
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(store, nil)
	engine.now = func() time.Time { return time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC) }
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetWorkflowSchedule(ctx, scope, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.NextRunAt.Equal(time.Date(2026, time.September, 13, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("overdue schedule advanced to %v, want next future occurrence", updated.NextRunAt)
	}
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	tasks, _, _ := store.ListTasks(ctx, scope, domain.Page{Size: 100}, domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if len(tasks) != 1 {
		t.Fatalf("overdue schedule created %d catch-up tasks, want one", len(tasks))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestWebhookActionIsSignedAndCarriesAStableId(t *testing.T) {
	ctx, scope := context.Background(), testScope()
	store := domain.NewMemStore()
	rawKey := bytes.Repeat([]byte{7}, 32)
	sealer, err := secrets.New(base64.RawStdEncoding.EncodeToString(rawKey))
	if err != nil {
		t.Fatal(err)
	}
	endpointID := domain.NewID()
	const signingSecret = "receiver-secret"
	ciphertext, err := sealer.Seal([]byte(signingSecret), WebhookSecretBinding(scope.TenantID, endpointID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWorkflowWebhookEndpoint(ctx, scope, &domain.WorkflowWebhookEndpoint{
		ID: endpointID, Name: "Warehouse", URL: "https://hooks.example.com/savvy",
		SigningSecretCiphertext: ciphertext, CreatedBy: "author",
	}); err != nil {
		t.Fatal(err)
	}
	rule := activeRuleWithActions(t, store,
		domain.WorkflowAction{Type: domain.ActionSendWebhook, WebhookEndpointID: endpointID},
	)
	createLead(t, store, "referral")
	var gotBody []byte
	var gotHeader http.Header
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(req.Body)
		gotHeader = req.Header.Clone()
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	sender := NewWebhookSender(store, sealer, client)
	sender.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	if _, err := NewWithWebhook(store, sender, nil).RunTenant(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotHeader.Get("X-Savvy-Event-ID"), ":0") || gotHeader.Get("X-Savvy-Timestamp") != "1800000000" {
		t.Fatalf("idempotency headers = %q / %q", gotHeader.Get("X-Savvy-Event-ID"), gotHeader.Get("X-Savvy-Timestamp"))
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte("1800000000."))
	_, _ = mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotHeader.Get("X-Savvy-Signature") != want {
		t.Fatalf("signature = %q, want %q", gotHeader.Get("X-Savvy-Signature"), want)
	}
	if !bytes.Contains(gotBody, []byte(rule.ID)) || bytes.Contains(gotBody, []byte("receiver-secret")) {
		t.Fatalf("unexpected webhook payload: %s", gotBody)
	}
}
