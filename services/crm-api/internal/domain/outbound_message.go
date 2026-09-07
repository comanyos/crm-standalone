package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Messages an automation has drafted (task_crm.md §6,
// migrations/0032_outbound_messages.sql).
//
// A draft, never a send. Nothing here delivers anything — provider-sent
// messaging is task_crm.md §4 and waits on a provider contract. What a rule
// can do without one is the part that actually needs judgement: decide who to
// contact, check that contacting them is permitted, and write the wording.
// A rep then sends it in one tap through the click-to-chat links (§3).
//
// Rendering happens here, on the server, with nobody present — which is the
// case docs/WORKFLOW_AUTOMATION.md refuses templated action text for: "a
// template that can read the record can read fields the rule author is not
// allowed to see." That refusal is about *arbitrary* interpolation and still
// stands. It does not reach this vocabulary, for two reasons that have to
// both hold:
//
//   - MergeFields is a fixed allowlist of fixed-schema fields. Field-level
//     visibility in this codebase (internal/api/field_security.go) applies to
//     custom fields only; nothing in the allowlist can be hidden from a role
//     that can read the record at all.
//   - Scopes are type-level, so a rule author holding crm:contact:read can
//     already read every contact the rule could fire on. There is no record
//     the engine can see that the author cannot.
//
// The second reason is the one that would expire. If record-level
// authorization ever lands (task.md §1.4), a rule could fire on a record its
// author may not read, and this needs revisiting — hence saying so here
// rather than in a commit message.

// Outbound message states.
const (
	OutboundPending   = "pending"
	OutboundSent      = "sent"
	OutboundDiscarded = "discarded"
)

// The bounded reasons a draft can be discarded. Bounded because they reach a
// UI and could otherwise carry a fragment of the message.
const (
	DiscardReasonManual         = "manual"
	DiscardReasonConsentChanged = "consent_changed"
)

// outboundSubjects are the record types a draft can be about. Both carry an
// address of their own; an Account or an Opportunity does not, and picking
// "a contact on the account" is a choice nobody has made.
var outboundSubjects = map[string]bool{
	SubjectLead:    true,
	SubjectContact: true,
}

const maxOutboundBodyLen = 4000

// OutboundMessage is one drafted message.
type OutboundMessage struct {
	ID            string    `json:"id"`
	SubjectType   string    `json:"subject_type"`
	SubjectID     string    `json:"subject_id"`
	ChannelKind   string    `json:"channel_kind"`
	Address       string    `json:"address"`
	Subject       string    `json:"subject"`
	Body          string    `json:"body"`
	Status        string    `json:"status"`
	AssigneeID    string    `json:"assignee_id"`
	RuleVersionID string    `json:"rule_version_id"`
	ExecutionID   string    `json:"execution_id"`
	CreatedAt     time.Time `json:"created_at"`
	SentAt        time.Time `json:"sent_at"`
	SentBy        string    `json:"sent_by"`
	DiscardReason string    `json:"discard_reason"`
}

// EntityID returns the record's identifier.
func (e *OutboundMessage) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *OutboundMessage) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps CreatedAt on creation. A draft's later transitions
// stamp their own columns.
func (e *OutboundMessage) SetTimestamps(created, _ time.Time) {
	if e.CreatedAt.IsZero() && !created.IsZero() {
		e.CreatedAt = created
	}
}

// Created reports when the draft was written.
func (e *OutboundMessage) Created() time.Time { return e.CreatedAt }

// Validate rejects a draft that cannot be stored.
func (e *OutboundMessage) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no message", ErrInvalid)
	}
	if !outboundSubjects[e.SubjectType] {
		return fmt.Errorf("%w: %q cannot be messaged directly (allowed: %s, %s)",
			ErrInvalid, e.SubjectType, SubjectContact, SubjectLead)
	}
	if strings.TrimSpace(e.SubjectID) == "" {
		return fmt.Errorf("%w: a message needs a record", ErrInvalid)
	}
	if !TemplateChannelKinds[e.ChannelKind] {
		return fmt.Errorf("%w: %q is not a channel a message can be drafted for (allowed: %s)",
			ErrInvalid, e.ChannelKind, strings.Join(SortedTemplateChannelKinds(), ", "))
	}
	if strings.TrimSpace(e.Address) == "" {
		return fmt.Errorf("%w: a message needs an address", ErrInvalid)
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Errorf("%w: a message needs a body", ErrInvalid)
	}
	if len([]rune(e.Body)) > maxOutboundBodyLen {
		return fmt.Errorf("%w: message body exceeds %d characters", ErrInvalid, maxOutboundBodyLen)
	}
	switch e.Status {
	case OutboundPending, OutboundSent, OutboundDiscarded:
	default:
		return fmt.Errorf("%w: %q is not a message status", ErrInvalid, e.Status)
	}
	// The database enforces this too. Here as well because a draft marked
	// sent with nobody attached leaves "who contacted this customer"
	// unanswerable, and the store is not the only writer.
	if e.Status == OutboundSent && strings.TrimSpace(e.SentBy) == "" {
		return fmt.Errorf("%w: a sent message must record who sent it", ErrInvalid)
	}
	return nil
}

func cloneOutboundMessage(v *OutboundMessage) *OutboundMessage {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// mergePlaceholderPattern matches "{{ field.name }}", the same shape
// message_template.go validates and the browser expands.
var mergePlaceholderPattern = regexp.MustCompile(`\{\{\s*([a-z_.]+)\s*\}\}`)

// RenderMergeFields substitutes an allowlisted placeholder vocabulary.
//
// It is the server-side twin of apps/web/src/lib/messageTemplates.ts, and the
// two have to agree: a rep previewing a template in the browser and an
// automation drafting from the same template must produce the same words, or
// the preview is a lie. Both are pinned by tests using the same fixtures.
//
// A field with no value renders empty and the surrounding punctuation is
// tidied, so a contact with no first name gets "Hi," rather than "Hi ,".
func RenderMergeFields(body string, values map[string]string) string {
	substituted := mergePlaceholderPattern.ReplaceAllStringFunc(body, func(match string) string {
		field := mergePlaceholderPattern.FindStringSubmatch(match)[1]
		return values[field]
	})
	return tidyRenderedText(substituted)
}

// tidyRenderedText removes the artefacts an empty substitution leaves.
//
// Deliberately narrow — a dangling space before punctuation, a doubled space,
// a leading orphan comma — rather than a general prose fixer. Guessing at
// somebody's wording is how a template stops saying what its author wrote.
func tidyRenderedText(text string) string {
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = spaceBeforePunctuation.ReplaceAllString(line, "$1")
		line = strings.TrimLeft(line, " \t")
		line = leadingPunctuation.ReplaceAllString(line, "")
		line = repeatedSpace.ReplaceAllString(line, " ")
		out.WriteString(strings.TrimRight(line, " \t"))
		out.WriteString("\n")
	}
	return strings.Trim(out.String(), "\n")
}

var (
	spaceBeforePunctuation = regexp.MustCompile(` +([,.!?])`)
	leadingPunctuation     = regexp.MustCompile(`^[,.!?]\s*`)
	repeatedSpace          = regexp.MustCompile(`  +`)
)

// MergeValuesForLead builds the substitution values for a Lead.
//
// A Lead has no name fields, so the name placeholders resolve to nothing and
// the tidy above turns "Hi {{contact.first_name}}," into "Hi,". That is the
// honest outcome: inventing a name from an email local part would put a guess
// in a message to a customer.
func MergeValuesForLead(lead *Lead, senderEmail string) map[string]string {
	if lead == nil {
		return map[string]string{}
	}
	return map[string]string{
		MergeContactFirstName: "",
		MergeContactLastName:  "",
		MergeContactFullName:  "",
		MergeContactEmail:     strings.TrimSpace(lead.ContactEmail),
		MergeContactCompany:   strings.TrimSpace(lead.Company),
		MergeSenderEmail:      strings.TrimSpace(senderEmail),
	}
}

// MergeValuesForContact builds the substitution values for a Contact.
func MergeValuesForContact(contact *Contact, accountName, senderEmail string) map[string]string {
	if contact == nil {
		return map[string]string{}
	}
	first := strings.TrimSpace(contact.FirstName)
	last := strings.TrimSpace(contact.LastName)
	return map[string]string{
		MergeContactFirstName: first,
		MergeContactLastName:  last,
		MergeContactFullName:  strings.TrimSpace(first + " " + last),
		MergeContactEmail:     strings.TrimSpace(contact.Email),
		MergeContactCompany:   strings.TrimSpace(accountName),
		MergeSenderEmail:      strings.TrimSpace(senderEmail),
	}
}
