package domain

import (
	"context"
	"sort"
	"strings"
	"time"
)

// DueLeadAlerts returns identifier-only work across all in-memory tenants.
func (s *MemStore) DueLeadAlerts(
	ctx context.Context, now time.Time, sla time.Duration, limit int,
) ([]LeadAlertRef, error) {
	if limit <= 0 {
		limit = 100
	}

	// Snapshot the leads before consulting alert state. Keeping two table
	// locks at once would create a lock-order contract every write path would
	// have to know about.
	s.lead.mu.RLock()
	leads := make(map[string][]*Lead, len(s.lead.rows))
	for tenantID, rows := range s.lead.rows {
		for _, lead := range rows {
			leads[tenantID] = append(leads[tenantID], cloneLead(lead))
		}
	}
	s.lead.mu.RUnlock()

	s.workflow.mu.Lock()
	states := make(map[string]map[string]leadAlertState, len(s.workflow.leadAlerts))
	for tenantID, rows := range s.workflow.leadAlerts {
		states[tenantID] = make(map[string]leadAlertState, len(rows))
		for leadID, state := range rows {
			states[tenantID][leadID] = *state
		}
	}
	s.workflow.mu.Unlock()

	out := make([]LeadAlertRef, 0)
	cutoff := now.UTC().Add(-sla)
	for tenantID, rows := range leads {
		scope := Scope{TenantID: tenantID, OrgID: tenantID}
		for _, lead := range rows {
			state := states[tenantID][lead.ID]
			if state.initialNotifiedAt.IsZero() {
				out = append(out, LeadAlertRef{TenantID: tenantID, LeadID: lead.ID, Kind: LeadAlertInitial})
			}
			if lead.Status != LeadStatusNew || lead.CreatedAt.After(cutoff) || !state.slaEscalatedAt.IsZero() {
				continue
			}
			activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectLead, lead.ID)
			if err != nil {
				return nil, err
			}
			if len(activities) == 0 {
				out = append(out, LeadAlertRef{TenantID: tenantID, LeadID: lead.ID, Kind: LeadAlertSLA})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		if out[i].LeadID != out[j].LeadID {
			return out[i].LeadID < out[j].LeadID
		}
		return out[i].Kind < out[j].Kind
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ClaimLeadAlert records one alert and creates all recipient notifications as
// one in-memory critical section, mirroring PostgreSQL's transaction.
func (s *MemStore) ClaimLeadAlert(
	ctx context.Context, scope Scope, alert LeadAlert,
) ([]*Notification, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if alert.LeadID == "" || (alert.Kind != LeadAlertInitial && alert.Kind != LeadAlertSLA) {
		return nil, false, ErrInvalid
	}
	lead, err := s.GetLead(ctx, scope, alert.LeadID)
	if err != nil {
		return nil, false, err
	}
	if alert.Kind == LeadAlertSLA {
		if lead.Status != LeadStatusNew || lead.CreatedAt.After(alert.SLACutoff) {
			return nil, false, nil
		}
		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectLead, lead.ID)
		if err != nil {
			return nil, false, err
		}
		if len(activities) > 0 {
			return nil, false, nil
		}
	}

	now := alert.Now.UTC()
	if now.IsZero() {
		now = nowStamp()
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	if s.workflow.leadAlerts[scope.TenantID] == nil {
		s.workflow.leadAlerts[scope.TenantID] = make(map[string]*leadAlertState)
	}
	state := s.workflow.leadAlerts[scope.TenantID][alert.LeadID]
	if state == nil {
		state = &leadAlertState{}
		s.workflow.leadAlerts[scope.TenantID][alert.LeadID] = state
	}
	if alert.Kind == LeadAlertInitial {
		if !state.initialNotifiedAt.IsZero() {
			return nil, false, nil
		}
		state.initialNotifiedAt = now
	} else {
		if !state.slaEscalatedAt.IsZero() {
			return nil, false, nil
		}
		state.slaEscalatedAt = now
	}

	if s.workflow.notifications[scope.TenantID] == nil {
		s.workflow.notifications[scope.TenantID] = make(map[string]*Notification)
	}
	if s.workflow.deliveries[scope.TenantID] == nil {
		s.workflow.deliveries[scope.TenantID] = make(map[string]*NotificationDelivery)
	}
	seen := make(map[string]bool)
	out := make([]*Notification, 0, len(alert.RecipientIDs))
	for _, recipientID := range alert.RecipientIDs {
		recipientID = strings.TrimSpace(recipientID)
		if recipientID == "" || seen[recipientID] {
			continue
		}
		seen[recipientID] = true
		record := &Notification{
			ID: newID(), RecipientID: recipientID, Kind: "lead_" + alert.Kind,
			Title: alert.Title, Body: alert.Body,
			SubjectType: SubjectLead, SubjectID: alert.LeadID, CreatedAt: now,
		}
		if err := record.Validate(); err != nil {
			return nil, false, err
		}
		s.workflow.notifications[scope.TenantID][record.ID] = cloneNotification(record)
		s.workflow.deliveries[scope.TenantID][record.ID] = &NotificationDelivery{
			Notification: cloneNotification(record), Status: NotificationDeliveryPending,
		}
		out = append(out, cloneNotification(record))
	}
	return out, true, nil
}

func (s *MemStore) DueNotificationDeliveries(
	_ context.Context, now time.Time, limit int,
) ([]NotificationDeliveryRef, error) {
	if limit <= 0 {
		limit = 100
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	out := make([]NotificationDeliveryRef, 0)
	for tenantID, rows := range s.workflow.deliveries {
		for notificationID, delivery := range rows {
			if delivery.Status != NotificationDeliveryPending && delivery.Status != NotificationDeliverySending {
				continue
			}
			if !delivery.NextAttemptAt.IsZero() && now.UTC().Before(delivery.NextAttemptAt.UTC()) {
				continue
			}
			out = append(out, NotificationDeliveryRef{TenantID: tenantID, NotificationID: notificationID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].NotificationID < out[j].NotificationID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemStore) ClaimNotificationDelivery(
	_ context.Context, scope Scope, notificationID string, now, leaseUntil time.Time,
) (*NotificationDelivery, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	record := s.workflow.deliveries[scope.TenantID][notificationID]
	if record == nil {
		return nil, false, ErrNotFound
	}
	if record.Status != NotificationDeliveryPending && record.Status != NotificationDeliverySending {
		return cloneNotificationDelivery(record), false, nil
	}
	if !record.NextAttemptAt.IsZero() && now.UTC().Before(record.NextAttemptAt.UTC()) {
		return cloneNotificationDelivery(record), false, nil
	}
	record.Status = NotificationDeliverySending
	record.Attempts++
	record.NextAttemptAt = leaseUntil.UTC()
	return cloneNotificationDelivery(record), true, nil
}

func (s *MemStore) FinishNotificationDelivery(
	_ context.Context, scope Scope, notificationID string,
	deliveredAt, nextAttemptAt time.Time, errorCategory string,
) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	s.workflow.mu.Lock()
	defer s.workflow.mu.Unlock()
	record := s.workflow.deliveries[scope.TenantID][notificationID]
	if record == nil {
		return ErrNotFound
	}
	record.ErrorCategory = errorCategory
	record.NextAttemptAt = nextAttemptAt.UTC()
	if !deliveredAt.IsZero() {
		record.Status = NotificationDeliverySent
		record.DeliveredAt = deliveredAt.UTC()
		record.NextAttemptAt = time.Time{}
	} else if nextAttemptAt.IsZero() {
		record.Status = NotificationDeliveryFailed
	} else {
		record.Status = NotificationDeliveryPending
	}
	return nil
}

func cloneNotificationDelivery(v *NotificationDelivery) *NotificationDelivery {
	if v == nil {
		return nil
	}
	out := *v
	out.Notification = cloneNotification(v.Notification)
	return &out
}
