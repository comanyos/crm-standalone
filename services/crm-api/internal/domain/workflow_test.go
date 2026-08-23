package domain

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Workflow automation storage, against both implementations.
//
// One test here matters more than the rest: an event claimed once for a rule
// version cannot be claimed again. Outbox delivery is at-least-once, so the
// second arrival of an event is routine — and without that constraint it
// becomes a second follow-up task on a customer's record.

func eachWorkflowStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

func workflowScope(label string) Scope {
	return Scope{
		TenantID: "tenant-wf-" + label + "-" + gmailRun,
		OrgID:    "org-wf-" + label + "-" + gmailRun,
	}
}

// activeRule publishes a rule and activates it, which is the state the engine
// actually reads.
func activeRule(t *testing.T, s Store, scope Scope, actions ...WorkflowAction) (*WorkflowRule, *WorkflowRuleVersion) {
	t.Helper()
	ctx := context.Background()
	if len(actions) == 0 {
		actions = []WorkflowAction{{
			Type: ActionCreateTask, Title: "Follow up", Assignee: AssigneeRecordOwner,
		}}
	}

	rule, err := s.CreateWorkflowRule(ctx, scope, &WorkflowRule{
		Name: "Follow up on new leads", CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	version, err := s.CreateWorkflowRuleVersion(ctx, scope, &WorkflowRuleVersion{
		RuleID: rule.ID, TriggerEventType: EventLeadCreated,
		Actions: actions, CreatedBy: "author-1",
	})
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	rule.Status, rule.ActiveVersionID = WorkflowActive, version.ID
	rule, err = s.UpdateWorkflowRule(ctx, scope, rule)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	return rule, version
}

// TestAnEventIsClaimedOnce is the safety property of the whole feature.
func TestAnEventIsClaimedOnce(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("claim-" + name)
		rule, version := activeRule(t, s, scope)

		claim := func() (*WorkflowExecution, bool, error) {
			return s.ClaimWorkflowExecution(ctx, scope, &WorkflowExecution{
				RuleID: rule.ID, RuleVersionID: version.ID,
				SourceEventID: "evt-1", SourceEventType: EventLeadCreated,
				SubjectType: SubjectLead, SubjectID: "lead-1",
			})
		}

		first, created, err := claim()
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if !created || first == nil {
			t.Fatal("the first claim of an event did not create an execution")
		}

		second, created, err := claim()
		if err != nil {
			t.Fatalf("second claim: %v", err)
		}
		if created || second != nil {
			t.Error("the same event was claimed twice; a customer would get two follow-ups")
		}

		executions, err := s.ListWorkflowExecutions(ctx, scope, rule.ID, 0)
		if err != nil {
			t.Fatalf("list executions: %v", err)
		}
		if len(executions) != 1 {
			t.Errorf("ledger holds %d executions for one event, want 1", len(executions))
		}
	})
}

// TestConcurrentClaimsProduceOneExecution is the same property under the
// access pattern that produces it: two runners handed the same delivery.
func TestConcurrentClaimsProduceOneExecution(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("claim-race-" + name)
		rule, version := activeRule(t, s, scope)

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
				_, created, err := s.ClaimWorkflowExecution(ctx, scope, &WorkflowExecution{
					RuleID: rule.ID, RuleVersionID: version.ID, SourceEventID: "evt-race",
				})
				if err == nil && created {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if winners != 1 {
			t.Errorf("%d of %d concurrent claims created an execution, want exactly 1", winners, racers)
		}
	})
}

// TestADifferentVersionClaimsTheSameEvent. The key includes the version on
// purpose: republishing a rule means its new version has genuinely not seen
// the events the old one did.
func TestADifferentVersionClaimsTheSameEvent(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("versions-" + name)
		rule, first := activeRule(t, s, scope)

		second, err := s.CreateWorkflowRuleVersion(ctx, scope, &WorkflowRuleVersion{
			RuleID: rule.ID, TriggerEventType: EventLeadCreated,
			Actions: []WorkflowAction{{Type: ActionNotify, Title: "New lead", Assignee: AssigneeRecordOwner}},
		})
		if err != nil {
			t.Fatalf("publish second version: %v", err)
		}
		if second.Version != 2 {
			t.Errorf("second version numbered %d, want 2", second.Version)
		}

		if _, created, err := s.ClaimWorkflowExecution(ctx, scope, &WorkflowExecution{
			RuleID: rule.ID, RuleVersionID: first.ID, SourceEventID: "evt-1",
		}); err != nil || !created {
			t.Fatalf("claim for the first version: created=%v err=%v", created, err)
		}
		if _, created, err := s.ClaimWorkflowExecution(ctx, scope, &WorkflowExecution{
			RuleID: rule.ID, RuleVersionID: second.ID, SourceEventID: "evt-1",
		}); err != nil || !created {
			t.Fatalf("claim for the second version: created=%v err=%v", created, err)
		}
	})
}

// TestVersionsAreImmutableAndNumbered: an execution records which version it
// ran, so the ledger can answer "why did this fire" months later.
func TestVersionsAreImmutableAndNumbered(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("immutable-" + name)
		rule, first := activeRule(t, s, scope)

		for want := 2; want <= 3; want++ {
			version, err := s.CreateWorkflowRuleVersion(ctx, scope, &WorkflowRuleVersion{
				RuleID: rule.ID, TriggerEventType: EventLeadCreated,
				Actions: []WorkflowAction{{Type: ActionNotify, Title: "New lead", Assignee: AssigneeRecordOwner}},
			})
			if err != nil {
				t.Fatalf("publish version %d: %v", want, err)
			}
			if version.Version != want {
				t.Errorf("version numbered %d, want %d", version.Version, want)
			}
		}

		// The first version still says exactly what it said.
		stored, err := s.GetWorkflowRuleVersion(ctx, scope, first.ID)
		if err != nil {
			t.Fatalf("read the first version: %v", err)
		}
		if len(stored.Actions) != 1 || stored.Actions[0].Type != ActionCreateTask {
			t.Errorf("the first version changed under a later publish: %+v", stored.Actions)
		}

		versions, err := s.ListWorkflowRuleVersions(ctx, scope, rule.ID)
		if err != nil {
			t.Fatalf("list versions: %v", err)
		}
		if len(versions) != 3 || versions[0].Version != 3 {
			t.Errorf("versions = %d rows, newest %d", len(versions), versions[0].Version)
		}
	})
}

// TestOnlyActiveRulesMatch. Draft, paused and archived rules exist so an
// author can stop one firing without deleting the record of what it did.
func TestOnlyActiveRulesMatch(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("states-" + name)
		rule, _ := activeRule(t, s, scope)

		matching, err := s.ActiveWorkflowVersionsFor(ctx, scope, EventLeadCreated)
		if err != nil {
			t.Fatalf("match active: %v", err)
		}
		if len(matching) != 1 {
			t.Fatalf("an active rule matched %d versions, want 1", len(matching))
		}

		for _, status := range []string{WorkflowPaused, WorkflowDraft, WorkflowArchived} {
			rule.Status = status
			if _, err := s.UpdateWorkflowRule(ctx, scope, rule); err != nil {
				t.Fatalf("set %s: %v", status, err)
			}
			matching, err := s.ActiveWorkflowVersionsFor(ctx, scope, EventLeadCreated)
			if err != nil {
				t.Fatalf("match %s: %v", status, err)
			}
			if len(matching) != 0 {
				t.Errorf("a %s rule still matched events", status)
			}
		}

		// Resuming works without re-picking a version, because pausing keeps
		// active_version_id.
		rule.Status = WorkflowActive
		resumed, err := s.UpdateWorkflowRule(ctx, scope, rule)
		if err != nil {
			t.Fatalf("resume: %v", err)
		}
		if resumed.ActiveVersionID == "" {
			t.Error("resuming a paused rule lost its published version")
		}
	})
}

// TestATriggerOnlyMatchesItsOwnEvent.
func TestATriggerOnlyMatchesItsOwnEvent(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("trigger-" + name)
		activeRule(t, s, scope)

		matching, err := s.ActiveWorkflowVersionsFor(ctx, scope, EventOpportunityClosedWon)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		if len(matching) != 0 {
			t.Errorf("a lead.created rule matched an opportunity event")
		}
	})
}

// TestExecutionsRetryUntilTheirBudgetIsSpent.
func TestExecutionsRetryUntilTheirBudgetIsSpent(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("retry-" + name)
		rule, version := activeRule(t, s, scope)
		now := time.Now().UTC()

		execution, _, err := s.ClaimWorkflowExecution(ctx, scope, &WorkflowExecution{
			RuleID: rule.ID, RuleVersionID: version.ID, SourceEventID: "evt-retry",
		})
		if err != nil {
			t.Fatalf("claim: %v", err)
		}

		// A failed attempt schedules a retry and stays due once it elapses.
		execution.Attempts = 1
		execution.Status = ExecutionPending
		execution.ErrorCategory = "action_failed"
		execution.NextAttemptAt = now.Add(time.Minute)
		if _, err := s.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
			t.Fatalf("record attempt: %v", err)
		}

		due, err := s.DueWorkflowExecutions(ctx, scope, now, 0)
		if err != nil {
			t.Fatalf("due before backoff: %v", err)
		}
		if len(due) != 0 {
			t.Error("an execution inside its backoff was offered for retry")
		}
		due, err = s.DueWorkflowExecutions(ctx, scope, now.Add(2*time.Minute), 0)
		if err != nil {
			t.Fatalf("due after backoff: %v", err)
		}
		if len(due) != 1 {
			t.Errorf("an execution past its backoff was not offered: %d due", len(due))
		}

		// A terminal execution is never offered again.
		execution.Attempts = MaxExecutionAttempts
		execution.Status = ExecutionFailed
		execution.NextAttemptAt = time.Time{}
		execution.FinishedAt = now
		if _, err := s.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
			t.Fatalf("fail: %v", err)
		}
		if !execution.Exhausted() {
			t.Error("an execution at the attempt limit does not report itself exhausted")
		}
		due, err = s.DueWorkflowExecutions(ctx, scope, now.Add(time.Hour), 0)
		if err != nil {
			t.Fatalf("due after failure: %v", err)
		}
		if len(due) != 0 {
			t.Errorf("a failed execution was offered for retry: %d due", len(due))
		}
	})
}

// TestTheOutboxCursorAdvancesWithoutSkippingATie. Several events routinely
// share a millisecond, and a batch that ended inside such a group would
// otherwise repeat or skip them.
func TestTheOutboxCursorAdvancesWithoutSkippingATie(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("cursor-" + name)

		for i := 0; i < 5; i++ {
			if _, err := EnqueueEvent(ctx, s, scope, Envelope{
				ID: "evt-" + string(rune('a'+i)), Type: EventLeadCreated, TenantID: scope.TenantID,
				CorrelationID: "corr", SourceApp: SourceAppCRM, EntityType: SubjectLead,
				EntityID: "lead-1", SourceVersion: int64(i + 1), Actor: "system",
				OccurredAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("enqueue %d: %v", i, err)
			}
		}

		var cursor WorkflowCursor
		seen := map[string]int{}
		for pass := 0; pass < 5; pass++ {
			batch, err := s.OutboxEventsAfter(ctx, scope, cursor, 2)
			if err != nil {
				t.Fatalf("read batch %d: %v", pass, err)
			}
			if len(batch) == 0 {
				break
			}
			for _, event := range batch {
				seen[event.ID]++
			}
			last := batch[len(batch)-1]
			cursor.LastEventCreatedAt, cursor.LastEventID = last.CreatedAt, last.ID
		}

		if len(seen) != 5 {
			t.Errorf("the cursor walked %d of 5 events", len(seen))
		}
		for id, count := range seen {
			if count != 1 {
				t.Errorf("event %s was read %d times", id, count)
			}
		}
	})
}

// TestNotificationsAreReadableOnlyByTheirRecipient. They are the one entity
// here an ordinary user reads.
func TestNotificationsAreReadableOnlyByTheirRecipient(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("notify-" + name)

		mine, err := s.CreateNotification(ctx, scope, &Notification{
			RecipientID: "user-a", Title: "A lead needs attention",
			SubjectType: SubjectLead, SubjectID: "lead-1",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := s.CreateNotification(ctx, scope, &Notification{
			RecipientID: "user-b", Title: "Not for you",
		}); err != nil {
			t.Fatalf("create other: %v", err)
		}

		rows, err := s.ListNotifications(ctx, scope, "user-a", false, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(rows) != 1 || rows[0].Title != "A lead needs attention" {
			t.Fatalf("user-a sees %+v", rows)
		}

		// Marking somebody else's is indistinguishable from one that is not
		// there — it must not confirm the id exists.
		if _, err := s.MarkNotificationRead(ctx, scope, "user-b", mine.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("marking another user's notification: %v, want ErrNotFound", err)
		}
		read, err := s.MarkNotificationRead(ctx, scope, "user-a", mine.ID)
		if err != nil {
			t.Fatalf("mark read: %v", err)
		}
		if read.ReadAt.IsZero() {
			t.Error("the notification was not marked read")
		}

		unread, err := s.ListNotifications(ctx, scope, "user-a", true, 0)
		if err != nil {
			t.Fatalf("list unread: %v", err)
		}
		if len(unread) != 0 {
			t.Errorf("a read notification is still in the unread list")
		}

		// A blank recipient would list the tenant's.
		if _, err := s.ListNotifications(ctx, scope, "", false, 0); !errors.Is(err, ErrInvalid) {
			t.Errorf("listing with no recipient: %v, want ErrInvalid", err)
		}
	})
}

// TestWorkflowIsTenantScoped: rules describe how an organization operates,
// and executions say what it did.
func TestWorkflowIsTenantScoped(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := workflowScope("wf-victim-" + name)
		attacker := workflowScope("wf-attacker-" + name)
		rule, version := activeRule(t, s, victim)

		if _, err := s.GetWorkflowRule(ctx, attacker, rule.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-tenant rule read: %v, want ErrNotFound", err)
		}
		if _, err := s.GetWorkflowRuleVersion(ctx, attacker, version.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-tenant version read: %v, want ErrNotFound", err)
		}
		rules, err := s.ListWorkflowRules(ctx, attacker, true)
		if err != nil {
			t.Fatalf("list as attacker: %v", err)
		}
		if len(rules) != 0 {
			t.Errorf("attacker lists %d of another tenant's rules", len(rules))
		}
		matching, err := s.ActiveWorkflowVersionsFor(ctx, attacker, EventLeadCreated)
		if err != nil {
			t.Fatalf("match as attacker: %v", err)
		}
		if len(matching) != 0 {
			t.Errorf("attacker matched %d of another tenant's rules", len(matching))
		}
		// A version cannot be attached to another tenant's rule.
		if _, err := s.CreateWorkflowRuleVersion(ctx, attacker, &WorkflowRuleVersion{
			RuleID: rule.ID, TriggerEventType: EventLeadCreated,
			Actions: []WorkflowAction{{Type: ActionNotify, Title: "x", Assignee: AssigneeRecordOwner}},
		}); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-tenant version publish: %v, want ErrNotFound", err)
		}
	})
}

// TestWorkflowRejectsAnEmptyScope.
func TestWorkflowRejectsAnEmptyScope(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		var none Scope
		now := time.Now().UTC()

		if _, err := s.CreateWorkflowRule(ctx, none, &WorkflowRule{Name: "x"}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("CreateWorkflowRule: %v, want ErrNoTenant", err)
		}
		if _, err := s.ListWorkflowRules(ctx, none, false); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListWorkflowRules: %v, want ErrNoTenant", err)
		}
		if _, err := s.ActiveWorkflowVersionsFor(ctx, none, EventLeadCreated); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ActiveWorkflowVersionsFor: %v, want ErrNoTenant", err)
		}
		if _, _, err := s.ClaimWorkflowExecution(ctx, none, &WorkflowExecution{}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ClaimWorkflowExecution: %v, want ErrNoTenant", err)
		}
		if _, err := s.DueWorkflowExecutions(ctx, none, now, 0); !errors.Is(err, ErrNoTenant) {
			t.Errorf("DueWorkflowExecutions: %v, want ErrNoTenant", err)
		}
		if _, err := s.CreateNotification(ctx, none, &Notification{
			RecipientID: "u", Title: "x",
		}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("CreateNotification: %v, want ErrNoTenant", err)
		}
		if _, err := s.ListNotifications(ctx, none, "u", false, 0); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListNotifications: %v, want ErrNoTenant", err)
		}
		if _, err := s.GetWorkflowCursor(ctx, none); !errors.Is(err, ErrNoTenant) {
			t.Errorf("GetWorkflowCursor: %v, want ErrNoTenant", err)
		}
	})
}

// TestTheCursorLeaseAdmitsOneRunner.
func TestTheCursorLeaseAdmitsOneRunner(t *testing.T) {
	eachWorkflowStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := workflowScope("cursor-lease-" + name)
		now := time.Now().UTC()

		if _, err := s.ClaimWorkflowCursor(ctx, scope, now, time.Minute); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if _, err := s.ClaimWorkflowCursor(ctx, scope, now, time.Minute); !errors.Is(err, ErrConflict) {
			t.Fatalf("second claim: %v, want ErrConflict", err)
		}
		if _, err := s.ClaimWorkflowCursor(ctx, scope, now.Add(2*time.Minute), time.Minute); err != nil {
			t.Fatalf("claim after expiry: %v", err)
		}
	})
}
