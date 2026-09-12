// Package workflow runs the CRM's automation: it reads the durable outbox,
// matches events against active rules, and performs their actions.
//
// The whole design turns on one fact stated in docs/WORKFLOW_AUTOMATION.md:
// outbox delivery is at-least-once, so every event reaches this engine more
// than once as a matter of course. An engine that treated a second delivery
// as new would give a customer two follow-up calls. So nothing here decides
// whether work has been done by remembering — it claims an execution row
// whose unique key is (tenant, rule version, source event), and only the
// caller that wins the insert does anything.
//
// The second rule, from the same contract: the event payload is a snapshot
// for evaluating conditions, never a source of truth for acting. Every action
// re-reads its target under the execution's own tenant scope before it
// writes, because the record may have changed — or been deleted — between the
// event and the execution.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/metrics"
)

const (
	// EventsPerBatch bounds how much of a tenant's backlog one pass reads.
	EventsPerBatch = 100
	// RunLease is how long one runner may hold a tenant's automation.
	RunLease = 2 * time.Minute
	// retryBackoff is the delay before a failed execution is retried. It
	// doubles per attempt; the point is to let a transient failure clear
	// without hammering, not to model an outage.
	retryBackoff = 30 * time.Second
)

// The bounded error categories an execution may record. They exist so a rule
// author reading the ledger sees something actionable, and so no message from
// a failing action — which may hold a customer's data — reaches the row.
const (
	ReasonRecordGone    = "record_gone"
	ReasonNoAssignee    = "no_assignee"
	ReasonActionFailed  = "action_failed"
	ReasonRuleGone      = "rule_version_gone"
	ReasonInvalidAction = "invalid_action"
	// ReasonConsentWithdrawn and ReasonNoAddress record why a message could
	// not be drafted. Both are terminal: consent does not return by waiting,
	// and an address does not appear.
	ReasonConsentWithdrawn = "consent_withdrawn"
	ReasonNoAddress        = "no_messageable_address"
	ReasonApprovalRejected = "approval_rejected"
)

var errApprovalPending = errors.New("workflow approval is pending")

// Engine evaluates events and performs actions.
type Engine struct {
	store   domain.Store
	webhook *WebhookSender
	now     func() time.Time
	log     *slog.Logger
}

// New wires the engine.
func New(store domain.Store, log *slog.Logger) *Engine {
	return NewWithWebhook(store, nil, log)
}

// NewWithWebhook wires the optional signed webhook delivery service. Keeping
// New as a wrapper preserves the small test and offline deployments that do
// not have a secret-encryption key configured.
func NewWithWebhook(store domain.Store, webhook *WebhookSender, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: store, webhook: webhook, now: func() time.Time { return time.Now().UTC() }, log: log}
}

// Result reports what one pass over one tenant did.
type Result struct {
	EventsRead       int
	ExecutionsMade   int
	ActionsPerformed int
	Failures         int
}

// RunTenant performs one pass for one tenant: read new events, claim
// executions for matching rules, then run everything that is due.
//
// It claims the tenant's cursor first and always releases it, including on
// failure — a tenant left leased by a failed pass would have no automation
// until the lease expired.
func (e *Engine) RunTenant(ctx context.Context, scope domain.Scope) (Result, error) {
	cursor, err := e.store.ClaimWorkflowCursor(ctx, scope, e.now(), RunLease)
	if err != nil {
		// ErrConflict means another runner holds it, which is the normal
		// outcome of running more than one replica.
		return Result{}, err
	}

	result, runErr := e.run(ctx, scope, cursor)

	cursor.RunningUntil = time.Time{}
	if err := e.store.SaveWorkflowCursor(ctx, scope, cursor); err != nil {
		e.log.Error("could not save the workflow cursor", "error", err)
		if runErr == nil {
			runErr = err
		}
	}

	outcome := "ran"
	if runErr != nil {
		outcome = "failed"
	}
	metrics.WorkflowPassesTotal.WithLabelValues(outcome).Inc()
	return result, runErr
}

func (e *Engine) run(
	ctx context.Context, scope domain.Scope, cursor *domain.WorkflowCursor,
) (Result, error) {
	var result Result

	// Turn due wall-clock schedules into ordinary execution rows before
	// draining event-triggered work. The execution's source id contains the
	// scheduled instant, so a crash before advancing the schedule replays into
	// the same unique key instead of creating a second run.
	if made, err := e.claimSchedules(ctx, scope); err != nil {
		return result, err
	} else {
		result.ExecutionsMade += made
	}

	events, err := e.store.OutboxEventsAfter(ctx, scope, *cursor, EventsPerBatch)
	if err != nil {
		return result, err
	}
	for _, event := range events {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		made, err := e.claimFor(ctx, scope, event)
		if err != nil {
			// The cursor is deliberately not advanced past an event that
			// could not be evaluated: the next pass sees it again, and the
			// idempotency key makes that safe.
			return result, err
		}
		result.EventsRead++
		result.ExecutionsMade += made
		cursor.LastEventCreatedAt, cursor.LastEventID = event.CreatedAt, event.ID
	}

	// Run what is due, including retries scheduled by earlier passes.
	due, err := e.store.DueWorkflowExecutions(ctx, scope, e.now(), EventsPerBatch)
	if err != nil {
		return result, err
	}
	for _, execution := range due {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		performed, err := e.perform(ctx, scope, execution)
		result.ActionsPerformed += performed
		if err != nil {
			// One rule's failure must not stop the tenant's other rules, so
			// the loop continues. But the pass is not a success either: the
			// caller — and the operator reading its log — needs to know that
			// automation this tenant was promised did not happen.
			result.Failures++
			e.log.Warn("a workflow execution failed",
				"rule_id", execution.RuleID, "execution_id", execution.ID, "error", err)
		}
	}
	if result.Failures > 0 {
		return result, fmt.Errorf("%d workflow executions failed", result.Failures)
	}
	return result, nil
}

func (e *Engine) claimSchedules(ctx context.Context, scope domain.Scope) (int, error) {
	now := e.now()
	schedules, err := e.store.DueWorkflowSchedules(ctx, scope, now, EventsPerBatch)
	if err != nil {
		return 0, err
	}
	made := 0
	for _, schedule := range schedules {
		rule, err := e.store.GetWorkflowRule(ctx, scope, schedule.RuleID)
		if err != nil {
			return made, err
		}
		if rule.Status != domain.WorkflowActive || rule.ActiveVersionID == "" {
			continue
		}
		version, err := e.store.GetWorkflowRuleVersion(ctx, scope, rule.ActiveVersionID)
		if err != nil {
			return made, err
		}
		if version.TriggerObjectType != schedule.SubjectType {
			// A newly published rule version may target a different object. An
			// old schedule must not fail every automation pass forever; pause it
			// for an administrator to review instead.
			schedule.Status = domain.SchedulePaused
			if _, err := e.store.UpdateWorkflowSchedule(ctx, scope, schedule); err != nil {
				return made, fmt.Errorf("pause incompatible workflow schedule: %w", err)
			}
			continue
		}
		sourceID := "schedule:" + schedule.ID + ":" + schedule.NextRunAt.UTC().Format(time.RFC3339Nano)
		_, created, err := e.store.ClaimWorkflowExecution(ctx, scope, &domain.WorkflowExecution{
			RuleID: rule.ID, RuleVersionID: version.ID, SourceEventID: sourceID,
			SourceEventType: "schedule.triggered", SubjectType: schedule.SubjectType,
			SubjectID: schedule.SubjectID, Status: domain.ExecutionPending,
		})
		if err != nil {
			return made, err
		}
		if created {
			made++
		}
		next, recurring := domain.NextScheduleOccurrence(schedule.NextRunAt, schedule.TimeZone, schedule.Recurrence, schedule.Interval)
		// Run one missed occurrence after an outage, then advance directly to
		// the next future wall-clock slot. Replaying every missed daily run at
		// ten-second intervals would create a catch-up storm of stale work.
		for recurring && !next.After(now) {
			next, recurring = domain.NextScheduleOccurrence(next, schedule.TimeZone, schedule.Recurrence, schedule.Interval)
		}
		status := domain.ScheduleActive
		if !recurring || (!schedule.EndsAt.IsZero() && next.After(schedule.EndsAt)) {
			status, next = domain.ScheduleCompleted, time.Time{}
		}
		if _, err := e.store.AdvanceWorkflowSchedule(ctx, scope, schedule.ID, schedule.NextRunAt, next, status); err != nil && !errors.Is(err, domain.ErrConflict) {
			return made, err
		}
	}
	return made, nil
}

// claimFor creates an execution for each active rule whose trigger and
// conditions match this event, and reports how many it claimed.
func (e *Engine) claimFor(
	ctx context.Context, scope domain.Scope, event *domain.OutboxEvent,
) (int, error) {
	versions, err := e.store.ActiveWorkflowVersionsFor(ctx, scope, event.Envelope.Type)
	if err != nil {
		return 0, err
	}
	if len(versions) == 0 {
		return 0, nil
	}

	// Decoded once per event rather than per rule.
	var payload map[string]any
	if len(event.Envelope.Payload) > 0 {
		if err := json.Unmarshal(event.Envelope.Payload, &payload); err != nil {
			// An undecodable payload is not a reason to stop the whole pass:
			// it is one malformed event, and blocking on it would stop every
			// rule in the tenant.
			e.log.Warn("workflow could not decode an event payload", "event_type", event.Envelope.Type)
			return 0, nil
		}
	}
	fields := domain.WorkflowEventFields(event.Envelope.Type, payload)

	claimed := 0
	for _, version := range versions {
		status := domain.ExecutionPending
		if !version.Matches(fields) {
			// Recorded rather than discarded. "Why didn't my rule fire" is
			// the question authors actually ask, and an engine that keeps no
			// record of considering an event cannot answer it.
			status = domain.ExecutionSkipped
		}

		execution, created, err := e.store.ClaimWorkflowExecution(ctx, scope, &domain.WorkflowExecution{
			RuleID:          version.RuleID,
			RuleVersionID:   version.ID,
			SourceEventID:   event.Envelope.ID,
			SourceEventType: event.Envelope.Type,
			SubjectType:     version.TriggerObjectType,
			SubjectID:       event.Envelope.EntityID,
			Status:          status,
		})
		if err != nil {
			return claimed, err
		}
		if !created {
			// Already claimed by an earlier delivery of this event. The
			// expected case.
			metrics.WorkflowExecutionsTotal.WithLabelValues("duplicate_suppressed").Inc()
			continue
		}
		claimed++
		if status == domain.ExecutionSkipped {
			execution.FinishedAt = e.now()
			if _, err := e.store.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
				return claimed, err
			}
			metrics.WorkflowExecutionsTotal.WithLabelValues("skipped").Inc()
		}
	}
	return claimed, nil
}

// perform runs one execution's remaining actions and records the outcome.
//
// It resumes rather than repeats: actions_completed says how many succeeded
// on earlier attempts, so a retry after a failure part-way through does not
// create the task a second time.
func (e *Engine) perform(
	ctx context.Context, scope domain.Scope, execution *domain.WorkflowExecution,
) (int, error) {
	version, err := e.store.GetWorkflowRuleVersion(ctx, scope, execution.RuleVersionID)
	if err != nil {
		// The version an execution names is immutable and never deleted, so
		// this is a corrupt ledger rather than a race. It is terminal:
		// retrying cannot make a missing version appear.
		return 0, e.finish(ctx, scope, execution, domain.ExecutionFailed, ReasonRuleGone, 0)
	}

	performed := 0
	for index := execution.ActionsCompleted; index < len(version.Actions); index++ {
		action := version.Actions[index]
		now := e.now()

		// A zero NextAttemptAt means this step has not had its sequence delay
		// scheduled yet. A non-zero value is either the durable wake-up written
		// here or a retry written by retryOrFail. DueWorkflowExecutions only
		// returns it once that instant arrives, so clearing it immediately before
		// the attempt makes the distinction unambiguous and restart-safe.
		if action.DelayMinutes > 0 && execution.NextAttemptAt.IsZero() {
			execution.Status = domain.ExecutionPending
			execution.NextAttemptAt = now.Add(time.Duration(action.DelayMinutes) * time.Minute)
			if _, err := e.store.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
				return performed, err
			}
			return performed, nil
		}
		if !execution.NextAttemptAt.IsZero() && now.Before(execution.NextAttemptAt) {
			return performed, nil
		}

		execution.NextAttemptAt = time.Time{}
		execution.Attempts++
		execution.Status = domain.ExecutionRunning
		if execution.StartedAt.IsZero() {
			execution.StartedAt = now
		}
		if _, err := e.store.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
			return performed, err
		}

		reason, err := e.act(ctx, scope, version, action, execution)
		if errors.Is(err, errApprovalPending) {
			execution.Status = domain.ExecutionWaitingApproval
			execution.ErrorCategory = ""
			execution.NextAttemptAt = time.Time{}
			execution.Attempts = 0
			if _, saveErr := e.store.UpdateWorkflowExecution(ctx, scope, execution); saveErr != nil {
				return performed, saveErr
			}
			return performed, nil
		}
		if err != nil {
			e.log.Warn("a workflow action failed", "reason", reason)
			return performed, e.retryOrFail(ctx, scope, execution, reason)
		}
		performed++
		execution.ActionsCompleted = index + 1
		// Retry attempts belong to one step. A later delayed step gets its own
		// budget instead of inheriting attempts from work that already succeeded.
		execution.Attempts = 0
		execution.NextAttemptAt = time.Time{}
		// Persisted per action, so a crash between two actions still resumes
		// at the right one rather than repeating the first.
		if _, err := e.store.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
			return performed, err
		}
	}

	metrics.WorkflowExecutionsTotal.WithLabelValues("succeeded").Inc()
	return performed, e.finish(ctx, scope, execution, domain.ExecutionSucceeded, "", execution.ActionsCompleted)
}

// retryOrFail schedules another attempt, or gives up once the budget is spent.
func (e *Engine) retryOrFail(
	ctx context.Context, scope domain.Scope, execution *domain.WorkflowExecution, reason string,
) error {
	if execution.Exhausted() || terminal(reason) {
		metrics.WorkflowExecutionsTotal.WithLabelValues("failed").Inc()
		return e.finish(ctx, scope, execution, domain.ExecutionFailed, reason, execution.ActionsCompleted)
	}
	execution.Status = domain.ExecutionPending
	execution.ErrorCategory = reason
	execution.NextAttemptAt = e.now().Add(retryBackoff * time.Duration(1<<uint(execution.Attempts-1)))
	metrics.WorkflowExecutionsTotal.WithLabelValues("retrying").Inc()
	_, err := e.store.UpdateWorkflowExecution(ctx, scope, execution)
	if err != nil {
		return err
	}
	return fmt.Errorf("workflow action failed: %s", reason)
}

// terminal reports whether retrying could possibly help.
//
// A deleted record and a malformed action will fail the same way on every
// attempt, and retrying them three times only delays the failure a rule
// author needs to see.
func terminal(reason string) bool {
	switch reason {
	case ReasonRecordGone, ReasonInvalidAction, ReasonRuleGone,
		// A withdrawal does not become a grant by waiting, and an address
		// does not appear. Retrying either would mean three attempts to
		// contact somebody who said no — the precise behaviour this ledger
		// exists to make impossible.
		ReasonConsentWithdrawn, ReasonNoAddress, ReasonApprovalRejected:
		return true
	default:
		return false
	}
}

func (e *Engine) finish(
	ctx context.Context, scope domain.Scope, execution *domain.WorkflowExecution,
	status, reason string, completed int,
) error {
	execution.Status = status
	execution.ErrorCategory = reason
	execution.ActionsCompleted = completed
	execution.NextAttemptAt = time.Time{}
	execution.FinishedAt = e.now()
	if _, err := e.store.UpdateWorkflowExecution(ctx, scope, execution); err != nil {
		return err
	}
	if status == domain.ExecutionFailed {
		return fmt.Errorf("workflow execution failed: %s", reason)
	}
	return nil
}
