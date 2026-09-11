package domain

import "time"

const (
	LeadAlertInitial = "initial"
	LeadAlertSLA     = "sla"

	NotificationDeliveryPending = "pending"
	NotificationDeliverySending = "sending"
	NotificationDeliverySent    = "sent"
	NotificationDeliveryFailed  = "failed"
)

// LeadAlertRef is the identifier-only cross-tenant work item returned to the
// engagement poller. The lead's personal data is loaded later under Scope.
type LeadAlertRef struct {
	TenantID string
	LeadID   string
	Kind     string
}

// LeadAlert is the tenant-scoped, idempotent notification request the poller
// builds after resolving active recipients.
type LeadAlert struct {
	LeadID       string
	Kind         string
	RecipientIDs []string
	Title        string
	Body         string
	Now          time.Time
	SLACutoff    time.Time
}

// NotificationDeliveryRef is the identifier-only cross-tenant delivery work
// item. Bodies and recipients never cross the drain connection.
type NotificationDeliveryRef struct {
	TenantID       string
	NotificationID string
}

// NotificationDelivery is one claimed email attempt plus the addressed
// in-app notification it delivers.
type NotificationDelivery struct {
	Notification  *Notification
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	DeliveredAt   time.Time
	ErrorCategory string
}
