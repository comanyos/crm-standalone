package domain

// Messaging channels and the consent gate (task_crm.md §2,
// migrations/0028_messaging_consent.sql).
//
// This file exists to answer one question in one place: may we contact this
// person on this channel right now? Everything that reaches out to a human —
// the click-to-chat actions, message templates, workflow send actions —
// calls MayMessage and nothing else.
//
// One gate rather than a check at each call site, for the reason
// assertPolicyIsTotal exists next door in internal/server: a rule enforced in
// n places is a rule that is enforced in n-1 places as soon as somebody adds
// the n+1th. There are no send paths yet — the first arrives with
// task_crm.md §3 — and the totality check over them belongs with it, since a
// reflection test over an empty set asserts nothing. Until then the rule is
// held by this file being the only thing that reads the ledger at all.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Channel kinds. An address may be permitted on one and refused on another:
// "email me but do not text me" is an ordinary thing for a person to want,
// so consent is keyed on the pair, never on the person alone.
const (
	ChannelEmail    = "email"
	ChannelSMS      = "sms"
	ChannelWhatsApp = "whatsapp"
	ChannelVoice    = "voice"
)

// MessagingChannelKinds is the allowlist. A kind outside it is rejected at
// write time rather than stored and never resolved again — the same posture
// TaggableObjectTypes takes.
var MessagingChannelKinds = map[string]bool{
	ChannelEmail:    true,
	ChannelSMS:      true,
	ChannelWhatsApp: true,
	ChannelVoice:    true,
}

// SortedMessagingChannelKinds returns the allowlist, sorted, for an
// authoring UI to offer.
func SortedMessagingChannelKinds() []string {
	return []string{ChannelEmail, ChannelSMS, ChannelVoice, ChannelWhatsApp}
}

// Consent states. There is deliberately no "unknown" row: absence of a row
// *is* unknown, and writing one to say so would make the ledger's newest-row
// read ambiguous about whether anybody ever decided anything.
const (
	ConsentGranted   = "granted"
	ConsentWithdrawn = "withdrawn"
)

// Consent sources — a bounded vocabulary rather than free text, so the
// ledger stays queryable and no provider string can arrive in it.
const (
	ConsentSourceManual  = "manual"
	ConsentSourceImport  = "import"
	ConsentSourceCapture = "capture"
	ConsentSourceInbound = "inbound_keyword"
)

// ConsentSources is the allowlist for MessagingConsent.Source.
var ConsentSources = map[string]bool{
	ConsentSourceManual:  true,
	ConsentSourceImport:  true,
	ConsentSourceCapture: true,
	ConsentSourceInbound: true,
}

const (
	maxConsentAddressLen  = 320 // an email address's practical maximum
	maxConsentEvidenceLen = 200
)

// MessagingChannel is one reachable address on one channel.
//
// It is a value, not a stored row: ContactChannels derives a contact's
// channels from the Email and Phone it already carries. See
// migrations/0028_messaging_consent.sql for why there is no channel table.
type MessagingChannel struct {
	Kind string `json:"kind"`
	// Address is normalised — NormalizeChannelAddress has already run.
	Address string `json:"address"`
	// Reachable is false when the address exists but cannot actually be used
	// on this channel; Reason says why. A WhatsApp deep link needs a full
	// international number, and a local one is not one.
	Reachable bool   `json:"reachable"`
	Reason    string `json:"reason,omitempty"`
}

// NormalizeChannelAddress canonicalises an address so that two spellings of
// the same destination are one key in the consent ledger.
//
// It canonicalises; it does not validate. "+1 (555) 010-0000" and
// "+15550100000" must be the same address or a withdrawal recorded against
// one would not stop a message to the other — that is the whole job. Deciding
// whether a number is dialable is a different question, answered per channel
// by channelReachable, because the answer differs: WhatsApp needs a country
// code, a phone dialer does not.
func NormalizeChannelAddress(kind, raw string) (string, error) {
	if !MessagingChannelKinds[kind] {
		return "", fmt.Errorf("%w: %q is not a messaging channel (allowed: %s)",
			ErrInvalid, kind, strings.Join(SortedMessagingChannelKinds(), ", "))
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: a messaging address cannot be blank", ErrInvalid)
	}
	if len([]rune(trimmed)) > maxConsentAddressLen {
		return "", fmt.Errorf("%w: messaging address exceeds %d characters", ErrInvalid, maxConsentAddressLen)
	}

	if kind == ChannelEmail {
		// Lower-cased whole, including the local part. RFC 5321 permits a
		// case-sensitive local part, but no mail system anybody uses treats
		// one that way, and a consent ledger that distinguished
		// Bob@x.com from bob@x.com would let a capitalisation difference
		// resurrect permission somebody withdrew.
		return strings.ToLower(trimmed), nil
	}

	// Phone kinds: keep a leading '+' and the digits, drop the punctuation
	// people write numbers with.
	var b strings.Builder
	for i, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' || r == '/':
			// Punctuation people write numbers with. Dropped, not rejected.
		default:
			return "", fmt.Errorf("%w: %q is not a usable phone number", ErrInvalid, raw)
		}
	}
	normalized := b.String()
	if normalized == "" || normalized == "+" {
		return "", fmt.Errorf("%w: %q has no digits", ErrInvalid, raw)
	}
	return normalized, nil
}

// NormalizeE164Phone returns the canonical international representation used
// by lead identity, deduplication and every phone messaging channel. It is
// intentionally stricter than NormalizeChannelAddress: identity cannot store
// an ambiguous local number and guess a country later.
func NormalizeE164Phone(raw string) (string, error) {
	normalized, err := NormalizeChannelAddress(ChannelWhatsApp, raw)
	if err != nil {
		return "", err
	}
	if reachable, reason := channelReachable(ChannelWhatsApp, normalized); !reachable {
		return "", fmt.Errorf("must be E.164 international format: %s", reason)
	}
	if len(normalized) > 16 { // '+' plus E.164's maximum 15 digits.
		return "", fmt.Errorf("must contain at most 15 digits")
	}
	return normalized, nil
}

// channelReachable reports whether a normalised address can actually be used
// on its channel, and why not when it cannot.
//
// Every phone channel requires full international format, and that rule is
// load-bearing rather than fussy. It was added after a test asked whether
// consent withdrawn against "+15550100000" stops a message to "555 010 0000",
// and the answer was no: the two normalise to different strings, so they are
// different keys in the ledger, so the withdrawal does not apply. Editing a
// contact's phone from one format to the other would silently restore
// permission somebody had taken away.
//
// The tempting fix — infer the country code — is worse than the bug. Guessing
// +1 for a tenant whose customers are in the UK does not produce an
// unreachable number, it produces a *reachable different person's* number,
// and then applies one person's consent decisions to another. So the
// ambiguity is refused instead of resolved: a number we cannot key
// unambiguously is a number we do not offer to message, and the UI says which
// country code is missing.
//
// ponytail: requires a leading '+' because there is no per-tenant default
// region to resolve a local number against. Add that setting and resolve at
// write time — in ContactChannels and RecordConsent, so the ledger keeps
// storing one canonical form — and this restriction can lift.
func channelReachable(kind, normalized string) (bool, string) {
	switch kind {
	case ChannelEmail:
		at := strings.Index(normalized, "@")
		if at <= 0 || at == len(normalized)-1 || strings.Contains(normalized, " ") {
			return false, "not a usable email address"
		}
		return true, ""
	case ChannelSMS, ChannelVoice, ChannelWhatsApp:
		if !strings.HasPrefix(normalized, "+") {
			return false, "needs a country code (start the number with +)"
		}
		// A country code plus a subscriber number is never shorter than
		// this; the check catches an extension or a truncated paste.
		if len(normalized) < 8 {
			return false, "too short to be an international number"
		}
		return true, ""
	default:
		return false, "unknown channel"
	}
}

// ContactChannels derives the channels one Contact can be reached on.
//
// Derived rather than stored: a Contact carries one Email and one Phone, so
// the set is a pure function of the record. A channel whose underlying field
// is empty is absent from the result entirely — callers render what is here,
// so an unreachable-by-omission channel simply never offers a button.
func ContactChannels(c *Contact) []MessagingChannel {
	if c == nil {
		return nil
	}
	out := make([]MessagingChannel, 0, 4)
	add := func(kind, raw string) {
		if strings.TrimSpace(raw) == "" {
			return
		}
		normalized, err := NormalizeChannelAddress(kind, raw)
		if err != nil {
			// A stored value we cannot canonicalise is surfaced as an
			// unreachable channel rather than dropped: "why is there no
			// WhatsApp button" is a question the UI should be able to answer.
			out = append(out, MessagingChannel{Kind: kind, Address: strings.TrimSpace(raw), Reason: "not a usable address"})
			return
		}
		reachable, reason := channelReachable(kind, normalized)
		out = append(out, MessagingChannel{Kind: kind, Address: normalized, Reachable: reachable, Reason: reason})
	}
	add(ChannelEmail, c.Email)
	add(ChannelSMS, c.Phone)
	add(ChannelVoice, c.Phone)
	add(ChannelWhatsApp, c.Phone)
	return out
}

// LeadChannels mirrors ContactChannels for the person who has not converted
// yet. Keeping it server-side means consent and reachability have the same
// single source of truth before and after conversion.
func LeadChannels(lead *Lead) []MessagingChannel {
	if lead == nil {
		return nil
	}
	return ContactChannels(&Contact{Email: lead.ContactEmail, Phone: lead.Phone})
}

// MessagingConsent is one immutable decision about one address on one
// channel. Current state is the newest row for the pair; there is no update
// path, which is what makes the ledger answer "when did they opt out" rather
// than only "are they opted out".
type MessagingConsent struct {
	ID          string    `json:"id"`
	ChannelKind string    `json:"channel_kind"`
	Address     string    `json:"address"`
	State       string    `json:"state"`
	Source      string    `json:"source"`
	Evidence    string    `json:"evidence"`
	RecordedBy  string    `json:"recorded_by"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// EntityID returns the record's identifier.
func (e *MessagingConsent) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *MessagingConsent) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps RecordedAt on creation only. A consent row is written
// once and never touched again, so there is no "updated".
func (e *MessagingConsent) SetTimestamps(created, _ time.Time) {
	if e.RecordedAt.IsZero() && !created.IsZero() {
		e.RecordedAt = created
	}
}

// Created reports when the decision was recorded.
func (e *MessagingConsent) Created() time.Time { return e.RecordedAt }

// Validate rejects a consent row that cannot be stored. It assumes Address is
// already normalised — RecordConsent normalises before it validates, so a
// caller cannot store a row under a spelling the gate would never look up.
func (e *MessagingConsent) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no consent record", ErrInvalid)
	}
	if !MessagingChannelKinds[e.ChannelKind] {
		return fmt.Errorf("%w: %q is not a messaging channel (allowed: %s)",
			ErrInvalid, e.ChannelKind, strings.Join(SortedMessagingChannelKinds(), ", "))
	}
	if strings.TrimSpace(e.Address) == "" {
		return fmt.Errorf("%w: a consent record needs an address", ErrInvalid)
	}
	// A decision may only be recorded about an address we could actually
	// message. Without this, a withdrawal could be stored against a local
	// number — which MayMessage refuses to send to anyway — while the same
	// person's international number stays permitted, because the two are
	// different keys. Refusing at write time keeps every row in the ledger in
	// the one canonical form the gate looks up.
	if reachable, reason := channelReachable(e.ChannelKind, e.Address); !reachable {
		return fmt.Errorf("%w: cannot record consent for %q on %s: %s",
			ErrInvalid, e.Address, e.ChannelKind, reason)
	}
	if e.State != ConsentGranted && e.State != ConsentWithdrawn {
		return fmt.Errorf("%w: consent state must be %q or %q", ErrInvalid, ConsentGranted, ConsentWithdrawn)
	}
	if e.Source != "" && !ConsentSources[e.Source] {
		return fmt.Errorf("%w: %q is not a consent source", ErrInvalid, e.Source)
	}
	if len([]rune(e.Evidence)) > maxConsentEvidenceLen {
		return fmt.Errorf("%w: consent evidence exceeds %d characters", ErrInvalid, maxConsentEvidenceLen)
	}
	return nil
}

func cloneMessagingConsent(v *MessagingConsent) *MessagingConsent {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// The bounded refusal reasons MayMessage returns. Bounded because they reach
// a UI and an execution ledger, and a free-text reason built from a record
// would put customer data in both.
const (
	RefusalNone             = ""
	RefusalNoAddress        = "no_address"
	RefusalUnreachable      = "unreachable_address"
	RefusalConsentWithdrawn = "consent_withdrawn"
	RefusalSuppressed       = "address_suppressed"
)

// ConsentReader is what MayMessage needs from a store. It is an interface
// rather than *Store so the gate can be tested, and so a caller cannot pass
// something that reaches further than these two reads.
type ConsentReader interface {
	// CurrentConsent returns ErrNotFound when nobody has recorded a decision
	// for the pair. That is "unknown", not "refused" — see MayMessage.
	CurrentConsent(ctx context.Context, scope Scope, channelKind, address string) (*MessagingConsent, error)
	SuppressedSyncAddresses(ctx context.Context, scope Scope) (map[string]bool, error)
}

// MayMessage is the single gate every send path passes through.
//
// It answers for one channel and one address, and it fails closed: any error
// reaching it is a refusal, not a permission, because "we could not check"
// and "we may proceed" must never be the same outcome on a path that
// contacts a member of the public.
//
// Four things can refuse:
//
//  1. There is no address, or it is not usable on this channel.
//  2. The address is on the tenant's sync suppression list. That list already
//     means "this person asked not to be in our CRM" — reusing it here rather
//     than keeping a second do-not-contact list means a person has to say it
//     once, and there is no second list to forget to check.
//  3. The newest consent row for the pair says withdrawn.
//  4. (not yet) quiet hours. Deliberately absent: a human tapping "message
//     this person" should not be told to wait until 9am, and the automated
//     sends where quiet hours do apply do not exist yet. It arrives with
//     task_crm.md §6, which is where a send happens without a person present.
//
// Absence of a consent row is permission. That is the correct posture for a
// direct, one-to-one message to a business contact whose address they gave
// this tenant, and the wrong one for bulk — see the ponytail note.
//
// ponytail: unknown consent is treated as permitted, which suits 1:1 sending
// only. Bulk/campaign sending (task_crm.md §7) must require an explicit
// ConsentGranted row instead; give MayMessage a purpose argument then rather
// than a second gate.
func MayMessage(ctx context.Context, r ConsentReader, scope Scope, channel MessagingChannel) (bool, string, error) {
	if !scope.Valid() {
		return false, RefusalNoAddress, ErrNoTenant
	}
	if strings.TrimSpace(channel.Address) == "" {
		return false, RefusalNoAddress, nil
	}
	normalized, err := NormalizeChannelAddress(channel.Kind, channel.Address)
	if err != nil {
		return false, RefusalUnreachable, nil
	}
	if reachable, _ := channelReachable(channel.Kind, normalized); !reachable {
		return false, RefusalUnreachable, nil
	}

	suppressed, err := r.SuppressedSyncAddresses(ctx, scope)
	if err != nil {
		return false, RefusalSuppressed, err
	}
	// The suppression list is keyed on email addresses as sync stores them;
	// look the normalised form up directly, and lower-cased for the phone
	// kinds where normalisation does not lower-case.
	if suppressed[normalized] || suppressed[strings.ToLower(normalized)] {
		return false, RefusalSuppressed, nil
	}

	current, err := r.CurrentConsent(ctx, scope, channel.Kind, normalized)
	switch {
	case errors.Is(err, ErrNotFound):
		// Nobody has decided anything about this address. Unknown, which for
		// a direct one-to-one message is permission — see the ponytail note
		// above for why bulk must not reuse this branch.
	case err != nil:
		return false, RefusalConsentWithdrawn, err
	case current != nil && current.State == ConsentWithdrawn:
		return false, RefusalConsentWithdrawn, nil
	}
	return true, RefusalNone, nil
}
