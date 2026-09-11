// Package engagement runs durable speed-to-lead alerts and email delivery.
package engagement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

const (
	defaultInterval = 5 * time.Second
	defaultLeadSLA  = 15 * time.Minute
	workPerCycle    = 100
	deliveryLease   = time.Minute
	maxAttempts     = 6
)

type Store interface {
	DueLeadAlerts(context.Context, time.Time, time.Duration, int) ([]domain.LeadAlertRef, error)
	GetLead(context.Context, domain.Scope, string) (*domain.Lead, error)
	ClaimLeadAlert(context.Context, domain.Scope, domain.LeadAlert) ([]*domain.Notification, bool, error)
	DueNotificationDeliveries(context.Context, time.Time, int) ([]domain.NotificationDeliveryRef, error)
	ClaimNotificationDelivery(context.Context, domain.Scope, string, time.Time, time.Time) (*domain.NotificationDelivery, bool, error)
	FinishNotificationDelivery(context.Context, domain.Scope, string, time.Time, time.Time, string) error
}

type Directory interface {
	AlertRecipientIDs(context.Context, string, string, bool) ([]string, error)
	SendUserEmail(context.Context, string, string, string) error
}

type Poller struct {
	store      Store
	directory  Directory
	sla        time.Duration
	webBaseURL string
	interval   time.Duration
	log        *slog.Logger
	now        func() time.Time
}

func New(
	store Store, directory Directory, sla time.Duration, webBaseURL string,
	interval time.Duration, log *slog.Logger,
) *Poller {
	if sla <= 0 {
		sla = defaultLeadSLA
	}
	if interval <= 0 {
		interval = defaultInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		store: store, directory: directory, sla: sla,
		webBaseURL: strings.TrimRight(webBaseURL, "/"), interval: interval,
		log: log, now: func() time.Time { return time.Now().UTC() },
	}
}

// RunOnce performs one bounded pass. It is public so tests can exercise
// claims and retry timing without sleeping.
func (p *Poller) RunOnce(ctx context.Context) error {
	if p == nil || p.store == nil || p.directory == nil {
		return errors.New("engagement: store and directory are required")
	}
	now := p.now().UTC()
	var errs []error
	if err := p.processLeadAlerts(ctx, now); err != nil {
		errs = append(errs, err)
	}
	if err := p.processDeliveries(ctx, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (p *Poller) processLeadAlerts(ctx context.Context, now time.Time) error {
	refs, err := p.store.DueLeadAlerts(ctx, now, p.sla, workPerCycle)
	if err != nil {
		return err
	}
	var errs []error
	for _, ref := range refs {
		scope := domain.Scope{TenantID: ref.TenantID, OrgID: ref.TenantID}
		lead, err := p.store.GetLead(ctx, scope, ref.LeadID)
		if err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				errs = append(errs, fmt.Errorf("load alert lead: %w", err))
			}
			continue
		}
		escalate := ref.Kind == domain.LeadAlertSLA || lead.OwnerID == ""
		recipients, err := p.directory.AlertRecipientIDs(ctx, ref.TenantID, lead.OwnerID, escalate)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve alert recipients: %w", err))
			continue
		}
		title, body := alertCopy(ref.Kind, lead, p.sla)
		_, _, err = p.store.ClaimLeadAlert(ctx, scope, domain.LeadAlert{
			LeadID: lead.ID, Kind: ref.Kind, RecipientIDs: recipients,
			Title: title, Body: body, Now: now, SLACutoff: now.Add(-p.sla),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("claim lead alert: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (p *Poller) processDeliveries(ctx context.Context, now time.Time) error {
	refs, err := p.store.DueNotificationDeliveries(ctx, now, workPerCycle)
	if err != nil {
		return err
	}
	var errs []error
	for _, ref := range refs {
		scope := domain.Scope{TenantID: ref.TenantID, OrgID: ref.TenantID}
		delivery, claimed, err := p.store.ClaimNotificationDelivery(
			ctx, scope, ref.NotificationID, now, now.Add(deliveryLease))
		if err != nil {
			errs = append(errs, fmt.Errorf("claim email delivery: %w", err))
			continue
		}
		if !claimed || delivery == nil || delivery.Notification == nil {
			continue
		}
		notification := delivery.Notification
		body := notification.Body
		if p.webBaseURL != "" && notification.SubjectType != "" && notification.SubjectID != "" {
			body += "\n\nOpen in Savvy CRM: " + p.subjectURL(notification)
		}
		err = p.directory.SendUserEmail(ctx, notification.RecipientID, notification.Title, body)
		if err == nil {
			if finishErr := p.store.FinishNotificationDelivery(
				ctx, scope, ref.NotificationID, now, time.Time{}, ""); finishErr != nil {
				errs = append(errs, fmt.Errorf("record email delivery: %w", finishErr))
			}
			continue
		}

		next := time.Time{}
		if delivery.Attempts < maxAttempts {
			next = now.Add(deliveryBackoff(delivery.Attempts))
		}
		if finishErr := p.store.FinishNotificationDelivery(
			ctx, scope, ref.NotificationID, time.Time{}, next, "delivery_failed"); finishErr != nil {
			errs = append(errs, fmt.Errorf("record email failure: %w", finishErr))
		}
		p.log.Warn("notification email delivery failed",
			"tenant", ref.TenantID, "notification", ref.NotificationID,
			"attempt", delivery.Attempts, "retrying", !next.IsZero())
	}
	return errors.Join(errs...)
}

func (p *Poller) subjectURL(notification *domain.Notification) string {
	segments := map[string]string{
		domain.SubjectLead: "leads", domain.SubjectAccount: "accounts",
		domain.SubjectContact: "contacts", domain.SubjectOpportunity: "opportunities",
	}
	segment := segments[notification.SubjectType]
	if segment == "" {
		return p.webBaseURL
	}
	return p.webBaseURL + "/" + segment + "/" + url.PathEscape(notification.SubjectID)
}

func alertCopy(kind string, lead *domain.Lead, sla time.Duration) (string, string) {
	name := strings.TrimSpace(lead.FirstName + " " + lead.LastName)
	if name == "" {
		name = strings.TrimSpace(lead.Company)
	}
	if name == "" {
		name = strings.TrimSpace(lead.ContactEmail)
	}
	if name == "" {
		name = strings.TrimSpace(lead.Phone)
	}
	if name == "" {
		name = "New lead"
	}
	if kind == domain.LeadAlertSLA {
		return "Lead SLA needs attention: " + name,
			fmt.Sprintf("This lead is still new with no recorded activity after %d minutes. Review ownership and take the next action.", int(sla/time.Minute))
	}
	return "New lead assigned: " + name,
		"A new lead is ready in Savvy CRM. Review the details and make the first contact."
}

func deliveryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute * time.Duration(1<<uint(attempt-1))
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}

// Start runs immediately and then at the configured cadence until ctx ends.
func Start(
	ctx context.Context, store Store, directory Directory, sla time.Duration,
	webBaseURL string, interval time.Duration, log *slog.Logger,
) {
	poller := New(store, directory, sla, webBaseURL, interval, log)
	go func() {
		if err := poller.RunOnce(ctx); err != nil && ctx.Err() == nil {
			poller.log.Warn("engagement worker pass failed", "err", err)
		}
		ticker := time.NewTicker(poller.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := poller.RunOnce(ctx); err != nil && ctx.Err() == nil {
					poller.log.Warn("engagement worker pass failed", "err", err)
				}
			}
		}
	}()
}
