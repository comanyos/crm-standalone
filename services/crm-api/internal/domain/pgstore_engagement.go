package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DueLeadAlerts discovers alert work across tenants but returns identifiers
// only. Personal data is read later through the scoped application pool.
func (s *PgStore) DueLeadAlerts(
	ctx context.Context, now time.Time, sla time.Duration, limit int,
) ([]LeadAlertRef, error) {
	if limit <= 0 {
		limit = 100
	}
	if sla <= 0 {
		sla = 15 * time.Minute
	}
	rows, err := s.drain.Query(ctx, `
		SELECT tenant_id, lead_id, alert_kind
		  FROM (
			SELECT l.tenant_id, l.id AS lead_id, 'initial'::text AS alert_kind,
			       l.created_at, 0 AS alert_order
			  FROM leads l
			  LEFT JOIN lead_alert_states a
			    ON a.tenant_id=l.tenant_id AND a.lead_id=l.id
			 WHERE a.initial_notified_at IS NULL
			UNION ALL
			SELECT l.tenant_id, l.id AS lead_id, 'sla'::text AS alert_kind,
			       l.created_at, 1 AS alert_order
			  FROM leads l
			  LEFT JOIN lead_alert_states a
			    ON a.tenant_id=l.tenant_id AND a.lead_id=l.id
			 WHERE a.sla_escalated_at IS NULL
			   AND l.status='new'
			   AND l.created_at <= $1
			   AND NOT EXISTS (
			       SELECT 1 FROM activities x
			        WHERE x.tenant_id=l.tenant_id
			          AND x.subject_type='lead' AND x.subject_id=l.id
			   )
		  ) due
		 ORDER BY created_at, tenant_id, lead_id, alert_order
		 LIMIT $2`, now.UTC().Add(-sla), limit)
	if err != nil {
		return nil, fmt.Errorf("due lead alerts: %w", err)
	}
	defer rows.Close()
	out := make([]LeadAlertRef, 0)
	for rows.Next() {
		var ref LeadAlertRef
		if err := rows.Scan(&ref.TenantID, &ref.LeadID, &ref.Kind); err != nil {
			return nil, fmt.Errorf("scan due lead alert: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// ClaimLeadAlert marks the event and creates all addressed notifications in
// one tenant-bound transaction. A second worker loses the timestamp update
// and returns claimed=false without creating duplicates.
func (s *PgStore) ClaimLeadAlert(
	ctx context.Context, scope Scope, alert LeadAlert,
) ([]*Notification, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	if alert.LeadID == "" || (alert.Kind != LeadAlertInitial && alert.Kind != LeadAlertSLA) {
		return nil, false, ErrInvalid
	}
	now := alert.Now.UTC()
	if now.IsZero() {
		now = nowStamp()
	}
	var (
		created []*Notification
		claimed bool
	)
	err := s.db.transaction(ctx, scope, func(txCtx context.Context) error {
		if _, err := s.db.Exec(txCtx, scope, `
			INSERT INTO lead_alert_states (tenant_id, lead_id, created_at, updated_at)
			VALUES ($1,$2,$3,$3)
			ON CONFLICT (tenant_id, lead_id) DO NOTHING`, scope.TenantID, alert.LeadID, now); err != nil {
			return fmt.Errorf("create lead alert state: %w", err)
		}

		var id string
		var err error
		if alert.Kind == LeadAlertInitial {
			err = s.db.QueryRow(txCtx, scope, `
				UPDATE lead_alert_states
				   SET initial_notified_at=$3, updated_at=$3
				 WHERE tenant_id=$1 AND lead_id=$2 AND initial_notified_at IS NULL
				 RETURNING lead_id`, scope.TenantID, alert.LeadID, now).Scan(&id)
		} else {
			err = s.db.QueryRow(txCtx, scope, `
				UPDATE lead_alert_states a
				   SET sla_escalated_at=$3, updated_at=$3
				  FROM leads l
				 WHERE a.tenant_id=$1 AND a.lead_id=$2 AND a.sla_escalated_at IS NULL
				   AND l.tenant_id=a.tenant_id AND l.id=a.lead_id
				   AND l.status='new' AND l.created_at <= $4
				   AND NOT EXISTS (
				       SELECT 1 FROM activities x
				        WHERE x.tenant_id=l.tenant_id
				          AND x.subject_type='lead' AND x.subject_id=l.id
				   )
				 RETURNING a.lead_id`, scope.TenantID, alert.LeadID, now, alert.SLACutoff.UTC()).Scan(&id)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim %s lead alert: %w", alert.Kind, err)
		}
		claimed = true

		seen := make(map[string]bool)
		for _, recipientID := range alert.RecipientIDs {
			recipientID = strings.TrimSpace(recipientID)
			if recipientID == "" || seen[recipientID] {
				continue
			}
			seen[recipientID] = true
			notification, err := s.CreateNotification(txCtx, scope, &Notification{
				RecipientID: recipientID, Kind: "lead_" + alert.Kind,
				Title: alert.Title, Body: alert.Body,
				SubjectType: SubjectLead, SubjectID: alert.LeadID,
			})
			if err != nil {
				return err
			}
			created = append(created, notification)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return created, claimed, nil
}

// DueNotificationDeliveries returns only tenant and notification ids. The
// recipient and message body stay behind tenant-scoped RLS.
func (s *PgStore) DueNotificationDeliveries(
	ctx context.Context, now time.Time, limit int,
) ([]NotificationDeliveryRef, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.drain.Query(ctx, `
		SELECT tenant_id, notification_id
		  FROM notification_deliveries
		 WHERE channel='email'
		   AND status IN ('pending','sending')
		   AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
		 ORDER BY next_attempt_at NULLS FIRST, created_at, tenant_id, notification_id
		 LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("due notification deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]NotificationDeliveryRef, 0)
	for rows.Next() {
		var ref NotificationDeliveryRef
		if err := rows.Scan(&ref.TenantID, &ref.NotificationID); err != nil {
			return nil, fmt.Errorf("scan due notification delivery: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *PgStore) ClaimNotificationDelivery(
	ctx context.Context, scope Scope, notificationID string, now, leaseUntil time.Time,
) (*NotificationDelivery, bool, error) {
	if !scope.Valid() {
		return nil, false, ErrNoTenant
	}
	var out *NotificationDelivery
	err := s.db.transaction(ctx, scope, func(txCtx context.Context) error {
		var (
			attempts      int
			next          *time.Time
			delivered     *time.Time
			errorCategory string
		)
		err := s.db.QueryRow(txCtx, scope, `
			UPDATE notification_deliveries
			   SET status='sending', attempts=attempts+1, next_attempt_at=$4,
			       error_category='', updated_at=$3
			 WHERE tenant_id=$1 AND notification_id=$2 AND channel='email'
			   AND status IN ('pending','sending')
			   AND (next_attempt_at IS NULL OR next_attempt_at <= $3)
			 RETURNING attempts, next_attempt_at, delivered_at, error_category`,
			scope.TenantID, notificationID, now.UTC(), leaseUntil.UTC()).Scan(
			&attempts, &next, &delivered, &errorCategory)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim notification delivery: %w", err)
		}
		notification, err := scanNotification(s.db.QueryRow(txCtx, scope, `
			SELECT `+notificationColumns+` FROM notifications
			 WHERE tenant_id=$1 AND id=$2`, scope.TenantID, notificationID))
		if err != nil {
			return fmt.Errorf("load claimed notification: %w", err)
		}
		out = &NotificationDelivery{
			Notification: notification, Status: NotificationDeliverySending, Attempts: attempts,
			NextAttemptAt: timeOrZero(next), DeliveredAt: timeOrZero(delivered),
			ErrorCategory: errorCategory,
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, out != nil, nil
}

func (s *PgStore) FinishNotificationDelivery(
	ctx context.Context, scope Scope, notificationID string,
	deliveredAt, nextAttemptAt time.Time, errorCategory string,
) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	status := NotificationDeliveryFailed
	if !deliveredAt.IsZero() {
		status = NotificationDeliverySent
		nextAttemptAt = time.Time{}
	} else if !nextAttemptAt.IsZero() {
		status = NotificationDeliveryPending
	}
	tag, err := s.db.Exec(ctx, scope, `
		UPDATE notification_deliveries
		   SET status=$3, next_attempt_at=$4, delivered_at=$5,
		       error_category=$6, updated_at=$7
		 WHERE tenant_id=$1 AND notification_id=$2 AND channel='email'`,
		scope.TenantID, notificationID, status, nullTimeValue(nextAttemptAt),
		nullTimeValue(deliveredAt), errorCategory, nowStamp())
	if err != nil {
		return fmt.Errorf("finish notification delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
