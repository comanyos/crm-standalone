package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of workflow automation
// (migrations/0023_workflow_automation.sql).

const workflowRuleColumns = `id, name, description, status, active_version_id,
	created_by, created_at, updated_at`

func scanWorkflowRule(row pgx.Row) (*WorkflowRule, error) {
	var (
		v       WorkflowRule
		version *string
	)
	if err := row.Scan(&v.ID, &v.Name, &v.Description, &v.Status, &version,
		&v.CreatedBy, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	if version != nil {
		v.ActiveVersionID = *version
	}
	return &v, nil
}

// nullActiveVersion sends NULL rather than an empty string, because the
// table's "an active rule has a version" CHECK is written against NULL.
func nullActiveVersion(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// CreateWorkflowRule stores a new rule.
func (s *PgStore) CreateWorkflowRule(
	ctx context.Context, scope Scope, v *WorkflowRule,
) (*WorkflowRule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowRule(v)
	if record.Status == "" {
		record.Status = WorkflowDraft
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()

	out, err := scanWorkflowRule(s.db.QueryRow(ctx, scope,
		`INSERT INTO workflow_rules
		     (tenant_id, id, name, description, status, active_version_id,
		      created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		 RETURNING `+workflowRuleColumns,
		scope.TenantID, record.ID, record.Name, record.Description, record.Status,
		nullActiveVersion(record.ActiveVersionID), record.CreatedBy, now))
	if err != nil {
		return nil, fmt.Errorf("create workflow rule: %w", err)
	}
	return out, nil
}

// GetWorkflowRule reads one rule.
func (s *PgStore) GetWorkflowRule(ctx context.Context, scope Scope, id string) (*WorkflowRule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanWorkflowRule(s.db.QueryRow(ctx, scope,
		`SELECT `+workflowRuleColumns+` FROM workflow_rules
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get workflow rule: %w", err)
	}
	return out, nil
}

// ListWorkflowRules returns the tenant's rules, newest first.
func (s *PgStore) ListWorkflowRules(
	ctx context.Context, scope Scope, includeArchived bool,
) ([]*WorkflowRule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	sql := `SELECT ` + workflowRuleColumns + ` FROM workflow_rules WHERE tenant_id = $1`
	if !includeArchived {
		sql += ` AND status <> 'archived'`
	}
	sql += ` ORDER BY created_at DESC, id`

	rows, err := s.db.Query(ctx, scope, sql, scope.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list workflow rules: %w", err)
	}
	defer rows.Close()

	out := make([]*WorkflowRule, 0, 8)
	for rows.Next() {
		record, err := scanWorkflowRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workflow rule: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// UpdateWorkflowRule changes a rule's name, state or published version.
func (s *PgStore) UpdateWorkflowRule(
	ctx context.Context, scope Scope, v *WorkflowRule,
) (*WorkflowRule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowRule(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	out, err := scanWorkflowRule(s.db.QueryRow(ctx, scope,
		`UPDATE workflow_rules
		    SET name = $3, description = $4, status = $5,
		        active_version_id = $6, updated_at = $7
		  WHERE tenant_id = $1 AND id = $2
		RETURNING `+workflowRuleColumns,
		scope.TenantID, record.ID, record.Name, record.Description, record.Status,
		nullActiveVersion(record.ActiveVersionID), nowStamp()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update workflow rule: %w", err)
	}
	return out, nil
}

const workflowVersionColumns = `id, rule_id, version, trigger_event_type, trigger_object_type,
	conditions, actions, created_by, created_at`

func scanWorkflowVersion(row pgx.Row) (*WorkflowRuleVersion, error) {
	var (
		v          WorkflowRuleVersion
		conditions []byte
		actions    []byte
	)
	if err := row.Scan(&v.ID, &v.RuleID, &v.Version, &v.TriggerEventType, &v.TriggerObjectType,
		&conditions, &actions, &v.CreatedBy, &v.CreatedAt); err != nil {
		return nil, err
	}
	if len(conditions) > 0 {
		if err := json.Unmarshal(conditions, &v.Conditions); err != nil {
			return nil, fmt.Errorf("decode workflow conditions: %w", err)
		}
	}
	if len(actions) > 0 {
		if err := json.Unmarshal(actions, &v.Actions); err != nil {
			return nil, fmt.Errorf("decode workflow actions: %w", err)
		}
	}
	return &v, nil
}

// CreateWorkflowRuleVersion writes an immutable snapshot and assigns the next
// version number.
//
// The number comes from a subquery in the same statement rather than a read
// followed by a write: two authors publishing at once would otherwise both
// compute the same next version and one would lose to the unique index with
// no way to recover but retry.
func (s *PgStore) CreateWorkflowRuleVersion(
	ctx context.Context, scope Scope, v *WorkflowRuleVersion,
) (*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowRuleVersion(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	conditions, err := record.MarshalConditions()
	if err != nil {
		return nil, err
	}
	actions, err := record.MarshalActions()
	if err != nil {
		return nil, err
	}

	// The rule must exist in this tenant; without the check a version could
	// be written against an id from another organization.
	if _, err := s.GetWorkflowRule(ctx, scope, record.RuleID); err != nil {
		return nil, err
	}

	out, err := scanWorkflowVersion(s.db.QueryRow(ctx, scope,
		`INSERT INTO workflow_rule_versions
		     (tenant_id, id, rule_id, version, trigger_event_type, trigger_object_type,
		      conditions, actions, created_by, created_at)
		 VALUES ($1,$2,$3,
		     (SELECT COALESCE(MAX(version), 0) + 1 FROM workflow_rule_versions
		       WHERE tenant_id = $1 AND rule_id = $3),
		     $4,$5,$6,$7,$8,$9)
		 RETURNING `+workflowVersionColumns,
		scope.TenantID, record.ID, record.RuleID, record.TriggerEventType,
		record.TriggerObjectType, conditions, actions, record.CreatedBy, nowStamp()))
	if err != nil {
		return nil, fmt.Errorf("create workflow rule version: %w", err)
	}
	return out, nil
}

// GetWorkflowRuleVersion reads one version.
func (s *PgStore) GetWorkflowRuleVersion(
	ctx context.Context, scope Scope, id string,
) (*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanWorkflowVersion(s.db.QueryRow(ctx, scope,
		`SELECT `+workflowVersionColumns+` FROM workflow_rule_versions
		  WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get workflow rule version: %w", err)
	}
	return out, nil
}

// ListWorkflowRuleVersions returns one rule's versions, newest first.
func (s *PgStore) ListWorkflowRuleVersions(
	ctx context.Context, scope Scope, ruleID string,
) ([]*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+workflowVersionColumns+` FROM workflow_rule_versions
		  WHERE tenant_id = $1 AND rule_id = $2 ORDER BY version DESC`,
		scope.TenantID, ruleID)
	if err != nil {
		return nil, fmt.Errorf("list workflow rule versions: %w", err)
	}
	defer rows.Close()

	out := make([]*WorkflowRuleVersion, 0, 4)
	for rows.Next() {
		record, err := scanWorkflowVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workflow rule version: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// ActiveWorkflowVersionsFor returns published versions of active rules whose
// trigger matches an event type.
func (s *PgStore) ActiveWorkflowVersionsFor(
	ctx context.Context, scope Scope, eventType string,
) ([]*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+prefixed(workflowVersionColumns, "v")+`
		   FROM workflow_rule_versions v
		   JOIN workflow_rules r
		     ON r.tenant_id = v.tenant_id AND r.active_version_id = v.id
		  WHERE v.tenant_id = $1
		    AND r.status = 'active'
		    AND v.trigger_event_type = $2
		  ORDER BY v.id`,
		scope.TenantID, eventType)
	if err != nil {
		return nil, fmt.Errorf("active workflow versions: %w", err)
	}
	defer rows.Close()

	out := make([]*WorkflowRuleVersion, 0, 4)
	for rows.Next() {
		record, err := scanWorkflowVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workflow rule version: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// prefixed qualifies a shared column list for a joined query, so the list
// itself stays single-sourced rather than written out twice and drifting.
func prefixed(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

const workflowExecutionColumns = `id, rule_id, rule_version_id, source_event_id, source_event_type,
	subject_type, subject_id, status, attempts, next_attempt_at, error_category,
	actions_completed, started_at, finished_at, created_at, updated_at`

func scanWorkflowExecution(row pgx.Row) (*WorkflowExecution, error) {
	var (
		v        WorkflowExecution
		next     *time.Time
		started  *time.Time
		finished *time.Time
	)
	if err := row.Scan(&v.ID, &v.RuleID, &v.RuleVersionID, &v.SourceEventID, &v.SourceEventType,
		&v.SubjectType, &v.SubjectID, &v.Status, &v.Attempts, &next, &v.ErrorCategory,
		&v.ActionsCompleted, &started, &finished, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.NextAttemptAt = timeOrZero(next)
	v.StartedAt = timeOrZero(started)
	v.FinishedAt = timeOrZero(finished)
	return &v, nil
}

// ClaimWorkflowExecution inserts an execution, or reports that this event was
// already claimed for this rule version.
//
// ON CONFLICT DO NOTHING against the idempotency index is the whole safety
// property: at-least-once outbox delivery means the second arrival of an
// event is routine, and it must produce nothing rather than a second
// follow-up task on a customer's record.
func (s *PgStore) ClaimWorkflowExecution(
	ctx context.Context, scope Scope, v *WorkflowExecution,
) (*WorkflowExecution, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if v == nil {
		return nil, false, ErrInvalid
	}
	record := cloneWorkflowExecution(v)
	if record.Status == "" {
		record.Status = ExecutionPending
	}
	if err := record.Validate(); err != nil {
		return nil, false, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()

	out, err := scanWorkflowExecution(s.db.QueryRow(ctx, scope,
		`INSERT INTO workflow_executions
		     (tenant_id, id, rule_id, rule_version_id, source_event_id, source_event_type,
		      subject_type, subject_id, status, attempts, next_attempt_at, error_category,
		      actions_completed, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,NULL,'',0,$10,$10)
		 ON CONFLICT (tenant_id, rule_version_id, source_event_id) DO NOTHING
		 RETURNING `+workflowExecutionColumns,
		scope.TenantID, record.ID, record.RuleID, record.RuleVersionID, record.SourceEventID,
		record.SourceEventType, record.SubjectType, record.SubjectID, record.Status, now))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already claimed. The expected case, not a fault.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("claim workflow execution: %w", err)
	}
	return out, true, nil
}

// UpdateWorkflowExecution records an attempt's outcome.
func (s *PgStore) UpdateWorkflowExecution(
	ctx context.Context, scope Scope, v *WorkflowExecution,
) (*WorkflowExecution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneWorkflowExecution(v)
	if err := record.Validate(); err != nil {
		return nil, err
	}
	out, err := scanWorkflowExecution(s.db.QueryRow(ctx, scope,
		`UPDATE workflow_executions
		    SET status = $3, attempts = $4, next_attempt_at = $5, error_category = $6,
		        actions_completed = $7, started_at = $8, finished_at = $9, updated_at = $10
		  WHERE tenant_id = $1 AND id = $2
		RETURNING `+workflowExecutionColumns,
		scope.TenantID, record.ID, record.Status, record.Attempts,
		nullTimeValue(record.NextAttemptAt), record.ErrorCategory, record.ActionsCompleted,
		nullTimeValue(record.StartedAt), nullTimeValue(record.FinishedAt), nowStamp()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update workflow execution: %w", err)
	}
	return out, nil
}

// ListWorkflowExecutions returns a rule's executions, newest first.
func (s *PgStore) ListWorkflowExecutions(
	ctx context.Context, scope Scope, ruleID string, limit int,
) ([]*WorkflowExecution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+workflowExecutionColumns+` FROM workflow_executions
		  WHERE tenant_id = $1 AND ($2::text IS NULL OR rule_id = $2)
		  ORDER BY created_at DESC, id
		  LIMIT $3`, scope.TenantID, nullString(ruleID), limit)
	if err != nil {
		return nil, fmt.Errorf("list workflow executions: %w", err)
	}
	defer rows.Close()

	out := make([]*WorkflowExecution, 0, 8)
	for rows.Next() {
		record, err := scanWorkflowExecution(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workflow execution: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// DueWorkflowExecutions returns retryable executions whose backoff elapsed.
func (s *PgStore) DueWorkflowExecutions(
	ctx context.Context, scope Scope, now time.Time, limit int,
) ([]*WorkflowExecution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+workflowExecutionColumns+` FROM workflow_executions
		  WHERE tenant_id = $1
		    AND status IN ('pending', 'running')
		    AND (next_attempt_at IS NULL OR next_attempt_at <= $2)
		  ORDER BY created_at, id
		  LIMIT $3`, scope.TenantID, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("due workflow executions: %w", err)
	}
	defer rows.Close()

	out := make([]*WorkflowExecution, 0, 8)
	for rows.Next() {
		record, err := scanWorkflowExecution(rows)
		if err != nil {
			return nil, fmt.Errorf("scan workflow execution: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// GetWorkflowCursor reads the engine's position in this tenant's outbox.
func (s *PgStore) GetWorkflowCursor(ctx context.Context, scope Scope) (*WorkflowCursor, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	var (
		v       WorkflowCursor
		created *time.Time
		running *time.Time
	)
	err := s.db.QueryRow(ctx, scope,
		`SELECT last_event_created_at, last_event_id, running_until, updated_at
		   FROM workflow_cursors WHERE tenant_id = $1`, scope.TenantID).
		Scan(&created, &v.LastEventID, &running, &v.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get workflow cursor: %w", err)
	}
	v.LastEventCreatedAt = timeOrZero(created)
	v.RunningUntil = timeOrZero(running)
	return &v, nil
}

// SaveWorkflowCursor advances the engine's position.
func (s *PgStore) SaveWorkflowCursor(ctx context.Context, scope Scope, v *WorkflowCursor) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if v == nil {
		return ErrInvalid
	}
	if _, err := s.db.Exec(ctx, scope,
		`INSERT INTO workflow_cursors
		     (tenant_id, last_event_created_at, last_event_id, running_until, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$5)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		      last_event_created_at = EXCLUDED.last_event_created_at,
		      last_event_id         = EXCLUDED.last_event_id,
		      running_until         = EXCLUDED.running_until,
		      updated_at            = EXCLUDED.updated_at`,
		scope.TenantID, nullTimeValue(v.LastEventCreatedAt), v.LastEventID,
		nullTimeValue(v.RunningUntil), nowStamp()); err != nil {
		return fmt.Errorf("save workflow cursor: %w", err)
	}
	return nil
}

// ClaimWorkflowCursor leases a tenant's automation for one batch.
func (s *PgStore) ClaimWorkflowCursor(
	ctx context.Context, scope Scope, now time.Time, lease time.Duration,
) (*WorkflowCursor, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	stamp := now.UTC().Truncate(storedTimePrecision)
	until := stamp.Add(lease)

	var (
		v       WorkflowCursor
		created *time.Time
		running *time.Time
	)
	err := s.db.QueryRow(ctx, scope,
		`INSERT INTO workflow_cursors (tenant_id, running_until, created_at, updated_at)
		 VALUES ($1,$2,$3,$3)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		      running_until = EXCLUDED.running_until, updated_at = EXCLUDED.updated_at
		 WHERE workflow_cursors.running_until IS NULL
		    OR workflow_cursors.running_until <= $3
		 RETURNING last_event_created_at, last_event_id, running_until, updated_at`,
		scope.TenantID, until, stamp).
		Scan(&created, &v.LastEventID, &running, &v.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("claim workflow cursor: %w", err)
	}
	v.LastEventCreatedAt = timeOrZero(created)
	v.RunningUntil = timeOrZero(running)
	return &v, nil
}

// OutboxEventsAfter returns the tenant's events following a cursor position.
//
// The comparison is on (created_at, id) rather than created_at alone: several
// events routinely share a millisecond, and a batch that ended inside such a
// group would otherwise either repeat them or skip them.
func (s *PgStore) OutboxEventsAfter(
	ctx context.Context, scope Scope, after WorkflowCursor, limit int,
) ([]*OutboxEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if limit <= 0 {
		limit = 100
	}
	return s.queryOutboxEvents(ctx, scope,
		`SELECT `+outboxCols+` FROM event_outbox
		  WHERE tenant_id = $1
		    AND ($2::timestamptz IS NULL
		         OR created_at > $2
		         OR (created_at = $2 AND id > $3))
		  ORDER BY created_at, id
		  LIMIT $4`,
		scope.TenantID, nullTimeValue(after.LastEventCreatedAt), after.LastEventID, limit)
}

// DueWorkflowTenants lists tenants whose automation is unclaimed.
//
// It runs on the drain pool for the same reason the other cross-tenant reads
// do, and returns identifiers only. Migration 0023 grants crm_drain SELECT on
// workflow_cursors alone — never on the rules, executions or notifications.
func (s *PgStore) DueWorkflowTenants(
	ctx context.Context, now time.Time, limit int,
) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	// A tenant with no cursor row has never run automation, so the left join
	// is what lets a newly activated rule start without a registration step.
	rows, err := s.drain.Query(ctx,
		`SELECT DISTINCT r.tenant_id
		   FROM workflow_rules r
		   LEFT JOIN workflow_cursors c ON c.tenant_id = r.tenant_id
		  WHERE r.status = 'active'
		    AND (c.running_until IS NULL OR c.running_until <= $1)
		  ORDER BY r.tenant_id
		  LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("due workflow tenants: %w", err)
	}
	defer rows.Close()

	out := make([]string, 0, 8)
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, fmt.Errorf("scan workflow tenant: %w", err)
		}
		out = append(out, tenant)
	}
	return out, rows.Err()
}

const notificationColumns = `id, recipient_id, kind, title, body, subject_type, subject_id,
	rule_id, read_at, created_at`

func scanNotification(row pgx.Row) (*Notification, error) {
	var (
		v    Notification
		read *time.Time
	)
	if err := row.Scan(&v.ID, &v.RecipientID, &v.Kind, &v.Title, &v.Body,
		&v.SubjectType, &v.SubjectID, &v.RuleID, &read, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.ReadAt = timeOrZero(read)
	return &v, nil
}

// CreateNotification stores an in-app message.
func (s *PgStore) CreateNotification(
	ctx context.Context, scope Scope, v *Notification,
) (*Notification, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneNotification(v)
	if record.Kind == "" {
		record.Kind = "workflow"
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	out, err := scanNotification(s.db.QueryRow(ctx, scope,
		`INSERT INTO notifications
		     (tenant_id, id, recipient_id, kind, title, body, subject_type, subject_id,
		      rule_id, read_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL,$10)
		 RETURNING `+notificationColumns,
		scope.TenantID, record.ID, record.RecipientID, record.Kind, record.Title, record.Body,
		record.SubjectType, record.SubjectID, record.RuleID, nowStamp()))
	if err != nil {
		return nil, fmt.Errorf("create notification: %w", err)
	}
	return out, nil
}

// ListNotifications returns one principal's own notifications, newest first.
func (s *PgStore) ListNotifications(
	ctx context.Context, scope Scope, recipientID string, unreadOnly bool, limit int,
) ([]*Notification, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if recipientID == "" {
		return nil, ErrInvalid
	}
	if limit <= 0 {
		limit = 50
	}
	sql := `SELECT ` + notificationColumns + ` FROM notifications
	         WHERE tenant_id = $1 AND recipient_id = $2`
	if unreadOnly {
		sql += ` AND read_at IS NULL`
	}
	sql += ` ORDER BY created_at DESC, id LIMIT $3`

	rows, err := s.db.Query(ctx, scope, sql, scope.TenantID, recipientID, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	out := make([]*Notification, 0, 8)
	for rows.Next() {
		record, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// MarkNotificationRead marks one of the caller's own notifications read.
//
// The recipient is in the WHERE clause rather than checked afterwards, so
// marking somebody else's is indistinguishable from one that does not exist.
func (s *PgStore) MarkNotificationRead(
	ctx context.Context, scope Scope, recipientID, id string,
) (*Notification, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	out, err := scanNotification(s.db.QueryRow(ctx, scope,
		`UPDATE notifications SET read_at = COALESCE(read_at, $4)
		  WHERE tenant_id = $1 AND id = $2 AND recipient_id = $3
		RETURNING `+notificationColumns,
		scope.TenantID, id, recipientID, nowStamp()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("mark notification read: %w", err)
	}
	return out, nil
}
