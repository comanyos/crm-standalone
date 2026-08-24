package domain

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The in-memory half of workflow automation.
//
// Its job is to behave exactly as PostgreSQL does on the one property the
// feature rests on: an event claimed once for a rule version cannot be
// claimed again. Everything else here is bookkeeping.

type workflowTables struct {
	mu         sync.Mutex
	rules      map[string]map[string]*WorkflowRule
	versions   map[string]map[string]*WorkflowRuleVersion
	executions map[string]map[string]*WorkflowExecution
	// claimed mirrors the unique index: tenant -> "version|event" -> true.
	claimed       map[string]map[string]bool
	notifications map[string]map[string]*Notification
	cursors       map[string]*WorkflowCursor
}

func newWorkflowTables() *workflowTables {
	return &workflowTables{
		rules:         make(map[string]map[string]*WorkflowRule),
		versions:      make(map[string]map[string]*WorkflowRuleVersion),
		executions:    make(map[string]map[string]*WorkflowExecution),
		claimed:       make(map[string]map[string]bool),
		notifications: make(map[string]map[string]*Notification),
		cursors:       make(map[string]*WorkflowCursor),
	}
}

func executionKey(versionID, eventID string) string { return versionID + "|" + eventID }

// CreateWorkflowRule stores a new rule.
func (s *MemStore) CreateWorkflowRule(
	_ context.Context, scope Scope, v *WorkflowRule,
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

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()
	record.CreatedAt, record.UpdatedAt = now, now
	if s.workflow.rules[scope.TenantID] == nil {
		s.workflow.rules[scope.TenantID] = make(map[string]*WorkflowRule)
	}
	s.workflow.rules[scope.TenantID][record.ID] = cloneWorkflowRule(record)
	return cloneWorkflowRule(record), nil
}

// GetWorkflowRule reads one rule.
func (s *MemStore) GetWorkflowRule(
	_ context.Context, scope Scope, id string,
) (*WorkflowRule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	record, ok := s.workflow.rules[scope.TenantID][id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneWorkflowRule(record), nil
}

// ListWorkflowRules returns the tenant's rules, newest first.
func (s *MemStore) ListWorkflowRules(
	_ context.Context, scope Scope, includeArchived bool,
) ([]*WorkflowRule, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]*WorkflowRule, 0, len(s.workflow.rules[scope.TenantID]))
	for _, record := range s.workflow.rules[scope.TenantID] {
		if !includeArchived && record.Status == WorkflowArchived {
			continue
		}
		out = append(out, cloneWorkflowRule(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// UpdateWorkflowRule changes a rule's name, description, state or published
// version.
func (s *MemStore) UpdateWorkflowRule(
	_ context.Context, scope Scope, v *WorkflowRule,
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

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	existing, ok := s.workflow.rules[scope.TenantID][record.ID]
	if !ok {
		return nil, ErrNotFound
	}
	record.CreatedAt = existing.CreatedAt
	record.CreatedBy = existing.CreatedBy
	record.UpdatedAt = nowStamp()
	s.workflow.rules[scope.TenantID][record.ID] = cloneWorkflowRule(record)
	return cloneWorkflowRule(record), nil
}

// CreateWorkflowRuleVersion writes an immutable snapshot.
func (s *MemStore) CreateWorkflowRuleVersion(
	_ context.Context, scope Scope, v *WorkflowRuleVersion,
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

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	if _, ok := s.workflow.rules[scope.TenantID][record.RuleID]; !ok {
		return nil, ErrNotFound
	}
	// The next version number, under the same lock that writes it, so two
	// concurrent publishes cannot both claim version 3.
	highest := 0
	for _, existing := range s.workflow.versions[scope.TenantID] {
		if existing.RuleID == record.RuleID && existing.Version > highest {
			highest = existing.Version
		}
	}
	record.Version = highest + 1
	if record.ID == "" {
		record.ID = newID()
	}
	record.CreatedAt = nowStamp()
	if s.workflow.versions[scope.TenantID] == nil {
		s.workflow.versions[scope.TenantID] = make(map[string]*WorkflowRuleVersion)
	}
	s.workflow.versions[scope.TenantID][record.ID] = cloneWorkflowRuleVersion(record)
	return cloneWorkflowRuleVersion(record), nil
}

// GetWorkflowRuleVersion reads one version.
func (s *MemStore) GetWorkflowRuleVersion(
	_ context.Context, scope Scope, id string,
) (*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	record, ok := s.workflow.versions[scope.TenantID][id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneWorkflowRuleVersion(record), nil
}

// ListWorkflowRuleVersions returns one rule's versions, newest first.
func (s *MemStore) ListWorkflowRuleVersions(
	_ context.Context, scope Scope, ruleID string,
) ([]*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]*WorkflowRuleVersion, 0, 4)
	for _, record := range s.workflow.versions[scope.TenantID] {
		if record.RuleID == ruleID {
			out = append(out, cloneWorkflowRuleVersion(record))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// ActiveWorkflowVersionsFor returns the published versions of active rules
// whose trigger matches an event type.
func (s *MemStore) ActiveWorkflowVersionsFor(
	_ context.Context, scope Scope, eventType string,
) ([]*WorkflowRuleVersion, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]*WorkflowRuleVersion, 0, 4)
	for _, rule := range s.workflow.rules[scope.TenantID] {
		if rule.Status != WorkflowActive || rule.ActiveVersionID == "" {
			continue
		}
		version, ok := s.workflow.versions[scope.TenantID][rule.ActiveVersionID]
		// version.RuleID is checked as well: a rule runs its own versions,
		// and the pointer between them is not a foreign key.
		if !ok || version.RuleID != rule.ID || version.TriggerEventType != eventType {
			continue
		}
		out = append(out, cloneWorkflowRuleVersion(version))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ClaimWorkflowExecution inserts an execution, or reports that this event was
// already claimed for this rule version.
func (s *MemStore) ClaimWorkflowExecution(
	_ context.Context, scope Scope, v *WorkflowExecution,
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

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	key := executionKey(record.RuleVersionID, record.SourceEventID)
	if s.workflow.claimed[scope.TenantID] == nil {
		s.workflow.claimed[scope.TenantID] = make(map[string]bool)
	}
	if s.workflow.claimed[scope.TenantID][key] {
		return nil, false, nil
	}

	if record.ID == "" {
		record.ID = newID()
	}
	now := nowStamp()
	record.CreatedAt, record.UpdatedAt = now, now
	s.workflow.claimed[scope.TenantID][key] = true
	if s.workflow.executions[scope.TenantID] == nil {
		s.workflow.executions[scope.TenantID] = make(map[string]*WorkflowExecution)
	}
	s.workflow.executions[scope.TenantID][record.ID] = cloneWorkflowExecution(record)
	return cloneWorkflowExecution(record), true, nil
}

// UpdateWorkflowExecution records an attempt's outcome.
func (s *MemStore) UpdateWorkflowExecution(
	_ context.Context, scope Scope, v *WorkflowExecution,
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

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	existing, ok := s.workflow.executions[scope.TenantID][record.ID]
	if !ok {
		return nil, ErrNotFound
	}
	record.CreatedAt = existing.CreatedAt
	record.UpdatedAt = nowStamp()
	s.workflow.executions[scope.TenantID][record.ID] = cloneWorkflowExecution(record)
	return cloneWorkflowExecution(record), nil
}

// ListWorkflowExecutions returns a rule's executions, newest first. An empty
// ruleID lists the tenant's.
func (s *MemStore) ListWorkflowExecutions(
	_ context.Context, scope Scope, ruleID string, limit int,
) ([]*WorkflowExecution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]*WorkflowExecution, 0, 8)
	for _, record := range s.workflow.executions[scope.TenantID] {
		if ruleID != "" && record.RuleID != ruleID {
			continue
		}
		out = append(out, cloneWorkflowExecution(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DueWorkflowExecutions returns retryable executions whose backoff elapsed.
func (s *MemStore) DueWorkflowExecutions(
	_ context.Context, scope Scope, now time.Time, limit int,
) ([]*WorkflowExecution, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]*WorkflowExecution, 0, 8)
	for _, record := range s.workflow.executions[scope.TenantID] {
		if record.Status != ExecutionPending && record.Status != ExecutionRunning {
			continue
		}
		if !record.NextAttemptAt.IsZero() && now.UTC().Before(record.NextAttemptAt.UTC()) {
			continue
		}
		out = append(out, cloneWorkflowExecution(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetWorkflowCursor reads the engine's position in this tenant's outbox.
func (s *MemStore) GetWorkflowCursor(_ context.Context, scope Scope) (*WorkflowCursor, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	record, ok := s.workflow.cursors[scope.TenantID]
	if !ok {
		return nil, ErrNotFound
	}
	out := *record
	return &out, nil
}

// SaveWorkflowCursor advances the engine's position.
func (s *MemStore) SaveWorkflowCursor(_ context.Context, scope Scope, v *WorkflowCursor) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	if v == nil {
		return ErrInvalid
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	record := *v
	record.UpdatedAt = nowStamp()
	s.workflow.cursors[scope.TenantID] = &record
	return nil
}

// ClaimWorkflowCursor leases a tenant's automation for one batch.
func (s *MemStore) ClaimWorkflowCursor(
	_ context.Context, scope Scope, now time.Time, lease time.Duration,
) (*WorkflowCursor, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	record, ok := s.workflow.cursors[scope.TenantID]
	if !ok {
		record = &WorkflowCursor{}
	} else if !record.RunningUntil.IsZero() && now.UTC().Before(record.RunningUntil.UTC()) {
		return nil, ErrConflict
	}
	record.RunningUntil = now.UTC().Add(lease).Truncate(storedTimePrecision)
	record.UpdatedAt = nowStamp()
	s.workflow.cursors[scope.TenantID] = record
	out := *record
	return &out, nil
}

// OutboxEventsAfter returns the tenant's events following a cursor position.
func (s *MemStore) OutboxEventsAfter(
	_ context.Context, scope Scope, after WorkflowCursor, limit int,
) ([]*OutboxEvent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	all, err := s.outbox.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*OutboxEvent, 0, len(all))
	for _, event := range all {
		if afterCursor(event, after) {
			out = append(out, event)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// afterCursor is the (created_at, id) comparison the SQL does, kept here so
// the two orderings cannot disagree at a timestamp boundary — where several
// events share a millisecond, which is exactly when a batch ends.
func afterCursor(event *OutboxEvent, after WorkflowCursor) bool {
	if after.LastEventCreatedAt.IsZero() {
		return true
	}
	if event.CreatedAt.After(after.LastEventCreatedAt) {
		return true
	}
	if event.CreatedAt.Equal(after.LastEventCreatedAt) {
		return event.ID > after.LastEventID
	}
	return false
}

// DueWorkflowTenants lists tenants whose automation is unclaimed.
func (s *MemStore) DueWorkflowTenants(
	_ context.Context, now time.Time, limit int,
) ([]string, error) {
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]string, 0, len(s.workflow.rules))
	for tenant, rules := range s.workflow.rules {
		active := false
		for _, rule := range rules {
			if rule.Status == WorkflowActive {
				active = true
				break
			}
		}
		if !active {
			continue
		}
		if cursor, ok := s.workflow.cursors[tenant]; ok &&
			!cursor.RunningUntil.IsZero() && now.UTC().Before(cursor.RunningUntil.UTC()) {
			continue
		}
		out = append(out, tenant)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CreateNotification stores an in-app message.
func (s *MemStore) CreateNotification(
	_ context.Context, scope Scope, v *Notification,
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

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	if record.ID == "" {
		record.ID = newID()
	}
	record.CreatedAt = nowStamp()
	if s.workflow.notifications[scope.TenantID] == nil {
		s.workflow.notifications[scope.TenantID] = make(map[string]*Notification)
	}
	s.workflow.notifications[scope.TenantID][record.ID] = cloneNotification(record)
	return cloneNotification(record), nil
}

// ListNotifications returns one principal's own notifications, newest first.
func (s *MemStore) ListNotifications(
	_ context.Context, scope Scope, recipientID string, unreadOnly bool, limit int,
) ([]*Notification, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if recipientID == "" {
		// A blank recipient would list the tenant's. Refusing is the point:
		// the recipient is a parameter rather than a filter so no call site
		// can omit it.
		return nil, ErrInvalid
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	out := make([]*Notification, 0, 8)
	for _, record := range s.workflow.notifications[scope.TenantID] {
		if record.RecipientID != recipientID {
			continue
		}
		if unreadOnly && !record.ReadAt.IsZero() {
			continue
		}
		out = append(out, cloneNotification(record))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkNotificationRead marks one of the caller's own notifications read.
func (s *MemStore) MarkNotificationRead(
	_ context.Context, scope Scope, recipientID, id string,
) (*Notification, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()

	record, ok := s.workflow.notifications[scope.TenantID][id]
	// The recipient is part of the lookup, not checked afterwards: marking
	// somebody else's notification read must be indistinguishable from one
	// that does not exist.
	if !ok || record.RecipientID != recipientID {
		return nil, ErrNotFound
	}
	if record.ReadAt.IsZero() {
		record.ReadAt = nowStamp()
	}
	return cloneNotification(record), nil
}
