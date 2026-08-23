package domain

import (
	"fmt"
	"strings"
	"time"
)

// Erasing synced communication content, and not collecting it again
// (migrations/0022_sync_erasure.sql).
//
// Sync puts a person's correspondence and meetings into this CRM. This file
// is how they come back out, and it has to answer two things that a plain
// DELETE does not.
//
// The first is that gmail_messages and calendar_events are the deduplication
// memory. Deleting them to erase somebody would make the next poll — a minute
// later — re-create every Activity it had just removed. So an erased row
// becomes a redacted tombstone: the provider's opaque id survives, because
// dedup needs it, and every human-readable field is blanked.
//
// The second is that a tombstone only covers what has already been seen.
// Without more, the next email that person sends is indexed as though nothing
// had been asked. SuppressSyncAddress is the "more": an address the matcher
// must never match again, checked before anything is written.

// ErasureResult reports what an erasure actually did.
//
// It is deliberately counts rather than a list. An erasure that returned the
// subject lines it had just erased would be an odd thing to build.
type ErasureResult struct {
	// ActivitiesDeleted counts timeline entries removed. Only entries sync
	// created are ever counted here — see EraseSyncedContent's contract.
	ActivitiesDeleted int `json:"activities_deleted"`
	// MessagesRedacted and EventsRedacted count index rows turned into
	// tombstones.
	MessagesRedacted int `json:"messages_redacted"`
	EventsRedacted   int `json:"events_redacted"`
	// AddressesSuppressed counts addresses added to the never-match list.
	AddressesSuppressed int `json:"addresses_suppressed"`
}

// Total reports whether the erasure touched anything at all, which is what a
// caller reporting "nothing to erase" needs.
func (r ErasureResult) Total() int {
	return r.ActivitiesDeleted + r.MessagesRedacted + r.EventsRedacted + r.AddressesSuppressed
}

// SyncSuppression is one address synchronisation must never match again.
type SyncSuppression struct {
	Address   string    `json:"address"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// maxSuppressionReasonLength bounds the operator note, mirroring the CHECK.
const maxSuppressionReasonLength = 200

// Validate normalizes and checks a suppression before it is stored.
func (s *SyncSuppression) Validate() error {
	s.Address = NormalizeEmail(s.Address)
	if s.Address == "" {
		return fmt.Errorf("%w: a sync suppression needs an address", ErrInvalid)
	}
	if !strings.Contains(s.Address, "@") {
		return fmt.Errorf("%w: %q is not an address sync could match", ErrInvalid, s.Address)
	}
	if len(s.Reason) > maxSuppressionReasonLength {
		return fmt.Errorf("%w: suppression reason exceeds %d characters",
			ErrInvalid, maxSuppressionReasonLength)
	}
	return nil
}

// The synced Activity kinds. Erasure removes only these.
//
// That boundary is the point rather than an optimization. A note somebody
// typed about a customer, or a call they logged by hand, is a record this
// organization authored — deleting it because a third party asked would
// destroy the organization's own business records under cover of a privacy
// request. What sync collected without anyone typing it is a different thing,
// and that is what comes out.
var syncedActivityKinds = []string{GmailActivityKind, CalendarActivityKind}

// SyncedActivityKinds returns the kinds erasure and retention operate on.
func SyncedActivityKinds() []string {
	return append([]string(nil), syncedActivityKinds...)
}

// IsSyncedActivityKind reports whether an Activity was created by sync rather
// than by a person.
func IsSyncedActivityKind(kind string) bool {
	for _, known := range syncedActivityKinds {
		if kind == known {
			return true
		}
	}
	return false
}

// EraseSubject names what an erasure applies to.
type EraseSubject struct {
	// SubjectType and SubjectID erase everything sync attached to one CRM
	// record. Empty means "not scoped to a record".
	SubjectType string
	SubjectID   string
	// OwnerID erases everything one connected mailbox or calendar produced.
	// Empty means "not scoped to a mailbox".
	OwnerID string
	// Before erases synced content older than this instant. Zero means no
	// age limit. This is what the retention sweep uses.
	Before time.Time
	// SuppressAddresses are added to the never-match list as part of the same
	// operation, so a request to be forgotten is not undone by the next poll.
	SuppressAddresses []string
	// Reason and Actor are recorded on the suppression and in the audit
	// event. An erasure that leaves no trace cannot be shown to have
	// happened, which is exactly what an auditor asks.
	Reason string
	Actor  string
}

// Validate rejects an erasure that names nothing.
//
// An unscoped erasure would delete every synced record in the tenant, and the
// difference between that and a caller forgetting to set a field is one
// missing line. So it is refused rather than interpreted.
func (e EraseSubject) Validate() error {
	scoped := (e.SubjectType != "" && e.SubjectID != "") || e.OwnerID != "" || !e.Before.IsZero()
	if !scoped {
		return fmt.Errorf(
			"%w: an erasure must name a record, a mailbox, or an age limit; "+
				"refusing to erase every synced record in the tenant", ErrInvalid)
	}
	if (e.SubjectType == "") != (e.SubjectID == "") {
		return fmt.Errorf("%w: an erasure naming a record needs both its type and its id", ErrInvalid)
	}
	if e.SubjectType != "" && !knownSubjectTypes[e.SubjectType] {
		return fmt.Errorf("%w: erasure subject_type %q is not a known subject type",
			ErrInvalid, e.SubjectType)
	}
	if len(e.Reason) > maxSuppressionReasonLength {
		return fmt.Errorf("%w: erasure reason exceeds %d characters",
			ErrInvalid, maxSuppressionReasonLength)
	}
	return nil
}

// NormalizedSuppressions returns the addresses to suppress, normalized and
// deduplicated, skipping anything that is not an address.
func (e EraseSubject) NormalizedSuppressions() []string {
	seen := make(map[string]bool, len(e.SuppressAddresses))
	out := make([]string, 0, len(e.SuppressAddresses))
	for _, raw := range e.SuppressAddresses {
		address := NormalizeEmail(raw)
		if address == "" || !strings.Contains(address, "@") || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	return out
}

// The erasure audit events.
const (
	// EntitySyncErasure identifies erasure envelopes on this module's bus
	// subject: crm.sync_erasure.<tenant>.
	EntitySyncErasure = "sync_erasure"
	// EventSyncContentErased fires whenever synced content is removed, by
	// request or by retention.
	EventSyncContentErased = EntitySyncErasure + ".content_erased"
)

// SyncErasurePayload is the audit body. It carries counts and the scope, and
// no content: an audit record of an erasure that quoted the erased subject
// lines would defeat the erasure.
type SyncErasurePayload struct {
	SubjectType         string `json:"subject_type,omitempty"`
	SubjectID           string `json:"subject_id,omitempty"`
	OwnerID             string `json:"owner_id,omitempty"`
	Before              string `json:"before,omitempty"`
	ActivitiesDeleted   int    `json:"activities_deleted"`
	MessagesRedacted    int    `json:"messages_redacted"`
	EventsRedacted      int    `json:"events_redacted"`
	AddressesSuppressed int    `json:"addresses_suppressed"`
	Reason              string `json:"reason,omitempty"`
	// Automatic distinguishes the retention sweep from somebody asking. An
	// auditor needs to tell "we deleted this because we were asked" from "we
	// deleted this because it aged out".
	Automatic bool `json:"automatic"`
}

func cloneSyncSuppression(s *SyncSuppression) *SyncSuppression {
	if s == nil {
		return nil
	}
	out := *s
	return &out
}
