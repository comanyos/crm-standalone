package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Workflow automation's RPC surface (docs/WORKFLOW_AUTOMATION.md).
//
// Two things this layer is responsible for, beyond translating messages:
//
// It decides what a request may change. The store's UpdateWorkflowRule
// replaces a row, so a request that arrived carrying a whole rule could edit
// its status, its published version and its author in one call labelled
// "rename". Each RPC here reads the rule first and applies only the fields it
// is named for, which is why UpdateWorkflowRule takes three strings rather
// than a WorkflowRule message.
//
// And no RPC here accepts a version id. A rule runs its own versions, and
// the reference between the two tables is circular, so there is no foreign
// key to enforce that; the surface enforces it instead, by only ever
// activating a version this same call just wrote for this same rule.

// defaultExecutionPage bounds the ledger when a caller names no limit.
const defaultExecutionPage = 50

// maxNotificationPage bounds the notification list however large a limit the
// caller asks for.
const maxNotificationPage = 200

// DescribeWorkflowCatalog reports the allowlists the editor must offer.
func (s *CRMService) DescribeWorkflowCatalog(
	ctx context.Context, _ *connect.Request[crmv1.DescribeWorkflowCatalogRequest],
) (*connect.Response[crmv1.DescribeWorkflowCatalogResponse], error) {
	if _, err := scopeFrom(ctx); err != nil {
		return nil, err
	}
	triggers := domain.WorkflowTriggers()
	out := make([]*crmv1.WorkflowTriggerDescriptor, 0, len(triggers))
	for _, trigger := range triggers {
		specs := domain.WorkflowFieldSpecsFor(trigger.ObjectType)
		fields := make([]*crmv1.WorkflowField, 0, len(specs))
		for _, spec := range specs {
			fields = append(fields, &crmv1.WorkflowField{
				Name: spec.Name, Kind: spec.Kind, Operators: spec.Operators,
			})
		}
		out = append(out, &crmv1.WorkflowTriggerDescriptor{
			EventType: trigger.EventType, ObjectType: trigger.ObjectType, Fields: fields,
		})
	}
	maxConditions, maxActions, maxText := domain.WorkflowLimits()
	return connect.NewResponse(&crmv1.DescribeWorkflowCatalogResponse{
		Triggers:           out,
		ActionTypes:        domain.WorkflowActionTypes(),
		AssigneeSelectors:  domain.WorkflowAssigneeSelectors(),
		MaxConditions:      boundedInt32(maxConditions),
		MaxActions:         boundedInt32(maxActions),
		MaxTextLength:      boundedInt32(maxText),
		MaxRotationMembers: boundedInt32(domain.MaxRotationMembers()),
		AssignableSubjects: domain.SortedAssignableSubjects(),
	}), nil
}

// CreateWorkflowRule starts a rule as a draft.
//
// The status is not a parameter. A rule can only be active once it has a
// published version to run, and the sequence that gets there — create,
// publish, activate — is the same sequence whatever the caller intended.
func (s *CRMService) CreateWorkflowRule(
	ctx context.Context, req *connect.Request[crmv1.CreateWorkflowRuleRequest],
) (*connect.Response[crmv1.CreateWorkflowRuleResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rule, err := s.store.CreateWorkflowRule(ctx, scope, &domain.WorkflowRule{
		Name:        req.Msg.GetName(),
		Description: req.Msg.GetDescription(),
		Status:      domain.WorkflowDraft,
		CreatedBy:   principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateWorkflowRuleResponse{
		Rule: workflowRuleToProto(rule),
	}), nil
}

// GetWorkflowRule reads a rule and the version it runs.
func (s *CRMService) GetWorkflowRule(
	ctx context.Context, req *connect.Request[crmv1.GetWorkflowRuleRequest],
) (*connect.Response[crmv1.GetWorkflowRuleResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rule, err := s.store.GetWorkflowRule(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	out := &crmv1.GetWorkflowRuleResponse{Rule: workflowRuleToProto(rule)}
	if rule.ActiveVersionID != "" {
		version, err := s.store.GetWorkflowRuleVersion(ctx, scope, rule.ActiveVersionID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, connectErr(err)
		}
		if version != nil {
			out.ActiveVersion = workflowVersionToProto(version)
		}
	}
	return connect.NewResponse(out), nil
}

// ListWorkflowRules returns the tenant's rules.
func (s *CRMService) ListWorkflowRules(
	ctx context.Context, req *connect.Request[crmv1.ListWorkflowRulesRequest],
) (*connect.Response[crmv1.ListWorkflowRulesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := s.store.ListWorkflowRules(ctx, scope, req.Msg.GetIncludeArchived())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.WorkflowRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, workflowRuleToProto(rule))
	}
	return connect.NewResponse(&crmv1.ListWorkflowRulesResponse{Rules: out}), nil
}

// UpdateWorkflowRule renames a rule. It cannot change what the rule does.
func (s *CRMService) UpdateWorkflowRule(
	ctx context.Context, req *connect.Request[crmv1.UpdateWorkflowRuleRequest],
) (*connect.Response[crmv1.UpdateWorkflowRuleResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rule, err := s.store.GetWorkflowRule(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	rule.Name, rule.Description = req.Msg.GetName(), req.Msg.GetDescription()
	updated, err := s.store.UpdateWorkflowRule(ctx, scope, rule)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateWorkflowRuleResponse{
		Rule: workflowRuleToProto(updated),
	}), nil
}

// PublishWorkflowRuleVersion writes a new immutable version of a rule.
//
// It never edits the running one. An author correcting a live rule publishes
// a version and then activates it, and until they do, the rule keeps doing
// what its ledger says it has been doing.
func (s *CRMService) PublishWorkflowRuleVersion(
	ctx context.Context, req *connect.Request[crmv1.PublishWorkflowRuleVersionRequest],
) (*connect.Response[crmv1.PublishWorkflowRuleVersionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rule, err := s.store.GetWorkflowRule(ctx, scope, req.Msg.GetRuleId())
	if err != nil {
		return nil, connectErr(err)
	}
	if rule.Status == domain.WorkflowArchived {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this rule is archived; publishing to it would produce a version nothing can run"))
	}

	version, err := s.store.CreateWorkflowRuleVersion(ctx, scope, &domain.WorkflowRuleVersion{
		RuleID:           rule.ID,
		TriggerEventType: req.Msg.GetTriggerEventType(),
		Conditions:       workflowConditionsFromProto(req.Msg.GetConditions()),
		Actions:          workflowActionsFromProto(req.Msg.GetActions()),
		CreatedBy:        principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}

	if req.Msg.GetActivate() {
		rule.ActiveVersionID = version.ID
		if err := rule.TransitionTo(domain.WorkflowActive); err != nil {
			return nil, connectErr(err)
		}
		rule, err = s.store.UpdateWorkflowRule(ctx, scope, rule)
		if err != nil {
			return nil, connectErr(err)
		}
	}
	return connect.NewResponse(&crmv1.PublishWorkflowRuleVersionResponse{
		Version: workflowVersionToProto(version),
		Rule:    workflowRuleToProto(rule),
	}), nil
}

// ListWorkflowRuleVersions returns a rule's history, newest first.
func (s *CRMService) ListWorkflowRuleVersions(
	ctx context.Context, req *connect.Request[crmv1.ListWorkflowRuleVersionsRequest],
) (*connect.Response[crmv1.ListWorkflowRuleVersionsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	versions, err := s.store.ListWorkflowRuleVersions(ctx, scope, req.Msg.GetRuleId())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.WorkflowRuleVersion, 0, len(versions))
	for _, version := range versions {
		out = append(out, workflowVersionToProto(version))
	}
	return connect.NewResponse(&crmv1.ListWorkflowRuleVersionsResponse{Versions: out}), nil
}

// SetWorkflowRuleStatus moves a rule through its lifecycle.
func (s *CRMService) SetWorkflowRuleStatus(
	ctx context.Context, req *connect.Request[crmv1.SetWorkflowRuleStatusRequest],
) (*connect.Response[crmv1.SetWorkflowRuleStatusResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rule, err := s.store.GetWorkflowRule(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	if err := rule.TransitionTo(req.Msg.GetStatus()); err != nil {
		return nil, connectErr(err)
	}
	updated, err := s.store.UpdateWorkflowRule(ctx, scope, rule)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.SetWorkflowRuleStatusResponse{
		Rule: workflowRuleToProto(updated),
	}), nil
}

// ListWorkflowExecutions returns one rule's ledger.
func (s *CRMService) ListWorkflowExecutions(
	ctx context.Context, req *connect.Request[crmv1.ListWorkflowExecutionsRequest],
) (*connect.Response[crmv1.ListWorkflowExecutionsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	// The rule is read first so a request naming another tenant's id — or one
	// that does not exist — is a not-found rather than an empty ledger, which
	// reads as "this rule has never run".
	if _, err := s.store.GetWorkflowRule(ctx, scope, req.Msg.GetRuleId()); err != nil {
		return nil, connectErr(err)
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = defaultExecutionPage
	}
	executions, err := s.store.ListWorkflowExecutions(ctx, scope, req.Msg.GetRuleId(), limit)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.WorkflowExecution, 0, len(executions))
	for _, execution := range executions {
		out = append(out, workflowExecutionToProto(execution))
	}
	return connect.NewResponse(&crmv1.ListWorkflowExecutionsResponse{Executions: out}), nil
}

// ListNotifications returns the calling principal's own notifications.
//
// The recipient is the caller, never a parameter. A notification carries the
// title and body of whatever a rule decided to say about a record, so reading
// somebody else's is reading records they can see and you may not.
func (s *CRMService) ListNotifications(
	ctx context.Context, req *connect.Request[crmv1.ListNotificationsRequest],
) (*connect.Response[crmv1.ListNotificationsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	recipient := principalSubject(ctx)
	if recipient == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated,
			errors.New("this principal has no subject, so it has no notifications"))
	}
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > maxNotificationPage {
		limit = maxNotificationPage
	}
	rows, err := s.store.ListNotifications(ctx, scope, recipient, req.Msg.GetUnreadOnly(), limit)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.Notification, 0, len(rows))
	for _, row := range rows {
		out = append(out, notificationToProto(row))
	}

	// The badge counts unread notifications, not unread notifications on this
	// page: a user with three hundred would otherwise see the page size.
	unread := rows
	if !req.Msg.GetUnreadOnly() {
		unread, err = s.store.ListNotifications(ctx, scope, recipient, true, maxNotificationPage)
		if err != nil {
			return nil, connectErr(err)
		}
	}
	return connect.NewResponse(&crmv1.ListNotificationsResponse{
		Notifications: out, UnreadCount: boundedInt32(len(unread)),
	}), nil
}

// MarkNotificationRead marks one of the caller's own notifications read.
func (s *CRMService) MarkNotificationRead(
	ctx context.Context, req *connect.Request[crmv1.MarkNotificationReadRequest],
) (*connect.Response[crmv1.MarkNotificationReadResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	recipient := principalSubject(ctx)
	if recipient == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated,
			errors.New("this principal has no subject, so it has no notifications"))
	}
	// The store matches on recipient as well as id, so marking somebody
	// else's notification read is a not-found rather than a silent success.
	row, err := s.store.MarkNotificationRead(ctx, scope, recipient, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.MarkNotificationReadResponse{
		Notification: notificationToProto(row),
	}), nil
}

func workflowRuleToProto(v *domain.WorkflowRule) *crmv1.WorkflowRule {
	if v == nil {
		return nil
	}
	return &crmv1.WorkflowRule{
		Id: v.ID, Name: v.Name, Description: v.Description, Status: v.Status,
		ActiveVersionId: v.ActiveVersionID, CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt),
	}
}

func workflowVersionToProto(v *domain.WorkflowRuleVersion) *crmv1.WorkflowRuleVersion {
	if v == nil {
		return nil
	}
	conditions := make([]*crmv1.WorkflowCondition, 0, len(v.Conditions))
	for _, condition := range v.Conditions {
		conditions = append(conditions, &crmv1.WorkflowCondition{
			Field: condition.Field, Operator: condition.Operator, Value: condition.Value,
		})
	}
	actions := make([]*crmv1.WorkflowAction, 0, len(v.Actions))
	for _, action := range v.Actions {
		actions = append(actions, &crmv1.WorkflowAction{
			Type: action.Type, Title: action.Title, Body: action.Body,
			Assignee: action.Assignee, AssigneeId: action.AssigneeID,
			Priority: action.Priority, DueInDays: boundedInt32(action.DueInDays),
			ActivityKind: action.ActivityKind, RotationMembers: action.RotationMembers,
			ChannelKind: action.ChannelKind, TemplateId: action.TemplateID,
		})
	}
	return &crmv1.WorkflowRuleVersion{
		Id: v.ID, RuleId: v.RuleID, Version: boundedInt32(v.Version),
		TriggerEventType: v.TriggerEventType, TriggerObjectType: v.TriggerObjectType,
		Conditions: conditions, Actions: actions, CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt),
	}
}

func workflowConditionsFromProto(in []*crmv1.WorkflowCondition) []domain.WorkflowCondition {
	out := make([]domain.WorkflowCondition, 0, len(in))
	for _, condition := range in {
		out = append(out, domain.WorkflowCondition{
			Field: condition.GetField(), Operator: condition.GetOperator(), Value: condition.GetValue(),
		})
	}
	return out
}

func workflowActionsFromProto(in []*crmv1.WorkflowAction) []domain.WorkflowAction {
	out := make([]domain.WorkflowAction, 0, len(in))
	for _, action := range in {
		out = append(out, domain.WorkflowAction{
			Type: action.GetType(), Title: action.GetTitle(), Body: action.GetBody(),
			Assignee: action.GetAssignee(), AssigneeID: action.GetAssigneeId(),
			Priority: action.GetPriority(), DueInDays: int(action.GetDueInDays()),
			ActivityKind: action.GetActivityKind(), RotationMembers: action.GetRotationMembers(),
			ChannelKind: action.GetChannelKind(), TemplateID: action.GetTemplateId(),
		})
	}
	return out
}

func workflowExecutionToProto(v *domain.WorkflowExecution) *crmv1.WorkflowExecution {
	if v == nil {
		return nil
	}
	return &crmv1.WorkflowExecution{
		Id: v.ID, RuleId: v.RuleID, RuleVersionId: v.RuleVersionID,
		SourceEventId: v.SourceEventID, SourceEventType: v.SourceEventType,
		SubjectType: v.SubjectType, SubjectId: v.SubjectID, Status: v.Status,
		Attempts: boundedInt32(v.Attempts), ErrorCategory: v.ErrorCategory,
		ActionsCompleted: boundedInt32(v.ActionsCompleted),
		NextAttemptAt:    optionalTimestamp(v.NextAttemptAt),
		StartedAt:        optionalTimestamp(v.StartedAt),
		FinishedAt:       optionalTimestamp(v.FinishedAt),
		CreatedAt:        optionalTimestamp(v.CreatedAt),
	}
}

func notificationToProto(v *domain.Notification) *crmv1.Notification {
	if v == nil {
		return nil
	}
	return &crmv1.Notification{
		Id: v.ID, Kind: v.Kind, Title: v.Title, Body: v.Body,
		SubjectType: v.SubjectType, SubjectId: v.SubjectID, RuleId: v.RuleID,
		ReadAt: optionalTimestamp(v.ReadAt), CreatedAt: optionalTimestamp(v.CreatedAt),
	}
}
