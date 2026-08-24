package workflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// Performing a rule's actions.
//
// The rule this file exists to enforce comes from the design contract: "the
// event payload is a snapshot for rule evaluation only; actions must re-read
// their target under the execution tenant scope before they mutate it."
//
// That is not defensive coding. An execution can run minutes after its event
// — after a retry, after a backlog, after a restart — and in between the deal
// may have been reassigned, or the lead deleted. Acting on the snapshot would
// assign work to somebody who no longer owns the record, or attach a task to
// a row that is gone. So every action starts by reading the subject, and a
// subject that has disappeared is a terminal outcome rather than a retry:
// it will not come back.

// act performs one action and returns a bounded reason on failure.
func (e *Engine) act(
	ctx context.Context, scope domain.Scope, version *domain.WorkflowRuleVersion,
	action domain.WorkflowAction, execution *domain.WorkflowExecution,
) (string, error) {
	subject, reason, err := e.resolveSubject(ctx, scope, execution)
	if err != nil {
		return reason, err
	}

	switch action.Type {
	case domain.ActionCreateTask:
		return e.createTask(ctx, scope, version, action, execution, subject)
	case domain.ActionAddActivity:
		return e.addActivity(ctx, scope, action, execution)
	case domain.ActionNotify:
		return e.notify(ctx, scope, version, action, execution, subject)
	default:
		// A version that stored an action type this build does not know is
		// terminal: it will not become known by retrying.
		return ReasonInvalidAction, errors.New("unknown action type")
	}
}

// subjectSnapshot is what actions need about the record the event concerned,
// read fresh rather than taken from the event.
type subjectSnapshot struct {
	Type    string
	ID      string
	OwnerID string
}

// resolveSubject re-reads the record under the execution's own tenant scope.
func (e *Engine) resolveSubject(
	ctx context.Context, scope domain.Scope, execution *domain.WorkflowExecution,
) (subjectSnapshot, string, error) {
	snapshot := subjectSnapshot{Type: execution.SubjectType, ID: execution.SubjectID}
	if snapshot.ID == "" {
		return snapshot, ReasonRecordGone, errors.New("execution names no subject")
	}

	switch execution.SubjectType {
	case domain.SubjectLead:
		lead, err := e.store.GetLead(ctx, scope, execution.SubjectID)
		if err != nil {
			return snapshot, gone(err), err
		}
		snapshot.OwnerID = lead.OwnerID
	case domain.SubjectOpportunity:
		opportunity, err := e.store.GetOpportunity(ctx, scope, execution.SubjectID)
		if err != nil {
			return snapshot, gone(err), err
		}
		// An Opportunity carries no owner of its own in this schema, so the
		// owner is the Account's — the same resolution the forecast uses.
		if opportunity.AccountID != "" {
			account, err := e.store.GetAccount(ctx, scope, opportunity.AccountID)
			if err == nil {
				snapshot.OwnerID = account.OwnerID
			}
		}
	case domain.SubjectAccount:
		account, err := e.store.GetAccount(ctx, scope, execution.SubjectID)
		if err != nil {
			return snapshot, gone(err), err
		}
		snapshot.OwnerID = account.OwnerID
	case domain.SubjectContact:
		if _, err := e.store.GetContact(ctx, scope, execution.SubjectID); err != nil {
			return snapshot, gone(err), err
		}
	default:
		return snapshot, ReasonInvalidAction, errors.New("unknown subject type")
	}
	return snapshot, "", nil
}

// gone maps a read failure onto a bounded reason. A record that is not there
// is terminal; anything else is worth another attempt.
func gone(err error) string {
	if errors.Is(err, domain.ErrNotFound) {
		return ReasonRecordGone
	}
	return ReasonActionFailed
}

// assigneeFor resolves who an action is for, at execution time.
func assigneeFor(action domain.WorkflowAction, subject subjectSnapshot) string {
	switch action.Assignee {
	case domain.AssigneeFixedUser:
		return strings.TrimSpace(action.AssigneeID)
	case domain.AssigneeRecordOwner:
		return strings.TrimSpace(subject.OwnerID)
	default:
		return ""
	}
}

// createTask adds a task, attached to the record the event was about.
func (e *Engine) createTask(
	ctx context.Context, scope domain.Scope, version *domain.WorkflowRuleVersion,
	action domain.WorkflowAction, execution *domain.WorkflowExecution, subject subjectSnapshot,
) (string, error) {
	assignee := assigneeFor(action, subject)
	if assignee == "" {
		// A record whose owner has been cleared leaves nobody to do the work.
		// Terminal rather than retried: the owner will not appear by itself,
		// and the rule author needs to see it.
		return ReasonNoAssignee, errors.New("no assignee could be resolved")
	}

	priority := action.Priority
	if priority == "" {
		priority = domain.TaskPriorityNormal
	}
	task := &domain.Task{
		Title:       action.Title,
		Description: action.Body,
		Priority:    priority,
		AssigneeID:  assignee,
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		// created_by names the rule, not a person. A task nobody typed
		// should not appear to have been typed by somebody.
		CreatedBy: workflowActor(version.RuleID),
	}
	if action.DueInDays > 0 {
		task.DueAt = e.now().Add(time.Duration(action.DueInDays) * 24 * time.Hour)
	}
	if _, err := e.store.CreateTask(ctx, scope, task); err != nil {
		return actionReason(err), err
	}
	return "", nil
}

// addActivity logs an activity against the record.
func (e *Engine) addActivity(
	ctx context.Context, scope domain.Scope, action domain.WorkflowAction,
	execution *domain.WorkflowExecution,
) (string, error) {
	if _, err := e.store.CreateActivity(ctx, scope, &domain.Activity{
		Kind:        action.ActivityKind,
		SubjectType: execution.SubjectType,
		SubjectID:   execution.SubjectID,
		Notes:       action.Body,
		ActorID:     workflowActor(execution.RuleID),
		OccurredAt:  e.now(),
	}); err != nil {
		return actionReason(err), err
	}
	return "", nil
}

// notify creates an in-app notification.
func (e *Engine) notify(
	ctx context.Context, scope domain.Scope, version *domain.WorkflowRuleVersion,
	action domain.WorkflowAction, execution *domain.WorkflowExecution, subject subjectSnapshot,
) (string, error) {
	recipient := assigneeFor(action, subject)
	if recipient == "" {
		return ReasonNoAssignee, errors.New("no recipient could be resolved")
	}
	if _, err := e.store.CreateNotification(ctx, scope, &domain.Notification{
		RecipientID: recipient,
		Kind:        "workflow",
		Title:       action.Title,
		Body:        action.Body,
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		RuleID:      version.RuleID,
	}); err != nil {
		return actionReason(err), err
	}
	return "", nil
}

// workflowActor is the attribution automation writes.
//
// Naming the rule rather than a person is deliberate: a task nobody typed
// must not appear to have been typed by somebody, and a user who finds work
// assigned to them needs to be able to ask which rule did it.
func workflowActor(ruleID string) string { return "workflow:" + ruleID }

// actionReason maps a store failure onto a bounded reason. An invalid record
// is terminal — the action is malformed and will be malformed next time.
func actionReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return ReasonRecordGone
	case errors.Is(err, domain.ErrInvalid):
		return ReasonInvalidAction
	default:
		return ReasonActionFailed
	}
}
