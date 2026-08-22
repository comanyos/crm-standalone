package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Task is a first-class entity rather than an Activity kind (task.md §5.1).
//
// The distinction is not taxonomy. An Activity records what happened and is
// immutable once logged — that immutability is the whole reason it is worth
// having for audit. A Task records what should happen: it is assigned,
// rescheduled, edited and eventually completed. Storing one as the other
// forces a choice between making Activity mutable, which destroys its value,
// and storing tasks that can never be completed.
//
// Two rules below are deliberate departures from their nearest neighbours in
// this package:
//
//   - Completion is reversible, unlike an Opportunity's terminal stages. A
//     task that turns out not to be finished is reopened, not replaced;
//     replacing it would lose what was originally asked and when.
//   - A subject is optional, unlike an Activity's. "Call the vendor back" is
//     a real task that attaches to nothing, and rejecting it would push
//     users into inventing a record to hang it on.
type Task struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority"`
	AssigneeID  string    `json:"assignee_id"`
	DueAt       time.Time `json:"due_at"`

	// SubjectType and SubjectID optionally attach this task to one CRM
	// record, using the same polymorphic edge Activity uses. Either both are
	// set or neither is.
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`

	// CompletedAt and CompletedBy are owned by CompleteTask/ReopenTask and
	// are never accepted from a caller — see sanitizeTaskLifecycle.
	CompletedAt time.Time `json:"completed_at"`
	CompletedBy string    `json:"completed_by"`

	CreatedBy    string       `json:"created_by"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	CustomFields CustomValues `json:"custom_fields"`
}

// Task.Status values.
const (
	TaskOpen       = "open"
	TaskInProgress = "in_progress"
	TaskCompleted  = "completed"
	TaskCancelled  = "cancelled"
)

var taskStatuses = map[string]bool{
	TaskOpen:       true,
	TaskInProgress: true,
	TaskCompleted:  true,
	TaskCancelled:  true,
}

// Task.Priority values. Empty is accepted and means normal.
const (
	TaskPriorityLow    = "low"
	TaskPriorityNormal = "normal"
	TaskPriorityHigh   = "high"
	TaskPriorityUrgent = "urgent"
)

var taskPriorities = map[string]bool{
	TaskPriorityLow:    true,
	TaskPriorityNormal: true,
	TaskPriorityHigh:   true,
	TaskPriorityUrgent: true,
}

const maxTaskTitleLen = 300
const maxTaskDescriptionLen = 10_000

// TaskIsOpen reports whether a status still represents outstanding work.
// Both non-terminal statuses count, which is why "still to do" is a concept
// the query layer exposes directly rather than making callers ask twice.
func TaskIsOpen(status string) bool {
	return status == TaskOpen || status == TaskInProgress
}

// EntityID returns the record's identifier.
func (e *Task) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Task) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Task) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Created returns the record's creation time.
func (e *Task) Created() time.Time { return e.CreatedAt }

// Validate rejects a Task that cannot be stored.
func (e *Task) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no task", ErrInvalid)
	}
	if strings.TrimSpace(e.Title) == "" {
		return fmt.Errorf("%w: Task.title is required", ErrInvalid)
	}
	if len(e.Title) > maxTaskTitleLen {
		return fmt.Errorf("%w: Task.title exceeds %d characters", ErrInvalid, maxTaskTitleLen)
	}
	if len(e.Description) > maxTaskDescriptionLen {
		return fmt.Errorf("%w: Task.description exceeds %d characters", ErrInvalid, maxTaskDescriptionLen)
	}
	if e.Status == "" {
		e.Status = TaskOpen
	}
	if !taskStatuses[e.Status] {
		return fmt.Errorf("%w: Task.status %q is not a known status", ErrInvalid, e.Status)
	}
	if e.Priority == "" {
		e.Priority = TaskPriorityNormal
	}
	if !taskPriorities[e.Priority] {
		return fmt.Errorf("%w: Task.priority %q is not a known priority", ErrInvalid, e.Priority)
	}
	// Half a subject is worse than none: it stores a dangling reference that
	// no query can resolve and no validation later can reconstruct.
	if (e.SubjectType == "") != (e.SubjectID == "") {
		return fmt.Errorf("%w: Task.subject_type and Task.subject_id must be set together", ErrInvalid)
	}
	return nil
}

func cloneTask(v *Task) *Task {
	if v == nil {
		return nil
	}
	c := *v
	c.CustomFields = v.CustomFields.Clone()
	return &c
}

// sanitizeTaskLifecycle keeps completion metadata under the store's control.
//
// CompletedAt and CompletedBy describe something the server observed, so a
// caller must not be able to assert them — a client that sets
// completed_at itself can backdate work, and one that sets it while leaving
// status open produces a record that contradicts itself. On create they are
// cleared; on update they are carried over from the stored record.
func sanitizeTaskLifecycle(v *Task, existing *Task) {
	if existing == nil {
		v.CompletedAt = time.Time{}
		v.CompletedBy = ""
		return
	}
	v.CompletedAt = existing.CompletedAt
	v.CompletedBy = existing.CompletedBy
}

// applyTaskCompletion moves a task to completed, idempotently.
//
// Idempotence matters more than it looks: completing is the action most
// likely to be double-submitted (a double-tap, a retried request), and a
// second call must not overwrite who finished the work or when.
func applyTaskCompletion(v *Task, actor string, now time.Time) {
	if v.Status == TaskCompleted && !v.CompletedAt.IsZero() {
		return
	}
	v.Status = TaskCompleted
	v.CompletedAt = now
	v.CompletedBy = actor
}

// applyTaskReopen returns a task to open and clears the completion record.
//
// Clearing is the point: a reopened task that kept completed_at would report
// as finished in every "completed this week" query while sitting in
// somebody's open list.
func applyTaskReopen(v *Task) {
	v.Status = TaskOpen
	v.CompletedAt = time.Time{}
	v.CompletedBy = ""
}

// TaskFilter narrows ListTasks. Every field is optional; an empty filter
// lists the tenant's tasks.
type TaskFilter struct {
	AssigneeID  string
	Status      string
	SubjectType string
	SubjectID   string
	// OpenOnly excludes completed and cancelled tasks. Distinct from Status
	// because "still to do" spans two statuses.
	OpenOnly bool
	// DueBefore, when non-zero, matches tasks due strictly earlier. With
	// OpenOnly this is the overdue list.
	DueBefore time.Time
}

// Validate rejects a filter that cannot be applied.
func (f TaskFilter) Validate() error {
	if f.Status != "" && !taskStatuses[f.Status] {
		return fmt.Errorf("%w: %q is not a known Task status", ErrInvalid, f.Status)
	}
	if (f.SubjectType == "") != (f.SubjectID == "") {
		return fmt.Errorf("%w: subject_type and subject_id must be set together", ErrInvalid)
	}
	if f.SubjectType != "" && !knownSubjectTypes[f.SubjectType] {
		return fmt.Errorf("%w: %q is not a known subject type", ErrInvalid, f.SubjectType)
	}
	return nil
}

// matches reports whether a task satisfies the filter. Shared by MemStore so
// its behaviour cannot drift from the SQL that PgStore builds.
func (f TaskFilter) matches(v *Task) bool {
	if f.AssigneeID != "" && v.AssigneeID != f.AssigneeID {
		return false
	}
	if f.Status != "" && v.Status != f.Status {
		return false
	}
	if f.SubjectType != "" && (v.SubjectType != f.SubjectType || v.SubjectID != f.SubjectID) {
		return false
	}
	if f.OpenOnly && !TaskIsOpen(v.Status) {
		return false
	}
	if !f.DueBefore.IsZero() {
		// A task with no due date is never overdue and never matches a
		// due-before window. Treating a zero time as "long ago" would put
		// every undated task at the top of the overdue list.
		if v.DueAt.IsZero() || !v.DueAt.Before(f.DueBefore) {
			return false
		}
	}
	return true
}

// TaskSort selects ListTasks' order. An allowlist, not a column name.
type TaskSort string

const (
	// SortTaskDue orders by due date ascending, undated tasks last — the
	// order a work list is actually read in.
	SortTaskDue TaskSort = ""
	// SortTaskCreatedDesc orders newest first.
	SortTaskCreatedDesc TaskSort = "-created_at"
)

// ParseTaskSort validates a raw sort string from a request.
func ParseTaskSort(raw string) (TaskSort, error) {
	switch TaskSort(raw) {
	case SortTaskDue, SortTaskCreatedDesc:
		return TaskSort(raw), nil
	default:
		return "", fmt.Errorf("%w: %q is not a supported Task sort", ErrInvalid, raw)
	}
}

// requireTaskSubject validates a task's optional subject. An unset subject is
// fine; a set one must resolve inside the caller's tenant.
func requireTaskSubject(ctx context.Context, r subjectReader, scope Scope, subjectType, subjectID string) error {
	if subjectType == "" && subjectID == "" {
		return nil
	}
	return requireSubject(ctx, r, scope, "Task", subjectType, subjectID)
}
