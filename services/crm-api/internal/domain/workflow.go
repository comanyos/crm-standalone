package domain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Workflow automation, v1 (docs/WORKFLOW_AUTOMATION.md,
// migrations/0023_workflow_automation.sql).
//
// The shape of v1 is deliberately narrow, and the narrowness is the feature.
// A rule is: one allowlisted trigger, an AND-only list of typed comparisons
// over allowlisted fields, and an ordered list of actions from a fixed set.
// There is no expression language, no arbitrary code, no outbound call and no
// time-based recurrence — each of those needs a sandbox, an egress policy, a
// secret story or a scheduler that this module does not have, and adding one
// without those is how automation becomes the way a CRM gets breached.
//
// Everything in this file is validated against an allowlist rather than
// parsed. A rule that names a field nobody allowed is refused at authoring
// time, not silently skipped at execution time — a rule that quietly never
// matches is worse than one that will not save, because the author believes
// it is working.

// The rule lifecycle states.
const (
	WorkflowDraft    = "draft"
	WorkflowActive   = "active"
	WorkflowPaused   = "paused"
	WorkflowArchived = "archived"
)

var workflowStatuses = map[string]bool{
	WorkflowDraft: true, WorkflowActive: true, WorkflowPaused: true, WorkflowArchived: true,
}

// The execution states.
const (
	ExecutionPending   = "pending"
	ExecutionRunning   = "running"
	ExecutionSucceeded = "succeeded"
	ExecutionFailed    = "failed"
	// ExecutionSkipped records that the rule matched the event type but its
	// conditions did not hold. It is stored rather than discarded because
	// "why didn't my rule fire" is the question authors actually ask, and an
	// engine that keeps no record of considering an event cannot answer it.
	ExecutionSkipped = "skipped"
)

// MaxExecutionAttempts bounds retries. A rule that has failed three times is
// failing for a reason that will not pass, and continuing costs the tenant
// work while burying the failure among retries.
const MaxExecutionAttempts = 3

// The trigger catalog: every event a v1 rule may fire on.
//
// This is an allowlist and not a convenience. It is also half of the loop
// prevention: workflow actions produce tasks, activities and notifications,
// and none of those appears here, so a rule cannot trigger on its own output.
// assertTriggersCannotLoop checks that at startup rather than trusting it,
// because the property is one added event type away from being false.
const (
	EventLeadCreated             = "lead.created"
	EventOpportunityStageChanged = EntityOpportunity + ".stage_changed"
)

// WorkflowTrigger names one allowlisted event and the object it concerns.
type WorkflowTrigger struct {
	EventType  string
	ObjectType string
}

// workflowTriggers is the catalog, keyed by event type.
var workflowTriggers = map[string]WorkflowTrigger{
	EventLeadCreated:             {EventLeadCreated, SubjectLead},
	EventOpportunityStageChanged: {EventOpportunityStageChanged, SubjectOpportunity},
	EventOpportunityClosedWon:    {EventOpportunityClosedWon, SubjectOpportunity},
}

// WorkflowTriggers returns the catalog, for the authoring UI to offer.
func WorkflowTriggers() []WorkflowTrigger {
	out := make([]WorkflowTrigger, 0, len(workflowTriggers))
	for _, trigger := range workflowTriggers {
		out = append(out, trigger)
	}
	sortTriggers(out)
	return out
}

func sortTriggers(triggers []WorkflowTrigger) {
	for i := 1; i < len(triggers); i++ {
		for j := i; j > 0 && triggers[j].EventType < triggers[j-1].EventType; j-- {
			triggers[j], triggers[j-1] = triggers[j-1], triggers[j]
		}
	}
}

// The event types workflow actions cause. Nothing here may be a trigger.
var workflowActionOutputs = map[string]bool{
	"task.created":         true,
	"activity.created":     true,
	"notification.created": true,
}

// AssertTriggersCannotLoop reports an error if any allowlisted trigger is
// also something a workflow action produces.
//
// It runs at startup, like the scope policy's totality check and for the same
// reason: a rule that can fire on its own output is an unbounded loop against
// a customer's records, and discovering that from a support ticket is too
// late. Loop prevention in v1 is structural — there is no depth counter —
// so the structure has to be checked.
func AssertTriggersCannotLoop() error {
	var overlap []string
	for eventType := range workflowTriggers {
		if workflowActionOutputs[eventType] {
			overlap = append(overlap, eventType)
		}
	}
	if len(overlap) == 0 {
		return nil
	}
	return fmt.Errorf(
		"workflow triggers %v are also produced by workflow actions; a rule could "+
			"fire on its own output. v1 prevents loops by construction and has no "+
			"depth limit, so either remove the trigger or add one", overlap)
}

// The comparison operators a condition may use.
const (
	OpEquals      = "eq"
	OpNotEquals   = "neq"
	OpGreaterThan = "gt"
	OpLessThan    = "lt"
	OpContains    = "contains"
	OpIn          = "in"
)

// fieldKind says how a field compares, which decides which operators apply.
type fieldKind int

const (
	kindString fieldKind = iota
	kindNumber
)

// workflowFields is the allowlist of comparable fields per object type.
//
// Fixed fields only: custom fields are tenant-defined and can be archived or
// retyped underneath a rule, which would turn a saved rule into one that
// silently stops matching. That needs a versioning story of its own.
var workflowFields = map[string]map[string]fieldKind{
	SubjectLead: {
		"source":   kindString,
		"status":   kindString,
		"owner_id": kindString,
		"company":  kindString,
		"score":    kindNumber,
	},
	SubjectOpportunity: {
		"stage":       kindString,
		"account_id":  kindString,
		"pipeline_id": kindString,
		"name":        kindString,
		"amount":      kindNumber,
	},
}

// WorkflowFieldsFor returns the comparable fields of one object type, sorted,
// so the authoring UI offers exactly what the engine accepts.
func WorkflowFieldsFor(objectType string) []string {
	fields := workflowFields[objectType]
	out := make([]string, 0, len(fields))
	for name := range fields {
		out = append(out, name)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// WorkflowCondition is one typed comparison. Conditions combine with AND
// only: OR needs grouping to be meaningful, and grouping needs an expression
// tree, which needs an editor and an evaluator this version does not have.
type WorkflowCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// The action types v1 supports.
const (
	ActionCreateTask  = "create_task"
	ActionAddActivity = "add_activity"
	ActionNotify      = "notify"
)

// Assignee selectors. A rule cannot name an arbitrary principal by typing an
// id into a value field and having it silently mean nothing; it picks one of
// these, and "the record's owner" is resolved at execution time.
const (
	// AssigneeRecordOwner is the owner of the record the event was about.
	AssigneeRecordOwner = "record_owner"
	// AssigneeFixedUser is the principal named in the action's AssigneeID.
	AssigneeFixedUser = "fixed_user"
)

// WorkflowAction is one thing a rule does. It is a flat struct rather than a
// per-type union because v1 has three actions with overlapping fields, and a
// union would cost more in conversion than it saves in tidiness. Validate
// enforces which fields each type actually uses.
type WorkflowAction struct {
	Type string `json:"type"`

	// Title is the task title or the notification title.
	Title string `json:"title,omitempty"`
	// Body is the task description, the activity note, or the notification
	// body. It is literal text: there is no template expansion in v1, because
	// a template that can read the record can read fields the rule author is
	// not allowed to see.
	Body string `json:"body,omitempty"`

	// Assignee selects who a task is for, or who is notified.
	Assignee   string `json:"assignee,omitempty"`
	AssigneeID string `json:"assignee_id,omitempty"`

	// Priority applies to create_task.
	Priority string `json:"priority,omitempty"`
	// DueInDays offsets the task's due date from execution. Zero means none.
	DueInDays int `json:"due_in_days,omitempty"`

	// ActivityKind applies to add_activity.
	ActivityKind string `json:"activity_kind,omitempty"`
}

const (
	maxWorkflowTextLength = 500
	maxWorkflowActions    = 10
	maxWorkflowConditions = 10
	maxWorkflowDueInDays  = 3650
)

// Validate checks one action against its type's rules.
func (a *WorkflowAction) Validate() error {
	switch a.Type {
	case ActionCreateTask:
		if strings.TrimSpace(a.Title) == "" {
			return fmt.Errorf("%w: a create_task action needs a title", ErrInvalid)
		}
		// taskPriorities is Task's own allowlist rather than a copy: an action
		// that authored a priority the store then refused would surface as a
		// failed execution instead of a rejected save, and a copy is how the
		// two come to disagree.
		if a.Priority != "" && !taskPriorities[a.Priority] {
			return fmt.Errorf("%w: %q is not a task priority", ErrInvalid, a.Priority)
		}
		if a.DueInDays < 0 || a.DueInDays > maxWorkflowDueInDays {
			return fmt.Errorf("%w: due_in_days must be between 0 and %d", ErrInvalid, maxWorkflowDueInDays)
		}
		return a.validateAssignee(true)
	case ActionAddActivity:
		if strings.TrimSpace(a.Body) == "" {
			return fmt.Errorf("%w: an add_activity action needs a note", ErrInvalid)
		}
		if strings.TrimSpace(a.ActivityKind) == "" {
			return fmt.Errorf("%w: an add_activity action needs a kind", ErrInvalid)
		}
		return a.bounded()
	case ActionNotify:
		if strings.TrimSpace(a.Title) == "" {
			return fmt.Errorf("%w: a notify action needs a title", ErrInvalid)
		}
		return a.validateAssignee(true)
	default:
		return fmt.Errorf("%w: %q is not a workflow action this version supports", ErrInvalid, a.Type)
	}
}

func (a *WorkflowAction) validateAssignee(required bool) error {
	switch a.Assignee {
	case AssigneeRecordOwner:
	case AssigneeFixedUser:
		if strings.TrimSpace(a.AssigneeID) == "" {
			return fmt.Errorf("%w: a fixed_user assignee needs an assignee_id", ErrInvalid)
		}
	case "":
		if required {
			return fmt.Errorf("%w: this action needs an assignee (%s or %s)",
				ErrInvalid, AssigneeRecordOwner, AssigneeFixedUser)
		}
	default:
		return fmt.Errorf("%w: %q is not an assignee selector", ErrInvalid, a.Assignee)
	}
	return a.bounded()
}

func (a *WorkflowAction) bounded() error {
	for name, value := range map[string]string{"title": a.Title, "body": a.Body} {
		if len([]rune(value)) > maxWorkflowTextLength {
			return fmt.Errorf("%w: action %s exceeds %d characters",
				ErrInvalid, name, maxWorkflowTextLength)
		}
	}
	return nil
}

// WorkflowRule is the durable rule: a name, a lifecycle state, and a pointer
// at the version that runs.
type WorkflowRule struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Status          string    `json:"status"`
	ActiveVersionID string    `json:"active_version_id"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Validate enforces what the database also enforces.
func (r *WorkflowRule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: a workflow rule needs a name", ErrInvalid)
	}
	if len([]rune(r.Name)) > maxWorkflowTextLength {
		return fmt.Errorf("%w: rule name exceeds %d characters", ErrInvalid, maxWorkflowTextLength)
	}
	if !workflowStatuses[r.Status] {
		return fmt.Errorf("%w: %q is not a workflow rule status", ErrInvalid, r.Status)
	}
	if r.Status == WorkflowActive && strings.TrimSpace(r.ActiveVersionID) == "" {
		return fmt.Errorf(
			"%w: an active rule needs a published version, or it would match events and do nothing",
			ErrInvalid)
	}
	return nil
}

// WorkflowRuleVersion is an immutable snapshot of what a rule said.
type WorkflowRuleVersion struct {
	ID                string              `json:"id"`
	RuleID            string              `json:"rule_id"`
	Version           int                 `json:"version"`
	TriggerEventType  string              `json:"trigger_event_type"`
	TriggerObjectType string              `json:"trigger_object_type"`
	Conditions        []WorkflowCondition `json:"conditions"`
	Actions           []WorkflowAction    `json:"actions"`
	CreatedBy         string              `json:"created_by"`
	CreatedAt         time.Time           `json:"created_at"`
}

// Validate checks a version against the trigger, field and action allowlists.
func (v *WorkflowRuleVersion) Validate() error {
	trigger, ok := workflowTriggers[v.TriggerEventType]
	if !ok {
		return fmt.Errorf("%w: %q is not a trigger this version supports", ErrInvalid, v.TriggerEventType)
	}
	// The object type is derived rather than trusted: a rule claiming to
	// trigger on lead.created for an opportunity would evaluate its
	// conditions against fields the event never carries.
	if v.TriggerObjectType != "" && v.TriggerObjectType != trigger.ObjectType {
		return fmt.Errorf("%w: %s concerns a %s, not a %s",
			ErrInvalid, v.TriggerEventType, trigger.ObjectType, v.TriggerObjectType)
	}
	v.TriggerObjectType = trigger.ObjectType

	if len(v.Conditions) > maxWorkflowConditions {
		return fmt.Errorf("%w: a rule may have at most %d conditions", ErrInvalid, maxWorkflowConditions)
	}
	for i := range v.Conditions {
		if err := validateCondition(trigger.ObjectType, v.Conditions[i]); err != nil {
			return err
		}
	}

	if len(v.Actions) == 0 {
		return fmt.Errorf("%w: a rule needs at least one action", ErrInvalid)
	}
	if len(v.Actions) > maxWorkflowActions {
		return fmt.Errorf("%w: a rule may have at most %d actions", ErrInvalid, maxWorkflowActions)
	}
	for i := range v.Actions {
		if err := v.Actions[i].Validate(); err != nil {
			return fmt.Errorf("action %d: %w", i+1, err)
		}
	}
	return nil
}

// validateCondition refuses a comparison the engine could not evaluate.
func validateCondition(objectType string, condition WorkflowCondition) error {
	kind, ok := workflowFields[objectType][condition.Field]
	if !ok {
		return fmt.Errorf("%w: %q is not a comparable field on a %s (allowed: %s)",
			ErrInvalid, condition.Field, objectType,
			strings.Join(WorkflowFieldsFor(objectType), ", "))
	}
	switch kind {
	case kindString:
		switch condition.Operator {
		case OpEquals, OpNotEquals, OpContains, OpIn:
		default:
			return fmt.Errorf("%w: %s is text and cannot be compared with %q",
				ErrInvalid, condition.Field, condition.Operator)
		}
	case kindNumber:
		switch condition.Operator {
		case OpEquals, OpNotEquals, OpGreaterThan, OpLessThan:
		default:
			return fmt.Errorf("%w: %s is a number and cannot be compared with %q",
				ErrInvalid, condition.Field, condition.Operator)
		}
		if _, err := strconv.ParseFloat(strings.TrimSpace(condition.Value), 64); err != nil {
			return fmt.Errorf("%w: %s is a number, and %q is not one",
				ErrInvalid, condition.Field, condition.Value)
		}
	}
	if len([]rune(condition.Value)) > maxWorkflowTextLength {
		return fmt.Errorf("%w: condition value exceeds %d characters",
			ErrInvalid, maxWorkflowTextLength)
	}
	return nil
}

// Matches reports whether every condition holds against a record snapshot.
//
// AND-only, and an unknown field is false rather than skipped: a condition
// the engine cannot evaluate must not be treated as satisfied, or a rule
// narrowed by a typo would fire on everything.
func (v *WorkflowRuleVersion) Matches(fields map[string]string) bool {
	for _, condition := range v.Conditions {
		if !conditionHolds(condition, fields) {
			return false
		}
	}
	return true
}

func conditionHolds(condition WorkflowCondition, fields map[string]string) bool {
	actual, present := fields[condition.Field]
	if !present {
		return false
	}
	switch condition.Operator {
	case OpEquals:
		return strings.EqualFold(actual, condition.Value)
	case OpNotEquals:
		return !strings.EqualFold(actual, condition.Value)
	case OpContains:
		return strings.Contains(strings.ToLower(actual), strings.ToLower(condition.Value))
	case OpIn:
		for _, candidate := range strings.Split(condition.Value, ",") {
			if strings.EqualFold(strings.TrimSpace(candidate), actual) {
				return true
			}
		}
		return false
	case OpGreaterThan, OpLessThan:
		left, leftErr := strconv.ParseFloat(strings.TrimSpace(actual), 64)
		right, rightErr := strconv.ParseFloat(strings.TrimSpace(condition.Value), 64)
		if leftErr != nil || rightErr != nil {
			return false
		}
		if condition.Operator == OpGreaterThan {
			return left > right
		}
		return left < right
	default:
		return false
	}
}

// WorkflowExecution is one attempt to run one rule version against one event.
type WorkflowExecution struct {
	ID              string    `json:"id"`
	RuleID          string    `json:"rule_id"`
	RuleVersionID   string    `json:"rule_version_id"`
	SourceEventID   string    `json:"source_event_id"`
	SourceEventType string    `json:"source_event_type"`
	SubjectType     string    `json:"subject_type"`
	SubjectID       string    `json:"subject_id"`
	Status          string    `json:"status"`
	Attempts        int       `json:"attempts"`
	NextAttemptAt   time.Time `json:"next_attempt_at"`
	// ErrorCategory is a bounded code, never a message: rule authors read
	// these rows, and a raw error would carry whatever the failing action
	// was holding.
	ErrorCategory    string    `json:"error_category"`
	ActionsCompleted int       `json:"actions_completed"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

var executionStatuses = map[string]bool{
	ExecutionPending: true, ExecutionRunning: true, ExecutionSucceeded: true,
	ExecutionFailed: true, ExecutionSkipped: true,
}

// Validate enforces what the database also enforces.
func (e *WorkflowExecution) Validate() error {
	switch {
	case strings.TrimSpace(e.RuleVersionID) == "":
		return fmt.Errorf("%w: an execution names no rule version", ErrInvalid)
	case strings.TrimSpace(e.SourceEventID) == "":
		return fmt.Errorf("%w: an execution names no source event", ErrInvalid)
	case !executionStatuses[e.Status]:
		return fmt.Errorf("%w: %q is not an execution status", ErrInvalid, e.Status)
	case e.Attempts < 0 || e.ActionsCompleted < 0:
		return fmt.Errorf("%w: execution counters cannot be negative", ErrInvalid)
	case len(e.ErrorCategory) > 64:
		return fmt.Errorf("%w: execution error category exceeds 64 characters", ErrInvalid)
	}
	return nil
}

// Exhausted reports whether this execution has used its retry budget.
func (e *WorkflowExecution) Exhausted() bool { return e.Attempts >= MaxExecutionAttempts }

// Notification is an in-app message to one principal.
type Notification struct {
	ID          string    `json:"id"`
	RecipientID string    `json:"recipient_id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
	RuleID      string    `json:"rule_id"`
	ReadAt      time.Time `json:"read_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// Validate enforces what the database also enforces.
func (n *Notification) Validate() error {
	switch {
	case strings.TrimSpace(n.RecipientID) == "":
		return fmt.Errorf("%w: a notification needs a recipient", ErrInvalid)
	case strings.TrimSpace(n.Title) == "":
		return fmt.Errorf("%w: a notification needs a title", ErrInvalid)
	case (n.SubjectType == "") != (n.SubjectID == ""):
		return fmt.Errorf("%w: half a notification subject is a reference nothing resolves", ErrInvalid)
	case n.SubjectType != "" && !knownSubjectTypes[n.SubjectType]:
		return fmt.Errorf("%w: %q is not a known subject type", ErrInvalid, n.SubjectType)
	case len([]rune(n.Title)) > maxWorkflowTextLength ||
		len([]rune(n.Body)) > maxWorkflowTextLength:
		return fmt.Errorf("%w: notification text exceeds %d characters",
			ErrInvalid, maxWorkflowTextLength)
	}
	return nil
}

// WorkflowCursor is where the engine has read up to in one tenant's outbox.
type WorkflowCursor struct {
	LastEventCreatedAt time.Time `json:"last_event_created_at"`
	LastEventID        string    `json:"last_event_id"`
	RunningUntil       time.Time `json:"running_until"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// MarshalConditions and MarshalActions render a version's lists for storage.
func (v *WorkflowRuleVersion) MarshalConditions() ([]byte, error) {
	if v.Conditions == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v.Conditions)
}

// MarshalActions renders the ordered action list for storage.
func (v *WorkflowRuleVersion) MarshalActions() ([]byte, error) {
	return json.Marshal(v.Actions)
}

func cloneWorkflowRule(r *WorkflowRule) *WorkflowRule {
	if r == nil {
		return nil
	}
	out := *r
	return &out
}

func cloneWorkflowRuleVersion(v *WorkflowRuleVersion) *WorkflowRuleVersion {
	if v == nil {
		return nil
	}
	out := *v
	out.Conditions = append([]WorkflowCondition(nil), v.Conditions...)
	out.Actions = append([]WorkflowAction(nil), v.Actions...)
	return &out
}

func cloneWorkflowExecution(e *WorkflowExecution) *WorkflowExecution {
	if e == nil {
		return nil
	}
	out := *e
	return &out
}

func cloneNotification(n *Notification) *Notification {
	if n == nil {
		return nil
	}
	out := *n
	return &out
}
