package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The engine, end to end: a rule is authored, a record is created, and the
// work appears. The tests that matter most are the ones about doing it twice.

func testScope() domain.Scope { return domain.Scope{TenantID: "tenant-1", OrgID: "org-1"} }

// fixture builds a store with an active rule that creates a task and notifies
// the lead's owner whenever a lead is created.
func fixture(t *testing.T, conditions ...domain.WorkflowCondition) (domain.Store, *domain.WorkflowRule) {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := testScope()

	rule, err := store.CreateWorkflowRule(ctx, scope, &domain.WorkflowRule{
		Name: "Follow up on new leads", CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	version, err := store.CreateWorkflowRuleVersion(ctx, scope, &domain.WorkflowRuleVersion{
		RuleID:           rule.ID,
		TriggerEventType: domain.EventLeadCreated,
		Conditions:       conditions,
		Actions: []domain.WorkflowAction{
			{
				Type: domain.ActionCreateTask, Title: "Call the new lead",
				Body: "Reach out within a day.", Priority: domain.TaskPriorityHigh,
				Assignee: domain.AssigneeRecordOwner, DueInDays: 1,
			},
			{
				Type: domain.ActionNotify, Title: "A new lead needs you",
				Assignee: domain.AssigneeRecordOwner,
			},
		},
		CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	rule.Status, rule.ActiveVersionID = domain.WorkflowActive, version.ID
	if rule, err = store.UpdateWorkflowRule(ctx, scope, rule); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return store, rule
}

func createLead(t *testing.T, store domain.Store, source string) *domain.Lead {
	t.Helper()
	lead, err := store.CreateLead(context.Background(), testScope(), &domain.Lead{
		Company: "Acme", Source: source, OwnerID: "rep-1", Score: 80,
	})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	return lead
}

// TestARuleTurnsAnEventIntoWork is the happy path, asserted where a user
// would see it: a task on their list and a notification addressed to them.
func TestARuleTurnsAnEventIntoWork(t *testing.T) {
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()
	lead := createLead(t, store, "referral")

	result, err := New(store, nil).RunTenant(ctx, scope)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExecutionsMade != 1 || result.ActionsPerformed != 2 {
		t.Fatalf("made %d executions and performed %d actions, want 1 and 2",
			result.ExecutionsMade, result.ActionsPerformed)
	}

	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("automation created %d tasks, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Title != "Call the new lead" || task.AssigneeID != "rep-1" {
		t.Errorf("task = %+v, want it assigned to the lead's owner", task)
	}
	if task.SubjectType != domain.SubjectLead || task.SubjectID != lead.ID {
		t.Errorf("task is attached to %s/%s, want the lead", task.SubjectType, task.SubjectID)
	}
	if task.DueAt.IsZero() {
		t.Error("due_in_days produced no due date")
	}
	// Attribution names the rule, not a person: a task nobody typed must not
	// look as though somebody did.
	if !strings.HasPrefix(task.CreatedBy, "workflow:") {
		t.Errorf("task created_by = %q, want it to name the rule", task.CreatedBy)
	}

	notifications, err := store.ListNotifications(ctx, scope, "rep-1", true, 0)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(notifications) != 1 || notifications[0].RuleID != rule.ID {
		t.Fatalf("notifications = %+v, want one naming the rule", notifications)
	}

	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(executions) != 1 || executions[0].Status != domain.ExecutionSucceeded {
		t.Errorf("execution = %+v, want one succeeded", executions)
	}
}

// TestTheSameEventTwiceDoesTheWorkOnce is the property the whole feature
// rests on. Outbox delivery is at-least-once, so this is the ordinary case
// and not an edge: without it a customer gets two follow-up calls.
func TestTheSameEventTwiceDoesTheWorkOnce(t *testing.T) {
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()
	createLead(t, store, "referral")

	engine := New(store, nil)
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Rewind the cursor: exactly what a redelivery looks like to the engine.
	if err := store.SaveWorkflowCursor(ctx, scope, &domain.WorkflowCursor{}); err != nil {
		t.Fatalf("rewind cursor: %v", err)
	}
	second, err := engine.RunTenant(ctx, scope)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.ExecutionsMade != 0 {
		t.Errorf("a redelivered event created %d executions, want 0", second.ExecutionsMade)
	}

	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("a redelivered event produced %d tasks, want 1 — the customer would be called twice",
			len(tasks))
	}
	notifications, err := store.ListNotifications(ctx, scope, "rep-1", false, 0)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Errorf("a redelivered event produced %d notifications, want 1", len(notifications))
	}
	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(executions) != 1 {
		t.Errorf("ledger holds %d executions for one event, want 1", len(executions))
	}
}

// TestConditionsDecideWhetherARuleActs, and a rule that considered an event
// and declined records that it did.
func TestConditionsDecideWhetherARuleActs(t *testing.T) {
	store, rule := fixture(t, domain.WorkflowCondition{
		Field: "source", Operator: domain.OpEquals, Value: "referral",
	})
	ctx := context.Background()
	scope := testScope()

	createLead(t, store, "cold-call")
	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}

	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("a rule whose condition failed created %d tasks", len(tasks))
	}

	// The consideration is recorded: "why didn't my rule fire" is answerable.
	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(executions) != 1 || executions[0].Status != domain.ExecutionSkipped {
		t.Fatalf("executions = %+v, want one skipped", executions)
	}

	// And a matching lead does fire.
	createLead(t, store, "referral")
	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("second run: %v", err)
	}
	tasks, _, err = store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("a matching lead produced %d tasks, want 1", len(tasks))
	}
}

// TestNumericConditionsCompareAsNumbers. JSON decoding turns every number
// into a float64, so a score of 80 arrives as 80.0 — and a rule saying
// "score gt 50" that compared strings would be comparing "80" to "50"
// lexically, which happens to work until the score reaches 100.
func TestNumericConditionsCompareAsNumbers(t *testing.T) {
	store, _ := fixture(t, domain.WorkflowCondition{
		Field: "score", Operator: domain.OpGreaterThan, Value: "50",
	})
	ctx := context.Background()
	scope := testScope()

	// 100 is greater than 50 numerically and *less* lexically.
	if _, err := store.CreateLead(ctx, scope, &domain.Lead{
		Company: "Acme", Source: "referral", OwnerID: "rep-1", Score: 100,
	}); err != nil {
		t.Fatalf("create lead: %v", err)
	}
	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}
	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("a score of 100 did not satisfy 'score > 50': %d tasks", len(tasks))
	}
}

// TestOnlyActiveRulesRun.
func TestOnlyActiveRulesRun(t *testing.T) {
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()

	rule.Status = domain.WorkflowPaused
	if _, err := store.UpdateWorkflowRule(ctx, scope, rule); err != nil {
		t.Fatalf("pause: %v", err)
	}
	createLead(t, store, "referral")
	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}
	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("a paused rule created %d tasks", len(tasks))
	}
}

// TestActionsReadTheRecordRatherThanTheEvent is the design contract's other
// rule. An execution can run long after its event, and the record may have
// been reassigned in between; acting on the snapshot would give the work to
// somebody who no longer owns it.
func TestActionsReadTheRecordRatherThanTheEvent(t *testing.T) {
	store, _ := fixture(t)
	ctx := context.Background()
	scope := testScope()
	lead := createLead(t, store, "referral")

	// Reassigned after the event, before the execution.
	lead.OwnerID = "rep-2"
	if _, err := store.UpdateLead(ctx, scope, lead); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}
	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("created %d tasks, want 1", len(tasks))
	}
	if tasks[0].AssigneeID != "rep-2" {
		t.Errorf("task assigned to %q, want rep-2 — the action used the event snapshot "+
			"rather than re-reading the record", tasks[0].AssigneeID)
	}
}

// TestADeletedRecordFailsTerminally rather than retrying three times. The
// record will not come back, and the rule author needs to see the failure.
func TestADeletedRecordFailsTerminally(t *testing.T) {
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()
	lead := createLead(t, store, "referral")

	if err := store.DeleteLead(ctx, scope, lead.ID); err != nil {
		t.Fatalf("delete lead: %v", err)
	}
	if _, err := New(store, nil).RunTenant(ctx, scope); err == nil {
		t.Fatal("running against a deleted record reported success")
	}

	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("executions = %+v", executions)
	}
	if executions[0].Status != domain.ExecutionFailed {
		t.Errorf("status = %q, want failed without retrying", executions[0].Status)
	}
	if executions[0].ErrorCategory != ReasonRecordGone {
		t.Errorf("reason = %q, want %q", executions[0].ErrorCategory, ReasonRecordGone)
	}
	if executions[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1 — a deleted record is not worth retrying",
			executions[0].Attempts)
	}
}

// TestAnUnassignableRecordFailsWithAReadableReason.
func TestAnUnassignableRecordFailsWithAReadableReason(t *testing.T) {
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()

	// A lead with no owner: nobody to give the work to.
	lead, err := store.CreateLead(ctx, scope, &domain.Lead{Company: "Acme", Source: "referral"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	_ = lead

	if _, err := New(store, nil).RunTenant(ctx, scope); err == nil {
		t.Fatal("running with no assignee reported success")
	}
	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if executions[0].ErrorCategory != ReasonNoAssignee {
		t.Errorf("reason = %q, want %q", executions[0].ErrorCategory, ReasonNoAssignee)
	}
	// A bounded code, not a message: rule authors read these rows.
	if strings.Contains(executions[0].ErrorCategory, " ") {
		t.Errorf("the recorded reason is a message rather than a code: %q",
			executions[0].ErrorCategory)
	}
}

// TestARetryResumesRatherThanRepeats. An execution that created the task and
// then failed to notify must not create a second task on its next attempt.
func TestARetryResumesRatherThanRepeats(t *testing.T) {
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()
	createLead(t, store, "referral")

	engine := New(store, nil)
	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}
	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	execution := executions[0]

	// Rewind it to look like a pass that completed the first action and then
	// failed, which is what a crash between two actions leaves behind.
	execution.Status = domain.ExecutionPending
	execution.ActionsCompleted = 1
	execution.Attempts = 1
	execution.FinishedAt = time.Time{}
	execution.NextAttemptAt = time.Time{}
	if _, err := store.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
		t.Fatalf("rewind execution: %v", err)
	}

	if _, err := engine.RunTenant(ctx, scope); err != nil {
		t.Fatalf("retry: %v", err)
	}
	tasks, _, err := store.ListTasks(ctx, scope, domain.Page{Size: 100},
		domain.TaskFilter{}, domain.SortTaskCreatedDesc)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("a resumed retry created %d tasks, want 1 — it repeated a completed action",
			len(tasks))
	}
}

// TestTheCursorDoesNotAdvancePastAnUnreadableEvent — the next pass must see
// it again, which the idempotency key makes safe.
func TestTheCursorAdvancesOnlyOverHandledEvents(t *testing.T) {
	store, _ := fixture(t)
	ctx := context.Background()
	scope := testScope()
	createLead(t, store, "referral")

	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}
	cursor, err := store.GetWorkflowCursor(ctx, scope)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor.LastEventID == "" {
		t.Error("the cursor did not advance over a handled event")
	}
	if !cursor.RunningUntil.IsZero() {
		t.Error("the pass kept its lease")
	}

	// A second pass with nothing new does nothing.
	second, err := New(store, nil).RunTenant(ctx, scope)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.EventsRead != 0 || second.ExecutionsMade != 0 {
		t.Errorf("a pass with no new events did %+v", second)
	}
}

// TestALeasedTenantIsNotRunTwice.
func TestALeasedTenantIsNotRunTwice(t *testing.T) {
	store, _ := fixture(t)
	ctx := context.Background()
	scope := testScope()

	if _, err := store.ClaimWorkflowCursor(ctx, scope, time.Now().UTC(), time.Hour); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}
	if _, err := New(store, nil).RunTenant(ctx, scope); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("running a leased tenant: %v, want ErrConflict", err)
	}
}

// TestARuleCannotFireOnItsOwnOutput. v1 prevents loops by construction, and
// the construction is checked rather than trusted.
func TestARuleCannotFireOnItsOwnOutput(t *testing.T) {
	if err := domain.AssertTriggersCannotLoop(); err != nil {
		t.Fatalf("the trigger catalogue overlaps what actions produce: %v", err)
	}

	// And behaviourally: a rule that creates a task and an activity produces
	// no further executions, because neither is a trigger.
	store, rule := fixture(t)
	ctx := context.Background()
	scope := testScope()
	createLead(t, store, "referral")

	engine := New(store, nil)
	for pass := 0; pass < 3; pass++ {
		if _, err := engine.RunTenant(ctx, scope); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	executions, err := store.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(executions) != 1 {
		t.Errorf("three passes produced %d executions, want 1 — a rule fired on its own output",
			len(executions))
	}
}

// TestStageChangesTriggerRules, so the catalogue is not just lead.created.
func TestStageChangesTriggerRules(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := testScope()

	rule, err := store.CreateWorkflowRule(ctx, scope, &domain.WorkflowRule{Name: "Negotiation prep"})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	version, err := store.CreateWorkflowRuleVersion(ctx, scope, &domain.WorkflowRuleVersion{
		RuleID:           rule.ID,
		TriggerEventType: domain.EventOpportunityStageChanged,
		Conditions: []domain.WorkflowCondition{
			{Field: "stage", Operator: domain.OpEquals, Value: domain.StageNegotiation},
		},
		Actions: []domain.WorkflowAction{{
			Type: domain.ActionAddActivity, ActivityKind: "note",
			Body: "Deal reached negotiation.",
		}},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rule.Status, rule.ActiveVersionID = domain.WorkflowActive, version.ID
	if _, err := store.UpdateWorkflowRule(ctx, scope, rule); err != nil {
		t.Fatalf("activate: %v", err)
	}

	account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "rep-1"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	opportunity, err := store.CreateOpportunity(ctx, scope, &domain.Opportunity{
		Name: "Renewal", AccountID: account.ID, Amount: 5000,
	})
	if err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	// The pipeline only allows one open stage at a time, so the deal walks
	// there. The first two moves are stage changes too, and the rule's
	// condition is what keeps them from firing.
	for _, stage := range []string{
		domain.StageQualification, domain.StageProposal, domain.StageNegotiation,
	} {
		opportunity.Stage = stage
		updated, err := store.UpdateOpportunity(ctx, scope, opportunity)
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		opportunity = updated
	}

	if _, err := New(store, nil).RunTenant(ctx, scope); err != nil {
		t.Fatalf("run: %v", err)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope,
		domain.SubjectOpportunity, opportunity.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	found := false
	for _, activity := range activities {
		if strings.Contains(activity.Notes, "reached negotiation") {
			found = true
		}
	}
	if !found {
		t.Errorf("a stage-change rule produced no activity: %+v", activities)
	}
}
