package gmail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/metrics"
)

// Synchronisation: walking a mailbox and turning what matches into CRM
// timeline entries.
//
// The rule everything here is arranged around, stated once:
//
//	A message becomes a CRM record only if one of its participants is
//	already a Contact in this tenant.
//
// That is not a filter applied at the end. Nothing is written until a match
// exists, and an unmatched message leaves no trace — no row, no counter of
// its subject, nothing. The reason is that the alternative is indefensible: a
// mailbox holds a person's whole correspondence, and this CRM's Activities
// are readable by every colleague with crm:activity:read. Syncing a mailbox
// wholesale would publish a user's private mail to their employer.

const (
	// backfillWindow bounds the first pass. Correspondence older than this is
	// of little use to a CRM and walking an entire mail history costs a great
	// deal of Gmail quota to find it. `-in:chats` drops Hangouts/Chat
	// records, which are not email and have no useful envelope.
	backfillWindow = "newer_than:90d -in:chats"

	// messagesPerPass bounds one mailbox's share of a sync cycle, so a large
	// backfill cannot starve every other mailbox. The cursor makes stopping
	// early free: the next pass resumes exactly where this one stopped.
	messagesPerPass = 100

	// listPageSize is what Gmail is asked for per list call.
	listPageSize = 100

	// SyncLease is how long one pass may hold a mailbox before another
	// replica may take it. It is generous relative to a pass, because the
	// cost of an over-long lease is a delayed sync while the cost of a short
	// one is two replicas doing the same work.
	SyncLease = 5 * time.Minute
)

// The bounded reason codes recorded on a mailbox and reported as metrics.
// They exist so neither the stored state, the metric label, nor a log line
// can carry a provider message.
const (
	ReasonNotConnected     = "not_connected"
	ReasonTokenUnavailable = "token_unavailable" // #nosec G101 -- bounded status code, not a credential
	ReasonHistoryExpired   = "history_expired"
	ReasonProviderError    = "provider_error"
	ReasonStorageFailed    = "storage_failed"
)

// TokenSource hands out a usable access token for one connected mailbox,
// refreshing it if needed.
//
// It is an interface rather than a direct dependency on the Google connection
// service so that this package does not import it: sync needs a token, not an
// OAuth implementation, and keeping that boundary is what stops the two
// packages growing into one.
type TokenSource interface {
	AccessToken(ctx context.Context, scope domain.Scope, ownerID string) (string, error)
}

// Retainer applies the retention window to one mailbox's synced content. It
// is an interface, and optional, so this package does not depend on the
// privacy service — sync needs "please age this out", not an erasure engine.
type Retainer interface {
	ApplyRetention(ctx context.Context, scope domain.Scope, ownerID string) (domain.ErasureResult, error)
}

// API is the part of the Gmail client sync uses. Tests substitute it.
type API interface {
	Profile(ctx context.Context, accessToken string) (Profile, error)
	ListMessageIDs(ctx context.Context, accessToken, query, pageToken string, pageSize int) ([]string, string, error)
	GetMessage(ctx context.Context, accessToken, id string) (Message, error)
	History(ctx context.Context, accessToken, startHistoryID, pageToken string) (HistoryPage, error)
}

// Syncer walks connected mailboxes and records what matches.
type Syncer struct {
	store  domain.Store
	api    API
	tokens TokenSource
	// retention is optional; nil means no window is configured.
	retention Retainer
	now       func() time.Time
	log       *slog.Logger
}

// NewSyncer wires the sync engine. retention may be nil.
func NewSyncer(
	store domain.Store, api API, tokens TokenSource, retention Retainer, log *slog.Logger,
) *Syncer {
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{
		store:     store,
		api:       api,
		tokens:    tokens,
		retention: retention,
		now:       func() time.Time { return time.Now().UTC() },
		log:       log,
	}
}

// Result reports what one pass did.
type Result struct {
	// MessagesExamined counts messages whose envelope was fetched. It is
	// always at least MessagesMatched, usually much larger, and it is not
	// stored anywhere per-message: an unmatched message leaves no trace.
	MessagesExamined int
	// MessagesMatched counts messages with at least one CRM Contact on them.
	MessagesMatched int
	// EntriesCreated counts timeline entries actually written. It is lower
	// than MessagesMatched on a re-sync, when the entries already exist.
	EntriesCreated int
	// BackfillComplete reports whether the mailbox has finished its first
	// pass over the backfill window.
	BackfillComplete bool
}

// SyncMailbox runs one pass over one connected mailbox.
//
// It claims a lease first and always releases it, including on failure: a
// mailbox left leased by a failed pass would be skipped until the lease
// expired, which turns one bad pass into several minutes of no sync.
func (s *Syncer) SyncMailbox(
	ctx context.Context, scope domain.Scope, ownerID string,
) (Result, error) {
	state, err := s.store.ClaimGmailSync(ctx, scope, ownerID, s.now(), SyncLease)
	if err != nil {
		// ErrConflict means another replica has it. That is a normal outcome
		// of two pollers running, not a failure.
		return Result{}, err
	}

	result, syncErr := s.run(ctx, scope, ownerID, state)

	// Age out old content while this pass still holds the lease. Doing it
	// here rather than in a separate sweep means no second scheduler and, more
	// importantly, no window in which a pass re-indexes content another job
	// is concurrently deleting. A retention failure does not fail the sync:
	// the mail already reached the timeline, and the next pass tries again.
	if s.retention != nil {
		if _, err := s.retention.ApplyRetention(ctx, scope, ownerID); err != nil {
			s.log.Error("could not apply the retention window to a mailbox", "error", err)
		}
	}

	// Release the lease and record the outcome in one write, whatever
	// happened. state is the claimed record, so this also persists whatever
	// progress run() made before failing.
	state.SyncingUntil = time.Time{}
	state.LastSyncedAt = s.now()
	if syncErr != nil {
		state.LastError = reasonFor(syncErr)
	} else {
		state.LastError = ""
	}
	if _, err := s.store.SaveGmailSyncState(ctx, scope, state); err != nil {
		s.log.Error("could not save gmail sync state", "error", err)
		if syncErr == nil {
			syncErr = err
		}
	}
	result.BackfillComplete = state.BackfillComplete

	outcome := "synced"
	if syncErr != nil {
		outcome = state.LastError
	}
	metrics.GmailSyncPassesTotal.WithLabelValues(outcome).Inc()
	if result.EntriesCreated > 0 {
		metrics.GmailTimelineEntriesTotal.Add(float64(result.EntriesCreated))
	}
	return result, syncErr
}

// run does the work between claiming and releasing the lease. It mutates
// state so the caller persists whatever progress was made even on failure —
// a backfill that indexed ninety messages and then hit a quota error must not
// start over.
func (s *Syncer) run(
	ctx context.Context, scope domain.Scope, ownerID string, state *domain.GmailSyncState,
) (Result, error) {
	connection, err := s.store.GetGoogleConnection(ctx, scope, ownerID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return Result{}, fmt.Errorf("%w: %s", errNotConnected, ownerID)
		}
		return Result{}, err
	}
	if connection.Status != domain.GoogleStatusConnected {
		return Result{}, fmt.Errorf("%w: %s", errNotConnected, ownerID)
	}
	if !hasGmailRead(connection.GrantedScopes) {
		// Partial consent: the user connected but did not grant mailbox
		// reading. Not an error to retry — a state to report.
		return Result{}, fmt.Errorf("%w: gmail read access was not granted", errNotConnected)
	}

	token, err := s.tokens.AccessToken(ctx, scope, ownerID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", errTokenUnavailable, err)
	}

	// The suppression list is loaded once per pass rather than queried per
	// participant: a pass examines hundreds of addresses and the list is
	// small. It is loaded before any message is fetched, so a pass that
	// cannot read it does no matching at all — failing closed here is the
	// only safe direction, because the list exists to record people who
	// asked not to be tracked.
	suppressed, err := s.store.SuppressedSyncAddresses(ctx, scope)
	if err != nil {
		return Result{}, err
	}

	if state.NeedsBackfill() {
		return s.backfill(ctx, scope, connection, state, token, suppressed)
	}
	return s.incremental(ctx, scope, connection, state, token, suppressed)
}

// backfill walks the mailbox's recent history for the first time.
//
// The history cursor is read *before* the walk and held until the walk
// finishes. Reading it afterwards would skip every message that arrived
// during the backfill: those messages are neither in the pages already walked
// nor after a cursor taken at the end.
func (s *Syncer) backfill(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	state *domain.GmailSyncState, token string, suppressed map[string]bool,
) (Result, error) {
	var result Result

	if state.LastHistoryID == "" {
		profile, err := s.api.Profile(ctx, token)
		if err != nil {
			return result, err
		}
		state.LastHistoryID = profile.HistoryID
	}

	pageToken := state.BackfillPageToken
	for result.MessagesExamined < messagesPerPass {
		ids, next, err := s.api.ListMessageIDs(ctx, token, backfillWindow, pageToken, listPageSize)
		if err != nil {
			return result, err
		}
		if err := s.indexMessages(ctx, scope, connection, state, token, ids, suppressed, &result); err != nil {
			return result, err
		}
		pageToken = next
		state.BackfillPageToken = next
		if next == "" {
			// The window is fully walked. Only now is the cursor taken at the
			// start safe to keep as the incremental starting point.
			state.BackfillComplete = true
			break
		}
	}
	return result, nil
}

// incremental asks Gmail what changed since the stored cursor.
//
// The new cursor is saved only after the last page. Gmail returns the
// mailbox's current history id on every page, so storing it early would skip
// whatever the remaining pages held.
func (s *Syncer) incremental(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	state *domain.GmailSyncState, token string, suppressed map[string]bool,
) (Result, error) {
	var result Result
	pageToken := ""
	latest := state.LastHistoryID

	for result.MessagesExamined < messagesPerPass {
		page, err := s.api.History(ctx, token, state.LastHistoryID, pageToken)
		if err != nil {
			if errors.Is(err, ErrHistoryExpired) {
				// Gmail no longer holds history back to our cursor, so an
				// unknown amount was missed. Start a fresh backfill rather
				// than resume from a position that means nothing.
				state.LastHistoryID = ""
				state.BackfillPageToken = ""
				state.BackfillComplete = false
				return result, err
			}
			return result, err
		}
		if err := s.indexMessages(ctx, scope, connection, state, token, page.MessageIDs, suppressed, &result); err != nil {
			return result, err
		}
		if page.HistoryID != "" {
			latest = page.HistoryID
		}
		if page.NextPageToken == "" {
			state.LastHistoryID = latest
			break
		}
		pageToken = page.NextPageToken
	}
	return result, nil
}

// indexMessages fetches each message's envelope and records the ones that
// match a Contact in this tenant.
func (s *Syncer) indexMessages(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	state *domain.GmailSyncState, token string, ids []string,
	suppressed map[string]bool, result *Result,
) error {
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if result.MessagesExamined >= messagesPerPass {
			return nil
		}
		message, err := s.api.GetMessage(ctx, token, id)
		if err != nil {
			return err
		}
		result.MessagesExamined++

		matched, err := s.matchContacts(ctx, scope, connection, message, suppressed)
		if err != nil {
			return err
		}
		if len(matched) == 0 {
			// No CRM counterparty. Nothing is written, and nothing about this
			// message is retained — not its subject, not its participants,
			// not the fact that it was examined.
			continue
		}
		result.MessagesMatched++
		state.MessagesIndexed++
		if message.SentAt.After(state.LastMessageAt) {
			state.LastMessageAt = message.SentAt
		}

		for _, contact := range matched {
			created, err := s.recordEntry(ctx, scope, connection, message, contact)
			if err != nil {
				return err
			}
			if created {
				result.EntriesCreated++
				state.ActivitiesCreated++
			}
		}
	}
	return nil
}

// matchContacts resolves the message's participants to Contacts in this
// tenant, in a stable order.
// matchContacts resolves the message's participants to Contacts in this
// tenant, skipping any address on the suppression list.
//
// The suppression check comes before the Contact lookup, not after. A
// suppressed address that still resolved to a Contact would be matched and
// recorded, and "we erased it and then indexed it again" is the failure this
// whole mechanism exists to prevent.
func (s *Syncer) matchContacts(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	message Message, suppressed map[string]bool,
) ([]*domain.Contact, error) {
	var matched []*domain.Contact
	seen := make(map[string]bool, 4)
	for _, address := range message.Participants(connection.GoogleAccountEmail) {
		if suppressed[address] {
			continue
		}
		contact, err := s.store.FindContactByEmail(ctx, scope, address)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if seen[contact.ID] {
			continue
		}
		seen[contact.ID] = true
		matched = append(matched, contact)
	}
	return matched, nil
}

// recordEntry writes one timeline entry, or reports that it already existed.
func (s *Syncer) recordEntry(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	message Message, contact *domain.Contact,
) (bool, error) {
	direction := domain.GmailInbound
	if message.IsSent() {
		direction = domain.GmailOutbound
	}
	occurredAt := message.SentAt
	if occurredAt.IsZero() {
		occurredAt = s.now()
	}
	subject := strings.TrimSpace(message.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	subject = truncate(subject, maxSubjectLength)

	activityID := domain.NewID()
	entry := &domain.GmailMessage{
		OwnerID:           connection.OwnerID,
		ProviderMessageID: message.ID,
		SubjectType:       domain.SubjectContact,
		SubjectID:         contact.ID,
		ThreadID:          message.ThreadID,
		ActivityID:        activityID,
		MessageSubject:    subject,
		FromAddress:       firstAddress(message.From),
		Direction:         direction,
		OccurredAt:        occurredAt,
	}
	activity := &domain.Activity{
		ID:          activityID,
		Kind:        domain.GmailActivityKind,
		SubjectType: domain.SubjectContact,
		SubjectID:   contact.ID,
		Notes:       timelineNote(direction, subject, entry.FromAddress),
		ActorID:     connection.OwnerID,
		OccurredAt:  occurredAt,
	}
	return s.store.RecordGmailActivity(ctx, scope, entry, activity)
}

// maxSubjectLength bounds the one piece of message text this system keeps.
// Subject headers are occasionally enormous, and an unbounded one would be an
// unbounded row.
const maxSubjectLength = 500

// timelineNote renders the entry a person reads on the record's timeline.
func timelineNote(direction, subject, from string) string {
	verb := "Email received"
	if direction == domain.GmailOutbound {
		verb = "Email sent"
	}
	note := verb + ": " + subject
	if direction == domain.GmailInbound && from != "" {
		note += " (from " + from + ")"
	}
	return note
}

func firstAddress(header string) string {
	if addresses := parseAddresses(header); len(addresses) > 0 {
		return addresses[0]
	}
	return ""
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// hasGmailRead reports whether the granted scopes actually allow reading
// mail. A user may grant calendar access and decline Gmail, which leaves a
// perfectly healthy connection that this sync cannot use.
func hasGmailRead(granted []string) bool {
	for _, scope := range granted {
		if strings.HasSuffix(scope, "/gmail.readonly") || strings.HasSuffix(scope, "/gmail.modify") ||
			scope == "https://mail.google.com/" {
			return true
		}
	}
	return false
}

// The sentinel errors this package classifies its own failures with, so
// reasonFor never has to inspect a provider message.
var (
	errNotConnected     = errors.New("gmail: mailbox is not connected")
	errTokenUnavailable = errors.New("gmail: no usable access token")
)

// reasonFor maps a failure onto a bounded code. Anything unrecognized is
// provider_error rather than the error's text, which is what keeps a Gmail
// response out of the stored state and the metric label.
func reasonFor(err error) string {
	switch {
	case errors.Is(err, errNotConnected):
		return ReasonNotConnected
	case errors.Is(err, errTokenUnavailable):
		return ReasonTokenUnavailable
	case errors.Is(err, ErrHistoryExpired):
		return ReasonHistoryExpired
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrNoTenant):
		return ReasonStorageFailed
	default:
		return ReasonProviderError
	}
}
