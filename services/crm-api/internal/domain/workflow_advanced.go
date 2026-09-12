package domain

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	ScheduleActive    = "active"
	SchedulePaused    = "paused"
	ScheduleCompleted = "completed"
	ScheduleOnce      = "once"
	ScheduleDaily     = "daily"
	ScheduleWeekly    = "weekly"
	ScheduleMonthly   = "monthly"

	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// WorkflowSchedule is a durable, tenant-scoped clock for an active rule.
// NextRunAt is advanced only after an occurrence has been claimed into the
// ordinary execution ledger, making a crash replay harmless.
type WorkflowSchedule struct {
	ID          string
	RuleID      string
	SubjectType string
	SubjectID   string
	TimeZone    string
	Recurrence  string
	Interval    int
	Status      string
	NextRunAt   time.Time
	EndsAt      time.Time
	LastRunAt   time.Time
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s *WorkflowSchedule) Validate() error {
	if strings.TrimSpace(s.RuleID) == "" || strings.TrimSpace(s.SubjectID) == "" || strings.TrimSpace(s.CreatedBy) == "" {
		return fmt.Errorf("%w: schedule rule, subject and creator are required", ErrInvalid)
	}
	if !knownSubjectTypes[s.SubjectType] {
		return fmt.Errorf("%w: unknown scheduled subject type", ErrInvalid)
	}
	if s.NextRunAt.IsZero() {
		return fmt.Errorf("%w: schedule start is required", ErrInvalid)
	}
	if s.TimeZone == "" {
		s.TimeZone = "UTC"
	}
	if _, err := time.LoadLocation(s.TimeZone); err != nil {
		return fmt.Errorf("%w: invalid schedule time zone", ErrInvalid)
	}
	if s.Interval == 0 {
		s.Interval = 1
	}
	if s.Interval < 1 || s.Interval > 365 {
		return fmt.Errorf("%w: schedule interval must be between 1 and 365", ErrInvalid)
	}
	switch s.Recurrence {
	case ScheduleOnce, ScheduleDaily, ScheduleWeekly, ScheduleMonthly:
	default:
		return fmt.Errorf("%w: unknown schedule recurrence", ErrInvalid)
	}
	if s.Status == "" {
		s.Status = ScheduleActive
	}
	if s.Status != ScheduleActive && s.Status != SchedulePaused && s.Status != ScheduleCompleted {
		return fmt.Errorf("%w: unknown schedule status", ErrInvalid)
	}
	if !s.EndsAt.IsZero() && s.EndsAt.Before(s.NextRunAt) {
		return fmt.Errorf("%w: schedule end precedes its next run", ErrInvalid)
	}
	return nil
}

// NextScheduleOccurrence advances in local wall-clock time, preserving a
// 09:00 meeting through daylight-saving transitions.
func NextScheduleOccurrence(current time.Time, zone, recurrence string, interval int) (time.Time, bool) {
	if recurrence == ScheduleOnce {
		return time.Time{}, false
	}
	if interval < 1 {
		interval = 1
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.UTC
	}
	local := current.In(location)
	var next time.Time
	switch recurrence {
	case ScheduleDaily:
		next = local.AddDate(0, 0, interval)
	case ScheduleWeekly:
		next = local.AddDate(0, 0, 7*interval)
	case ScheduleMonthly:
		// time.AddDate normalizes 31 January + one month into March. A
		// monthly sequence instead means the same day where possible and the
		// last day of a shorter month where it is not.
		first := time.Date(local.Year(), local.Month()+time.Month(interval), 1,
			local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), location)
		lastDay := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, location).Day()
		day := local.Day()
		if day > lastDay {
			day = lastDay
		}
		next = time.Date(first.Year(), first.Month(), day, local.Hour(), local.Minute(),
			local.Second(), local.Nanosecond(), location)
	default:
		return time.Time{}, false
	}
	return next.UTC(), true
}

// WorkflowApproval pauses an execution at one action until the named CRM
// principal decides it. One row per execution/action is the idempotency key.
type WorkflowApproval struct {
	ID           string
	ExecutionID  string
	RuleID       string
	ActionIndex  int
	ApproverID   string
	Title        string
	Body         string
	SubjectType  string
	SubjectID    string
	Status       string
	DecisionNote string
	DecisionBy   string
	RequestedAt  time.Time
	DecidedAt    time.Time
}

func (a *WorkflowApproval) Validate() error {
	if strings.TrimSpace(a.ExecutionID) == "" || strings.TrimSpace(a.RuleID) == "" ||
		strings.TrimSpace(a.ApproverID) == "" || strings.TrimSpace(a.Title) == "" || a.ActionIndex < 0 {
		return fmt.Errorf("%w: approval execution, rule, approver and title are required", ErrInvalid)
	}
	if !knownSubjectTypes[a.SubjectType] || strings.TrimSpace(a.SubjectID) == "" {
		return fmt.Errorf("%w: approval needs a valid subject", ErrInvalid)
	}
	if a.Status == "" {
		a.Status = ApprovalPending
	}
	if a.Status != ApprovalPending && a.Status != ApprovalApproved && a.Status != ApprovalRejected {
		return fmt.Errorf("%w: unknown approval status", ErrInvalid)
	}
	if len([]rune(a.Title)) > maxWorkflowTextLength || len([]rune(a.Body)) > maxWorkflowTextLength || len([]rune(a.DecisionNote)) > maxWorkflowTextLength {
		return fmt.Errorf("%w: approval text is too long", ErrInvalid)
	}
	return nil
}

// WorkflowWebhookEndpoint is a named HTTPS destination. SigningSecret is
// always ciphertext at the domain/store boundary.
type WorkflowWebhookEndpoint struct {
	ID                      string
	Name                    string
	URL                     string
	SigningSecretCiphertext []byte
	CreatedBy               string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (e *WorkflowWebhookEndpoint) Validate() error {
	e.Name, e.URL = strings.TrimSpace(e.Name), strings.TrimSpace(e.URL)
	if e.Name == "" || len([]rune(e.Name)) > 200 || strings.TrimSpace(e.CreatedBy) == "" {
		return fmt.Errorf("%w: webhook name and creator are required", ErrInvalid)
	}
	parsed, err := url.Parse(e.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%w: webhook URL must be an absolute HTTPS URL without credentials", ErrInvalid)
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return fmt.Errorf("%w: webhook URL may use only HTTPS port 443", ErrInvalid)
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && UnsafeWebhookIP(ip) {
		return fmt.Errorf("%w: webhook URL may not target a private or local address", ErrInvalid)
	}
	if len(e.SigningSecretCiphertext) == 0 {
		return fmt.Errorf("%w: webhook signing secret is required", ErrInvalid)
	}
	return nil
}

// UnsafeWebhookIP is exported for the egress dialer, which must repeat the
// check after DNS resolution to prevent DNS-rebinding SSRF.
func UnsafeWebhookIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	for _, prefix := range unsafeWebhookPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return !address.IsGlobalUnicast()
}

var unsafeWebhookPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"), netip.MustParsePrefix("2001:db8::/32"),
}

type WorkflowWebhookDelivery struct {
	ID           string
	EndpointID   string
	ExecutionID  string
	ActionIndex  int
	Status       string
	ResponseCode int
	AttemptedAt  time.Time
}

func cloneWorkflowSchedule(v *WorkflowSchedule) *WorkflowSchedule {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func cloneWorkflowApproval(v *WorkflowApproval) *WorkflowApproval {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func cloneWorkflowWebhookEndpoint(v *WorkflowWebhookEndpoint) *WorkflowWebhookEndpoint {
	if v == nil {
		return nil
	}
	out := *v
	out.SigningSecretCiphertext = append([]byte(nil), v.SigningSecretCiphertext...)
	return &out
}
