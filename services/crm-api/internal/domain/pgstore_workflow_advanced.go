package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const scheduleColumns = `id, rule_id, subject_type, subject_id, time_zone, recurrence,
	interval_count, status, next_run_at, ends_at, last_run_at, created_by, created_at, updated_at`

func scanWorkflowSchedule(row pgx.Row) (*WorkflowSchedule, error) {
	var v WorkflowSchedule
	var ends, last *time.Time
	if err := row.Scan(&v.ID, &v.RuleID, &v.SubjectType, &v.SubjectID, &v.TimeZone,
		&v.Recurrence, &v.Interval, &v.Status, &v.NextRunAt, &ends, &last,
		&v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.EndsAt, v.LastRunAt = timeOrZero(ends), timeOrZero(last)
	return &v, nil
}

func (s *PgStore) CreateWorkflowSchedule(ctx context.Context, scope Scope, v *WorkflowSchedule) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowSchedule(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	rule, err := s.GetWorkflowRule(ctx, scope, record.RuleID)
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
	if version.TriggerObjectType != record.SubjectType {
		return nil, ErrInvalid
	}
	if err := requireActivitySubject(ctx, s, scope, record.SubjectType, record.SubjectID); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()
	out, err := scanWorkflowSchedule(s.db.QueryRow(ctx, scope,
		`INSERT INTO workflow_schedules (tenant_id,id,rule_id,subject_type,subject_id,time_zone,
		 recurrence,interval_count,status,next_run_at,ends_at,last_run_at,created_by,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14) RETURNING `+scheduleColumns,
		scope.TenantID, record.ID, record.RuleID, record.SubjectType, record.SubjectID,
		record.TimeZone, record.Recurrence, record.Interval, record.Status, record.NextRunAt.UTC(),
		nullTimeValue(record.EndsAt), nullTimeValue(record.LastRunAt), record.CreatedBy, now))
	if err != nil {
		return nil, fmt.Errorf("create workflow schedule: %w", err)
	}
	return out, nil
}

func (s *PgStore) GetWorkflowSchedule(ctx context.Context, scope Scope, id string) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanWorkflowSchedule(s.db.QueryRow(ctx, scope,
		`SELECT `+scheduleColumns+` FROM workflow_schedules WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get workflow schedule: %w", err)
	}
	return out, nil
}

func (s *PgStore) ListWorkflowSchedules(ctx context.Context, scope Scope) ([]*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+scheduleColumns+` FROM workflow_schedules
		WHERE tenant_id=$1 ORDER BY next_run_at,id`, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list workflow schedules: %w", err)
	}
	defer rows.Close()
	out := make([]*WorkflowSchedule, 0, 8)
	for rows.Next() {
		v, err := scanWorkflowSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *PgStore) UpdateWorkflowSchedule(ctx context.Context, scope Scope, v *WorkflowSchedule) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowSchedule(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	out, err := scanWorkflowSchedule(s.db.QueryRow(ctx, scope,
		`UPDATE workflow_schedules SET time_zone=$3,recurrence=$4,interval_count=$5,status=$6,
		 next_run_at=$7,ends_at=$8,updated_at=$9 WHERE tenant_id=$1 AND id=$2 RETURNING `+scheduleColumns,
		scope.TenantID, record.ID, record.TimeZone, record.Recurrence, record.Interval, record.Status,
		record.NextRunAt.UTC(), nullTimeValue(record.EndsAt), nowStamp()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update workflow schedule: %w", err)
	}
	return out, nil
}

func (s *PgStore) DeleteWorkflowSchedule(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM workflow_schedules WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete workflow schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) DueWorkflowSchedules(ctx context.Context, scope Scope, now time.Time, limit int) ([]*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+scheduleColumns+` FROM workflow_schedules
		WHERE tenant_id=$1 AND status='active' AND next_run_at <= $2
		ORDER BY next_run_at,id LIMIT $3`, scope.TenantID, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("due workflow schedules: %w", err)
	}
	defer rows.Close()
	out := make([]*WorkflowSchedule, 0, 8)
	for rows.Next() {
		v, err := scanWorkflowSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *PgStore) AdvanceWorkflowSchedule(ctx context.Context, scope Scope, id string, expected, next time.Time, status string) (*WorkflowSchedule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if status != ScheduleActive && status != ScheduleCompleted {
		return nil, ErrInvalid
	}
	out, err := scanWorkflowSchedule(s.db.QueryRow(ctx, scope,
		`UPDATE workflow_schedules SET last_run_at=$3,next_run_at=CASE WHEN $4::timestamptz IS NULL THEN next_run_at ELSE $4 END,
		 status=$5,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND status='active' AND next_run_at=$6 RETURNING `+scheduleColumns,
		scope.TenantID, id, nowStamp(), nullTimeValue(next), status, expected.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("advance workflow schedule: %w", err)
	}
	return out, nil
}

const approvalColumns = `id,execution_id,rule_id,action_index,approver_id,title,body,subject_type,
	subject_id,status,decision_note,decision_by,requested_at,decided_at`

func scanWorkflowApproval(row pgx.Row) (*WorkflowApproval, error) {
	var v WorkflowApproval
	var decided *time.Time
	if err := row.Scan(&v.ID, &v.ExecutionID, &v.RuleID, &v.ActionIndex, &v.ApproverID, &v.Title, &v.Body,
		&v.SubjectType, &v.SubjectID, &v.Status, &v.DecisionNote, &v.DecisionBy, &v.RequestedAt, &decided); err != nil {
		return nil, err
	}
	v.DecidedAt = timeOrZero(decided)
	return &v, nil
}

func (s *PgStore) EnsureWorkflowApproval(ctx context.Context, scope Scope, v *WorkflowApproval) (*WorkflowApproval, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowApproval(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	if record.RequestedAt.IsZero() {
		record.RequestedAt = nowStamp()
	}
	_, err := s.db.Exec(ctx, scope, `INSERT INTO workflow_approvals
		(tenant_id,id,execution_id,rule_id,action_index,approver_id,title,body,subject_type,subject_id,status,requested_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (tenant_id,execution_id,action_index) DO NOTHING`, scope.TenantID, record.ID, record.ExecutionID,
		record.RuleID, record.ActionIndex, record.ApproverID, record.Title, record.Body, record.SubjectType, record.SubjectID, record.Status, record.RequestedAt)
	if err != nil {
		return nil, fmt.Errorf("ensure workflow approval: %w", err)
	}
	out, err := scanWorkflowApproval(s.db.QueryRow(ctx, scope, `SELECT `+approvalColumns+` FROM workflow_approvals
		WHERE tenant_id=$1 AND execution_id=$2 AND action_index=$3`, scope.TenantID, record.ExecutionID, record.ActionIndex))
	if err != nil {
		return nil, fmt.Errorf("read workflow approval: %w", err)
	}
	return out, nil
}

func (s *PgStore) ListWorkflowApprovals(ctx context.Context, scope Scope, approverID, status string, limit int) ([]*WorkflowApproval, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if approverID == "" {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+approvalColumns+` FROM workflow_approvals WHERE tenant_id=$1 AND approver_id=$2
		AND ($3='' OR status=$3) ORDER BY requested_at DESC,id LIMIT $4`, scope.TenantID, approverID, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list workflow approvals: %w", err)
	}
	defer rows.Close()
	out := make([]*WorkflowApproval, 0, 8)
	for rows.Next() {
		v, err := scanWorkflowApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *PgStore) DecideWorkflowApproval(ctx context.Context, scope Scope, id, approverID, decision, note string, now time.Time) (*WorkflowApproval, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if decision != ApprovalApproved && decision != ApprovalRejected {
		return nil, ErrInvalid
	}
	if len([]rune(note)) > maxWorkflowTextLength {
		return nil, ErrInvalid
	}
	var out *WorkflowApproval
	err := s.db.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		v, err := scanWorkflowApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM workflow_approvals WHERE tenant_id=$1 AND id=$2 AND approver_id=$3 FOR UPDATE`, scope.TenantID, id, approverID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if v.Status != ApprovalPending {
			if v.Status == decision {
				out = v
				return nil
			}
			return ErrConflict
		}
		v.Status, v.DecisionNote, v.DecisionBy, v.DecidedAt = decision, note, approverID, now.UTC()
		if _, err := tx.Exec(ctx, `UPDATE workflow_approvals SET status=$4,decision_note=$5,decision_by=$3,decided_at=$6 WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id, approverID, decision, note, now.UTC()); err != nil {
			return err
		}
		if decision == ApprovalApproved {
			_, err = tx.Exec(ctx, `UPDATE workflow_executions SET status='pending',error_category='',next_attempt_at=NULL,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND status='waiting_approval'`, scope.TenantID, v.ExecutionID, now.UTC())
		} else {
			_, err = tx.Exec(ctx, `UPDATE workflow_executions SET status='failed',error_category='approval_rejected',next_attempt_at=NULL,finished_at=$3,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND status='waiting_approval'`, scope.TenantID, v.ExecutionID, now.UTC())
		}
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const webhookEndpointColumns = `id,name,url,signing_secret_ciphertext,created_by,created_at,updated_at`

func scanWorkflowWebhookEndpoint(row pgx.Row) (*WorkflowWebhookEndpoint, error) {
	var v WorkflowWebhookEndpoint
	if err := row.Scan(&v.ID, &v.Name, &v.URL, &v.SigningSecretCiphertext, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *PgStore) CreateWorkflowWebhookEndpoint(ctx context.Context, scope Scope, v *WorkflowWebhookEndpoint) (*WorkflowWebhookEndpoint, error) {
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
	if r.ID == "" {
		r.ID = newID()
	}
	now := nowStamp()
	out, err := scanWorkflowWebhookEndpoint(s.db.QueryRow(ctx, scope, `INSERT INTO workflow_webhook_endpoints(tenant_id,id,name,url,signing_secret_ciphertext,created_by,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7)RETURNING `+webhookEndpointColumns, scope.TenantID, r.ID, r.Name, r.URL, r.SigningSecretCiphertext, r.CreatedBy, now))
	if err != nil {
		return nil, fmt.Errorf("create webhook endpoint: %w", err)
	}
	return out, nil
}
func (s *PgStore) GetWorkflowWebhookEndpoint(ctx context.Context, scope Scope, id string) (*WorkflowWebhookEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanWorkflowWebhookEndpoint(s.db.QueryRow(ctx, scope, `SELECT `+webhookEndpointColumns+` FROM workflow_webhook_endpoints WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}
func (s *PgStore) ListWorkflowWebhookEndpoints(ctx context.Context, scope Scope) ([]*WorkflowWebhookEndpoint, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope, `SELECT `+webhookEndpointColumns+` FROM workflow_webhook_endpoints WHERE tenant_id=$1 ORDER BY name,id`, scope.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*WorkflowWebhookEndpoint, 0, 8)
	for rows.Next() {
		v, err := scanWorkflowWebhookEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PgStore) DeleteWorkflowWebhookEndpoint(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope, `DELETE FROM workflow_webhook_endpoints WHERE tenant_id=$1 AND id=$2`, scope.TenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *PgStore) RecordWorkflowWebhookDelivery(ctx context.Context, scope Scope, v *WorkflowWebhookDelivery) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if v == nil {
		return ErrInvalid
	}
	if v.ID == "" {
		v.ID = newID()
	}
	if v.AttemptedAt.IsZero() {
		v.AttemptedAt = nowStamp()
	}
	_, err := s.db.Exec(ctx, scope, `INSERT INTO workflow_webhook_deliveries(tenant_id,id,endpoint_id,execution_id,action_index,status,response_code,attempted_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, scope.TenantID, v.ID, v.EndpointID, v.ExecutionID, v.ActionIndex, v.Status, v.ResponseCode, v.AttemptedAt)
	return err
}
