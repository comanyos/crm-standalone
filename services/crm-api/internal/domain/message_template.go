package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Message templates (task_crm.md §5, migrations/0030_message_templates.sql).
//
// A template is wording plus an allowlist of placeholders. The allowlist is
// the whole design, and it is what lets v1 have merge fields at all.
//
// docs/WORKFLOW_AUTOMATION.md refuses templated action text with a reason
// that is correct and still applies: "a template that can read the record can
// read fields the rule author is not allowed to see." Two things make this
// feature different rather than an exception to that.
//
// First, the vocabulary is a fixed list of fields every role that can read the
// record can already read — a contact's own name, their account's name. There
// is no field here that a reader could not see by looking at the page.
//
// Second, nothing expands a template on the server. The browser substitutes,
// from the record it is already displaying, for a person who is already
// looking at it. A user cannot leak to themselves what is on their screen.
//
// Server-side rendering — for an automated send, with nobody present — is a
// different feature, and it inherits the blocker unchanged.

// Merge placeholders, as they appear in a template body.
const (
	MergeContactFirstName = "contact.first_name"
	MergeContactLastName  = "contact.last_name"
	MergeContactFullName  = "contact.full_name"
	MergeContactEmail     = "contact.email"
	MergeContactCompany   = "account.name"
	MergeSenderEmail      = "sender.email"
)

// MergeFields is the allowlist. A body naming anything outside it is refused
// at save time rather than rendering as a literal "{{whatever}}" in a message
// to a customer — which is the failure the author would only hear about from
// the recipient.
var MergeFields = map[string]bool{
	MergeContactFirstName: true,
	MergeContactLastName:  true,
	MergeContactFullName:  true,
	MergeContactEmail:     true,
	MergeContactCompany:   true,
	MergeSenderEmail:      true,
}

// SortedMergeFields returns the allowlist, sorted, for the editor to offer.
func SortedMergeFields() []string {
	out := make([]string, 0, len(MergeFields))
	for field := range MergeFields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// TemplateChannelKinds are the channels a template may be written for.
//
// Voice is absent: a dialer has nowhere to put a message, so a template for
// it would be text nobody could send.
var TemplateChannelKinds = map[string]bool{
	ChannelEmail:    true,
	ChannelSMS:      true,
	ChannelWhatsApp: true,
}

// SortedTemplateChannelKinds returns those channels, sorted.
func SortedTemplateChannelKinds() []string {
	return []string{ChannelEmail, ChannelSMS, ChannelWhatsApp}
}

const (
	maxTemplateNameLen    = 100
	maxTemplateSubjectLen = 200
	maxTemplateBodyLen    = 2000
)

// mergePlaceholder matches "{{ field.name }}" with optional inner spacing, so
// an author's stray space does not turn a valid field into an unknown one.
var mergePlaceholder = regexp.MustCompile(`\{\{\s*([a-z_.]+)\s*\}\}`)

// MessageTemplate is one piece of reusable wording.
type MessageTemplate struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ChannelKind string    `json:"channel_kind"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// EntityID returns the record's identifier.
func (e *MessageTemplate) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *MessageTemplate) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *MessageTemplate) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Created reports when the template was written.
func (e *MessageTemplate) Created() time.Time { return e.CreatedAt }

// Validate rejects a template that cannot be stored or safely rendered.
func (e *MessageTemplate) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no template", ErrInvalid)
	}
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: a template needs a name", ErrInvalid)
	}
	if len([]rune(e.Name)) > maxTemplateNameLen {
		return fmt.Errorf("%w: template name exceeds %d characters", ErrInvalid, maxTemplateNameLen)
	}
	if !TemplateChannelKinds[e.ChannelKind] {
		return fmt.Errorf("%w: %q is not a channel a template can be written for (allowed: %s)",
			ErrInvalid, e.ChannelKind, strings.Join(SortedTemplateChannelKinds(), ", "))
	}
	if e.ChannelKind != ChannelEmail && strings.TrimSpace(e.Subject) != "" {
		return fmt.Errorf("%w: only an email template has a subject", ErrInvalid)
	}
	if len([]rune(e.Subject)) > maxTemplateSubjectLen {
		return fmt.Errorf("%w: template subject exceeds %d characters", ErrInvalid, maxTemplateSubjectLen)
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Errorf("%w: a template needs a body", ErrInvalid)
	}
	if len([]rune(e.Body)) > maxTemplateBodyLen {
		return fmt.Errorf("%w: template body exceeds %d characters", ErrInvalid, maxTemplateBodyLen)
	}
	return ValidateMergeFields(e.Subject + "\n" + e.Body)
}

// ValidateMergeFields refuses any placeholder outside the allowlist.
//
// Refusing at save time rather than rendering the unknown name literally is
// the point: a template that ships "{{contact.phone}}" to a customer verbatim
// is a mistake the author hears about from the recipient, if at all.
func ValidateMergeFields(text string) error {
	for _, match := range mergePlaceholder.FindAllStringSubmatch(text, -1) {
		if !MergeFields[match[1]] {
			return fmt.Errorf("%w: %q is not a field a template may use (allowed: %s)",
				ErrInvalid, match[1], strings.Join(SortedMergeFields(), ", "))
		}
	}
	// A lone "{{" that never closes is almost certainly a typo in a
	// placeholder, and it would reach a customer as literal braces.
	if strings.Count(text, "{{") != len(mergePlaceholder.FindAllString(text, -1)) {
		return fmt.Errorf("%w: an unclosed or malformed {{placeholder}} — "+
			"use one of: %s", ErrInvalid, strings.Join(SortedMergeFields(), ", "))
	}
	return nil
}

func cloneMessageTemplate(v *MessageTemplate) *MessageTemplate {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func templateNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
