package domain

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (s *MemStore) CreateWorkflowSchedule(ctx context.Context, scope Scope, v *WorkflowSchedule) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	r := cloneWorkflowSchedule(v)
	if err := r.Validate(); err != nil {
		return nil, err
	}
	rule, err := s.GetWorkflowRule(ctx, scope, r.RuleID)
	if err != nil {
		return nil, err
	}
	if rule.Status != WorkflowActive || rule.ActiveVersionID == "" {
		return nil, ErrInvalidTransition
	}
	version, err := s.GetWorkflowRuleVersion(ctx, scope, rule.ActiveVersionID)
	if err != nil {
		return nil, err
	}
	if version.TriggerObjectType != r.SubjectType {
		return nil, ErrInvalid
	}
	if err := requireActivitySubject(ctx, s, scope, r.SubjectType, r.SubjectID); err != nil {
		return nil, err
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	now := nowStamp()
	r.CreatedAt, r.UpdatedAt = now, now
	if s.workflow.schedules[scope.TenantID] == nil {
		s.workflow.schedules[scope.TenantID] = map[string]*WorkflowSchedule{}
	}
	s.workflow.schedules[scope.TenantID][r.ID] = cloneWorkflowSchedule(r)
	return cloneWorkflowSchedule(r), nil
}

func (s *MemStore) GetWorkflowSchedule(_ context.Context, scope Scope, id string) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	r := s.workflow.schedules[scope.TenantID][id]
	if r == nil {
		return nil, ErrNotFound
	}
	return cloneWorkflowSchedule(r), nil
}
func (s *MemStore) ListWorkflowSchedules(_ context.Context, scope Scope) ([]*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	out := make([]*WorkflowSchedule, 0)
	for _, r := range s.workflow.schedules[scope.TenantID] {
		out = append(out, cloneWorkflowSchedule(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRunAt.Before(out[j].NextRunAt) })
	return out, nil
}
func (s *MemStore) UpdateWorkflowSchedule(_ context.Context, scope Scope, v *WorkflowSchedule) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	r := cloneWorkflowSchedule(v)
	if err := r.Validate(); err != nil {
		return nil, err
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	existing := s.workflow.schedules[scope.TenantID][r.ID]
	if existing == nil {
		return nil, ErrNotFound
	}
	r.RuleID, r.SubjectType, r.SubjectID, r.CreatedBy, r.CreatedAt = existing.RuleID, existing.SubjectType, existing.SubjectID, existing.CreatedBy, existing.CreatedAt
	r.UpdatedAt = nowStamp()
	s.workflow.schedules[scope.TenantID][r.ID] = cloneWorkflowSchedule(r)
	return cloneWorkflowSchedule(r), nil
}
func (s *MemStore) DeleteWorkflowSchedule(_ context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	if s.workflow.schedules[scope.TenantID][id] == nil {
		return ErrNotFound
	}
	delete(s.workflow.schedules[scope.TenantID], id)
	return nil
}
func (s *MemStore) DueWorkflowSchedules(_ context.Context, scope Scope, now time.Time, limit int) ([]*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	out := make([]*WorkflowSchedule, 0)
	for _, r := range s.workflow.schedules[scope.TenantID] {
		if r.Status == ScheduleActive && !r.NextRunAt.After(now) {
			out = append(out, cloneWorkflowSchedule(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRunAt.Before(out[j].NextRunAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemStore) AdvanceWorkflowSchedule(_ context.Context, scope Scope, id string, expected, next time.Time, status string) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	r := s.workflow.schedules[scope.TenantID][id]
	if r == nil || r.Status != ScheduleActive || !r.NextRunAt.Equal(expected) {
		return nil, ErrConflict
	}
	r.LastRunAt = nowStamp()
	if !next.IsZero() {
		r.NextRunAt = next
	}
	r.Status = status
	r.UpdatedAt = nowStamp()
	return cloneWorkflowSchedule(r), nil
}

func (s *MemStore) EnsureWorkflowApproval(_ context.Context, scope Scope, v *WorkflowApproval) (*WorkflowApproval, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	r := cloneWorkflowApproval(v)
	if err := r.Validate(); err != nil {
		return nil, err
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	if s.workflow.approvals[scope.TenantID] == nil {
		s.workflow.approvals[scope.TenantID] = map[string]*WorkflowApproval{}
	}
	for _, existing := range s.workflow.approvals[scope.TenantID] {
		if existing.ExecutionID == r.ExecutionID && existing.ActionIndex == r.ActionIndex {
			return cloneWorkflowApproval(existing), nil
		}
	}
	if r.ID == "" {
		r.ID = newID()
	}
	if r.RequestedAt.IsZero() {
		r.RequestedAt = nowStamp()
	}
	s.workflow.approvals[scope.TenantID][r.ID] = cloneWorkflowApproval(r)
	return cloneWorkflowApproval(r), nil
}
func (s *MemStore) ListWorkflowApprovals(_ context.Context, scope Scope, approverID, status string, limit int) ([]*WorkflowApproval, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if approverID == "" {
		return nil, ErrInvalid
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	out := make([]*WorkflowApproval, 0)
	for _, r := range s.workflow.approvals[scope.TenantID] {
		if r.ApproverID == approverID && (status == "" || r.Status == status) {
			out = append(out, cloneWorkflowApproval(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.After(out[j].RequestedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemStore) DecideWorkflowApproval(_ context.Context, scope Scope, id, approverID, decision, note string, now time.Time) (*WorkflowApproval, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if decision != ApprovalApproved && decision != ApprovalRejected {
		return nil, ErrInvalid
	}
	if len([]rune(note)) > maxWorkflowTextLength {
		return nil, ErrInvalid
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	r := s.workflow.approvals[scope.TenantID][id]
	if r == nil || r.ApproverID != approverID {
		return nil, ErrNotFound
	}
	if r.Status != ApprovalPending {
		if r.Status == decision {
			return cloneWorkflowApproval(r), nil
		}
		return nil, ErrConflict
	}
	r.Status, r.DecisionNote, r.DecisionBy, r.DecidedAt = decision, note, approverID, now.UTC()
	execution := s.workflow.executions[scope.TenantID][r.ExecutionID]
	if execution != nil && execution.Status == ExecutionWaitingApproval {
		if decision == ApprovalApproved {
			execution.Status = ExecutionPending
			execution.ErrorCategory = ""
		} else {
			execution.Status = ExecutionFailed
			execution.ErrorCategory = "approval_rejected"
			execution.FinishedAt = now.UTC()
		}
	}
	return cloneWorkflowApproval(r), nil
}

func (s *MemStore) CreateWorkflowWebhookEndpoint(_ context.Context, scope Scope, v *WorkflowWebhookEndpoint) (*WorkflowWebhookEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	r := cloneWorkflowWebhookEndpoint(v)
	if err := r.Validate(); err != nil {
		return nil, err
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	now := nowStamp()
	r.CreatedAt, r.UpdatedAt = now, now
	if s.workflow.webhooks[scope.TenantID] == nil {
		s.workflow.webhooks[scope.TenantID] = map[string]*WorkflowWebhookEndpoint{}
	}
	for _, existing := range s.workflow.webhooks[scope.TenantID] {
		if strings.EqualFold(existing.Name, r.Name) {
			return nil, ErrConflict
		}
	}
	s.workflow.webhooks[scope.TenantID][r.ID] = cloneWorkflowWebhookEndpoint(r)
	return cloneWorkflowWebhookEndpoint(r), nil
}
func (s *MemStore) GetWorkflowWebhookEndpoint(_ context.Context, scope Scope, id string) (*WorkflowWebhookEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	r := s.workflow.webhooks[scope.TenantID][id]
	if r == nil {
		return nil, ErrNotFound
	}
	return cloneWorkflowWebhookEndpoint(r), nil
}
func (s *MemStore) ListWorkflowWebhookEndpoints(_ context.Context, scope Scope) ([]*WorkflowWebhookEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	out := make([]*WorkflowWebhookEndpoint, 0)
	for _, r := range s.workflow.webhooks[scope.TenantID] {
		out = append(out, cloneWorkflowWebhookEndpoint(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *MemStore) DeleteWorkflowWebhookEndpoint(_ context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	if s.workflow.webhooks[scope.TenantID][id] == nil {
		return ErrNotFound
	}
	delete(s.workflow.webhooks[scope.TenantID], id)
	return nil
}
func (s *MemStore) RecordWorkflowWebhookDelivery(_ context.Context, scope Scope, v *WorkflowWebhookDelivery) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if v == nil {
		return ErrInvalid
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	r := *v
	if r.ID == "" {
		r.ID = newID()
	}
	if r.AttemptedAt.IsZero() {
		r.AttemptedAt = nowStamp()
	}
	s.workflow.webhookDeliveries[scope.TenantID] = append(s.workflow.webhookDeliveries[scope.TenantID], &r)
	return nil
}
