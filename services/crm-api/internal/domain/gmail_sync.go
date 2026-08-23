package domain

import (
	"fmt"
	"strings"
	"time"
)

// Gmail synchronisation state and the message index
// (migrations/0020_gmail_sync.sql, docs/GOOGLE_INTEGRATION.md).
//
// The rule this file exists to enforce, and which the types are shaped
// around: a mailbox is not a CRM. Most of what a person receives has nothing
// to do with this organization's records, and everything written here becomes
// readable by every colleague holding crm:activity:read. So an indexed
// message is always a message that matched a Contact in the tenant, and what
// is kept of it is an envelope — participants, subject line, direction, time.
// There is no body, and no snippet, anywhere in these types.

// The direction of a synced message, relative to the connected mailbox.
const (
	// GmailInbound means the connected account received it.
	GmailInbound = "inbound"
	// GmailOutbound means the connected account sent it.
	GmailOutbound = "outbound"
)

// GmailActivityKind is the Activity.Kind synced email is logged under. It is
// distinct from a hand-logged "email" activity on purpose: a timeline that
// cannot tell "someone recorded that they emailed the customer" from "the
// system observed an email" cannot be audited, and only one of the two is
// evidence.
const GmailActivityKind = "email_synced"

// GmailMailboxRef names one mailbox across tenants. It is what
// DueGmailMailboxes returns: identifiers only, never state.
type GmailMailboxRef struct {
	TenantID string `json:"tenant_id"`
	OwnerID  string `json:"owner_id"`
}

// GmailSyncState is one connected mailbox's cursor.
type GmailSyncState struct {
	OwnerID string `json:"owner_id"`
	// LastHistoryID is Gmail's own incremental cursor. Empty means no
	// incremental position is known and the next pass must backfill.
	LastHistoryID string `json:"last_history_id"`
	// BackfillPageToken resumes an interrupted first pass.
	BackfillPageToken string `json:"backfill_page_token"`
	BackfillComplete  bool   `json:"backfill_complete"`
	// SyncingUntil is a lease rather than a lock: a replica that dies
	// mid-pass must not make a mailbox permanently unsyncable, so the claim
	// expires instead of needing release.
	SyncingUntil      time.Time `json:"syncing_until"`
	LastSyncedAt      time.Time `json:"last_synced_at"`
	LastMessageAt     time.Time `json:"last_message_at"`
	MessagesIndexed   int64     `json:"messages_indexed"`
	ActivitiesCreated int64     `json:"activities_created"`
	// LastError is a bounded reason code, never a provider response body.
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate enforces what the database also enforces.
func (s *GmailSyncState) Validate() error {
	if strings.TrimSpace(s.OwnerID) == "" {
		return fmt.Errorf("%w: gmail sync state has no owner", ErrInvalid)
	}
	if s.MessagesIndexed < 0 || s.ActivitiesCreated < 0 {
		return fmt.Errorf("%w: gmail sync counters cannot be negative", ErrInvalid)
	}
	if len(s.LastError) > maxGoogleErrorLength {
		return fmt.Errorf("%w: gmail sync error reason exceeds %d characters",
			ErrInvalid, maxGoogleErrorLength)
	}
	return nil
}

// NeedsBackfill reports whether the next pass must walk the mailbox rather
// than ask Gmail what changed.
//
// Both conditions matter. A mailbox that has never synced has no cursor; a
// mailbox whose cursor Gmail has since expired — history older than roughly a
// week is discarded — has one that no longer resolves. Treating the second as
// an incremental sync silently loses every message in the gap.
func (s *GmailSyncState) NeedsBackfill() bool {
	return s == nil || !s.BackfillComplete || s.LastHistoryID == ""
}

// GmailMessage is one timeline entry produced by sync: one message, one
// matched CRM record.
//
// An email to three Contacts belongs on three timelines, so it produces three
// of these. Keying on the subject as well as the message is what makes each
// entry idempotent on its own — re-syncing cannot double-post, and a Contact
// created after the message arrived still gets its entry on a later pass.
type GmailMessage struct {
	OwnerID           string `json:"owner_id"`
	ProviderMessageID string `json:"provider_message_id"`
	SubjectType       string `json:"subject_type"`
	SubjectID         string `json:"subject_id"`
	ThreadID          string `json:"thread_id"`
	ActivityID        string `json:"activity_id"`
	// MessageSubject is the Subject header, the only part of a message kept
	// verbatim: a timeline entry that cannot say what the email was about is
	// not worth having.
	MessageSubject string    `json:"message_subject"`
	FromAddress    string    `json:"from_address"`
	Direction      string    `json:"direction"`
	OccurredAt     time.Time `json:"occurred_at"`
	CreatedAt      time.Time `json:"created_at"`
	// ErasedAt marks a redacted tombstone: the provider id survives so
	// deduplication still works, and every human-readable field is blank.
	ErasedAt time.Time `json:"erased_at"`
}

// Validate enforces what the database also enforces.
func (m *GmailMessage) Validate() error {
	switch {
	case strings.TrimSpace(m.OwnerID) == "":
		return fmt.Errorf("%w: gmail message has no owner", ErrInvalid)
	case strings.TrimSpace(m.ProviderMessageID) == "":
		return fmt.Errorf("%w: gmail message has no provider id", ErrInvalid)
	case !knownSubjectTypes[m.SubjectType]:
		return fmt.Errorf("%w: gmail message subject_type %q is not a known subject type",
			ErrInvalid, m.SubjectType)
	case strings.TrimSpace(m.SubjectID) == "":
		return fmt.Errorf("%w: gmail message has no subject id", ErrInvalid)
	case strings.TrimSpace(m.ActivityID) == "":
		return fmt.Errorf("%w: gmail message names no activity", ErrInvalid)
	case m.Direction != GmailInbound && m.Direction != GmailOutbound:
		return fmt.Errorf("%w: gmail message direction %q is neither inbound nor outbound",
			ErrInvalid, m.Direction)
	case m.OccurredAt.IsZero():
		return fmt.Errorf("%w: gmail message has no timestamp", ErrInvalid)
	}
	return nil
}

// GmailSyncSummary is the browser-facing projection of a mailbox's sync
// state: progress and health, with no correspondence in it.
type GmailSyncSummary struct {
	OwnerID           string    `json:"owner_id"`
	BackfillComplete  bool      `json:"backfill_complete"`
	LastSyncedAt      time.Time `json:"last_synced_at"`
	LastMessageAt     time.Time `json:"last_message_at"`
	MessagesIndexed   int64     `json:"messages_indexed"`
	ActivitiesCreated int64     `json:"activities_created"`
	LastError         string    `json:"last_error"`
}

// Summary projects the state for a browser-facing response.
func (s *GmailSyncState) Summary() GmailSyncSummary {
	if s == nil {
		return GmailSyncSummary{}
	}
	return GmailSyncSummary{
		OwnerID:           s.OwnerID,
		BackfillComplete:  s.BackfillComplete,
		LastSyncedAt:      s.LastSyncedAt,
		LastMessageAt:     s.LastMessageAt,
		MessagesIndexed:   s.MessagesIndexed,
		ActivitiesCreated: s.ActivitiesCreated,
		LastError:         s.LastError,
	}
}

func cloneGmailSyncState(s *GmailSyncState) *GmailSyncState {
	if s == nil {
		return nil
	}
	out := *s
	return &out
}

func cloneGmailMessage(m *GmailMessage) *GmailMessage {
	if m == nil {
		return nil
	}
	out := *m
	return &out
}

// NormalizeEmail lowercases and trims an address for matching.
//
// It is exported because the sync layer has to key participants the same way
// the store matches Contacts; two normalizations that disagree by a space or
// a capital letter would silently stop matching anyone.
func NormalizeEmail(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
