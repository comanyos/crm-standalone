package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Lead lifecycle state machine (B1 — docs/customer-os/PLAN.md):
//
//	new -> working -> qualified -> converted / disqualified
//
// Read literally, the same way B2 reads the Opportunity pipeline chain
// (transitions.go): each open status may move to exactly the next status the
// chain names for it, and qualified is the one branch point, into either
// converted or disqualified. Skipping statuses (new -> qualified) and moving
// backward (qualified -> working) are both illegal transitions.
//
// converted and disqualified are both terminal: nothing may transition out
// of either.
//
// UpdateLead enforces this table but additionally refuses to set Status to
// converted directly — that transition only happens as a side effect of
// ConvertLead (lead_conversion.go), which performs the one-transaction
// conversion this state carries.

// Lead.Status values.
const (
	LeadStatusNew          = "new"
	LeadStatusWorking      = "working"
	LeadStatusQualified    = "qualified"
	LeadStatusConverted    = "converted"
	LeadStatusDisqualified = "disqualified"
)

// leadStatusEdges lists, for each status, the statuses it may move to. A
// status with an empty set is terminal. Single source of truth both MemStore
// and PgStore call through, so the two implementations cannot drift on the
// rule — only on the plumbing that reaches the row.
var leadStatusEdges = map[string]map[string]bool{
	LeadStatusNew:          {LeadStatusWorking: true},
	LeadStatusWorking:      {LeadStatusQualified: true},
	LeadStatusQualified:    {LeadStatusConverted: true, LeadStatusDisqualified: true},
	LeadStatusConverted:    {},
	LeadStatusDisqualified: {},
}

// leadStatusIsTerminal reports whether a status is one of the two terminal
// statuses.
func leadStatusIsTerminal(status string) bool {
	return status == LeadStatusConverted || status == LeadStatusDisqualified
}

// applyLeadStatusDefault fills the status of a newly created Lead. Default
// status on Create is new.
func applyLeadStatusDefault(v *Lead) {
	if v.Status == "" {
		v.Status = LeadStatusNew
	}
}

// checkLeadStatusKnown rejects a status value the machine has never heard
// of — distinct from a known-but-illegal transition, which
// checkLeadStatusTransition below handles.
func checkLeadStatusKnown(status string) error {
	if _, ok := leadStatusEdges[status]; !ok {
		return fmt.Errorf("%w: %q is not a known Lead status", ErrInvalidTransition, status)
	}
	return nil
}

// checkLeadStatusTransition validates a status change. An unchanged status
// is always allowed, so an update that only edits other fields (score,
// owner_id, ...) is never blocked by this check. A terminal status stays
// terminal: nothing may move out of it, including between the two terminal
// statuses.
func checkLeadStatusTransition(from, to string) error {
	if from == to {
		return nil
	}
	if leadStatusIsTerminal(from) {
		return fmt.Errorf(
			"%w: Lead is %q, a terminal status; it cannot move to %q",
			ErrInvalidTransition, from, to)
	}
	edges, ok := leadStatusEdges[from]
	if !ok {
		return fmt.Errorf("%w: %q is not a known Lead status", ErrInvalidTransition, from)
	}
	if _, ok := leadStatusEdges[to]; !ok {
		return fmt.Errorf("%w: %q is not a known Lead status", ErrInvalidTransition, to)
	}
	if !edges[to] {
		return fmt.Errorf("%w: Lead cannot move from %q to %q", ErrInvalidTransition, from, to)
	}
	return nil
}

// --- Exact-key dedup --------------------------------------------------
//
// A lead whose contact_email already belongs to an existing Contact in this
// tenant is flagged (Lead.DuplicateOfContactID), not silently duplicated and
// not blocked: the record is still written normally.

// contactByEmailFinder is what applyLeadDuplicateFlag needs from a store.
type contactByEmailFinder interface {
	FindContactByEmail(ctx context.Context, scope Scope, email string) (*Contact, error)
}

type contactIdentityFinder interface {
	contactByEmailFinder
	FindContactByPhone(ctx context.Context, scope Scope, phone string) (*Contact, error)
}

// applyLeadDuplicateFlag recomputes v.DuplicateOfContactID from the current
// state of the tenant's contacts. It is called on every Create and Update so
// the flag never goes stale: an email that stops matching (the contact was
// deleted) clears the flag, and one that starts matching sets it.
func applyLeadDuplicateFlag(ctx context.Context, r contactIdentityFinder, scope Scope, v *Lead) error {
	v.DuplicateOfContactID = ""
	email := strings.TrimSpace(v.ContactEmail)
	if email != "" {
		c, err := r.FindContactByEmail(ctx, scope, email)
		if errors.Is(err, ErrNotFound) {
			// Fall through to the phone key when one exists.
		} else if err != nil {
			return err
		} else {
			v.DuplicateOfContactID = c.ID
			return nil
		}
	}
	phone := strings.TrimSpace(v.Phone)
	if phone == "" {
		return nil
	}
	c, err := r.FindContactByPhone(ctx, scope, phone)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	v.DuplicateOfContactID = c.ID
	return nil
}

// normalizeLeadIdentity stores one spelling for every external identity key.
// It is called before validation on every create/update and by capture.
func normalizeLeadIdentity(v *Lead) error {
	if v == nil {
		return ErrInvalid
	}
	v.FirstName = strings.TrimSpace(v.FirstName)
	v.LastName = strings.TrimSpace(v.LastName)
	v.Company = strings.TrimSpace(v.Company)
	v.ContactEmail = strings.ToLower(strings.TrimSpace(v.ContactEmail))
	if strings.TrimSpace(v.Phone) != "" {
		phone, err := NormalizeE164Phone(v.Phone)
		if err != nil {
			return fmt.Errorf("%w: Lead.phone %v", ErrInvalid, err)
		}
		v.Phone = phone
	}
	return nil
}

// --- Shared lookup helpers, used by both MemStore and PgStore ------------
//
// Both stores expose FindContactByEmail/FindAccountByDomain (store.go), but
// implement the actual matching differently (MemStore scans its tables,
// PgStore queries Postgres). The comparison rules themselves — what counts
// as "the same email" and "the same domain" — live here once so the two
// backends cannot silently disagree on what a match is.

// sameEmail is the exact-key comparison this module uses for email dedup:
// trimmed, case-insensitive. Not fuzzy matching (Track D, deferred) — two
// strings are either the same key or they are not.
func sameEmail(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// emailDomain extracts the domain half of an email address, lowercased. An
// address with no "@" (or nothing after it) has no domain.
func emailDomain(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[at+1:]))
}

// websiteDomain normalizes an Account.Website value down to a bare domain,
// so "https://www.acme.com/pricing", "www.acme.com" and "acme.com" all
// compare equal to the email domain "acme.com".
func websiteDomain(website string) string {
	w := strings.ToLower(strings.TrimSpace(website))
	if w == "" {
		return ""
	}
	w = strings.TrimPrefix(w, "https://")
	w = strings.TrimPrefix(w, "http://")
	w = strings.TrimPrefix(w, "www.")
	if idx := strings.IndexAny(w, "/?#"); idx >= 0 {
		w = w[:idx]
	}
	return strings.TrimSpace(w)
}

// sameDomain reports whether two domain-ish strings name the same domain
// once normalized.
func sameDomain(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}
