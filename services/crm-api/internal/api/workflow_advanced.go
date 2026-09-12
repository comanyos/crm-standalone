package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	workflowintegration "github.com/santoshguru/crm/internal/integration/workflow"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *CRMService) CreateWorkflowSchedule(
	ctx context.Context, req *connect.Request[crmv1.CreateWorkflowScheduleRequest],
) (*connect.Response[crmv1.CreateWorkflowScheduleResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	next, ends, err := scheduleTimes(req.Msg.GetNextRunAt(), req.Msg.GetEndsAt())
	if err != nil {
		return nil, connectErr(err)
	}
	if next.Before(time.Now().UTC().Add(-time.Minute)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the first scheduled run cannot be in the past"))
	}
	row, err := s.store.CreateWorkflowSchedule(ctx, scope, &domain.WorkflowSchedule{
		RuleID: req.Msg.GetRuleId(), SubjectType: req.Msg.GetSubjectType(), SubjectID: req.Msg.GetSubjectId(),
		TimeZone: req.Msg.GetTimeZone(), Recurrence: req.Msg.GetRecurrence(), Interval: int(req.Msg.GetInterval()),
		Status: domain.ScheduleActive, NextRunAt: next, EndsAt: ends, CreatedBy: principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateWorkflowScheduleResponse{Schedule: workflowScheduleToProto(row)}), nil
}

func (s *CRMService) ListWorkflowSchedules(
	ctx context.Context, _ *connect.Request[crmv1.ListWorkflowSchedulesRequest],
) (*connect.Response[crmv1.ListWorkflowSchedulesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListWorkflowSchedules(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.WorkflowSchedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, workflowScheduleToProto(row))
	}
	return connect.NewResponse(&crmv1.ListWorkflowSchedulesResponse{Schedules: out}), nil
}

func (s *CRMService) UpdateWorkflowSchedule(
	ctx context.Context, req *connect.Request[crmv1.UpdateWorkflowScheduleRequest],
) (*connect.Response[crmv1.UpdateWorkflowScheduleResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.store.GetWorkflowSchedule(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	next, ends, err := scheduleTimes(req.Msg.GetNextRunAt(), req.Msg.GetEndsAt())
	if err != nil {
		return nil, connectErr(err)
	}
	row.TimeZone, row.Recurrence, row.Interval = req.Msg.GetTimeZone(), req.Msg.GetRecurrence(), int(req.Msg.GetInterval())
	row.Status, row.NextRunAt, row.EndsAt = req.Msg.GetStatus(), next, ends
	updated, err := s.store.UpdateWorkflowSchedule(ctx, scope, row)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateWorkflowScheduleResponse{Schedule: workflowScheduleToProto(updated)}), nil
}

func (s *CRMService) DeleteWorkflowSchedule(
	ctx context.Context, req *connect.Request[crmv1.DeleteWorkflowScheduleRequest],
) (*connect.Response[crmv1.DeleteWorkflowScheduleResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteWorkflowSchedule(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteWorkflowScheduleResponse{}), nil
}

func (s *CRMService) ListWorkflowApprovals(
	ctx context.Context, req *connect.Request[crmv1.ListWorkflowApprovalsRequest],
) (*connect.Response[crmv1.ListWorkflowApprovalsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	status := req.Msg.GetStatus()
	if status != "" && status != domain.ApprovalPending && status != domain.ApprovalApproved && status != domain.ApprovalRejected {
		return nil, connectErr(domain.ErrInvalid)
	}
	rows, err := s.store.ListWorkflowApprovals(ctx, scope, principalSubject(ctx), status, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.WorkflowApproval, 0, len(rows))
	for _, row := range rows {
		out = append(out, workflowApprovalToProto(row))
	}
	return connect.NewResponse(&crmv1.ListWorkflowApprovalsResponse{Approvals: out}), nil
}

func (s *CRMService) DecideWorkflowApproval(
	ctx context.Context, req *connect.Request[crmv1.DecideWorkflowApprovalRequest],
) (*connect.Response[crmv1.DecideWorkflowApprovalResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.store.DecideWorkflowApproval(ctx, scope, req.Msg.GetId(), principalSubject(ctx),
		req.Msg.GetDecision(), req.Msg.GetNote(), time.Now().UTC())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DecideWorkflowApprovalResponse{Approval: workflowApprovalToProto(row)}), nil
}

func (s *CRMService) CreateWorkflowWebhookEndpoint(
	ctx context.Context, req *connect.Request[crmv1.CreateWorkflowWebhookEndpointRequest],
) (*connect.Response[crmv1.CreateWorkflowWebhookEndpointResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.workflowWebhooks == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("workflow webhook secrets are not configured for this deployment"))
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not generate webhook signing secret"))
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	clear(secretBytes)
	endpointID := domain.NewID()
	ciphertext, err := s.workflowWebhooks.Seal([]byte(secret), workflowintegration.WebhookSecretBinding(scope.TenantID, endpointID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not protect webhook signing secret"))
	}
	row, err := s.store.CreateWorkflowWebhookEndpoint(ctx, scope, &domain.WorkflowWebhookEndpoint{
		ID: endpointID, Name: req.Msg.GetName(), URL: req.Msg.GetUrl(), SigningSecretCiphertext: ciphertext,
		CreatedBy: principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateWorkflowWebhookEndpointResponse{
		Endpoint: workflowWebhookEndpointToProto(row), SigningSecret: secret,
	}), nil
}

func (s *CRMService) ListWorkflowWebhookEndpoints(
	ctx context.Context, _ *connect.Request[crmv1.ListWorkflowWebhookEndpointsRequest],
) (*connect.Response[crmv1.ListWorkflowWebhookEndpointsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListWorkflowWebhookEndpoints(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.WorkflowWebhookEndpoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, workflowWebhookEndpointToProto(row))
	}
	return connect.NewResponse(&crmv1.ListWorkflowWebhookEndpointsResponse{Endpoints: out}), nil
}

func (s *CRMService) DeleteWorkflowWebhookEndpoint(
	ctx context.Context, req *connect.Request[crmv1.DeleteWorkflowWebhookEndpointRequest],
) (*connect.Response[crmv1.DeleteWorkflowWebhookEndpointResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteWorkflowWebhookEndpoint(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteWorkflowWebhookEndpointResponse{}), nil
}

func scheduleTimes(nextMessage, endsMessage *timestamppb.Timestamp) (time.Time, time.Time, error) {
	var next, ends time.Time
	if nextMessage != nil {
		if err := nextMessage.CheckValid(); err != nil {
			return time.Time{}, time.Time{}, domain.ErrInvalid
		}
		next = nextMessage.AsTime()
	}
	if endsMessage != nil {
		if err := endsMessage.CheckValid(); err != nil {
			return time.Time{}, time.Time{}, domain.ErrInvalid
		}
		ends = endsMessage.AsTime()
	}
	return next, ends, nil
}

func workflowScheduleToProto(v *domain.WorkflowSchedule) *crmv1.WorkflowSchedule {
	if v == nil {
		return nil
	}
	return &crmv1.WorkflowSchedule{Id: v.ID, RuleId: v.RuleID, SubjectType: v.SubjectType, SubjectId: v.SubjectID,
		TimeZone: v.TimeZone, Recurrence: v.Recurrence, Interval: boundedInt32(v.Interval), Status: v.Status,
		NextRunAt: optionalTimestamp(v.NextRunAt), EndsAt: optionalTimestamp(v.EndsAt), LastRunAt: optionalTimestamp(v.LastRunAt),
		CreatedBy: v.CreatedBy, CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt)}
}

func workflowApprovalToProto(v *domain.WorkflowApproval) *crmv1.WorkflowApproval {
	if v == nil {
		return nil
	}
	return &crmv1.WorkflowApproval{Id: v.ID, ExecutionId: v.ExecutionID, RuleId: v.RuleID,
		ActionIndex: boundedInt32(v.ActionIndex), Title: v.Title, Body: v.Body, SubjectType: v.SubjectType,
		SubjectId: v.SubjectID, Status: v.Status, DecisionNote: v.DecisionNote,
		RequestedAt: optionalTimestamp(v.RequestedAt), DecidedAt: optionalTimestamp(v.DecidedAt)}
}

func workflowWebhookEndpointToProto(v *domain.WorkflowWebhookEndpoint) *crmv1.WorkflowWebhookEndpoint {
	if v == nil {
		return nil
	}
	return &crmv1.WorkflowWebhookEndpoint{Id: v.ID, Name: v.Name, Url: v.URL, CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt)}
}
