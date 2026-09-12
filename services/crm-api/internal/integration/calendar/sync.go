package calendar

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

// Calendar synchronisation.
//
// The same rule as Gmail, for the same reason: an event becomes a CRM record
// only if one of its participants is already a Contact in this tenant. A
// calendar is at least as revealing as a mailbox — it names who somebody met,
// when, and how often — and every Activity here is readable by any colleague
// holding crm:activity:read.
//
// What differs is that events change. The store decides whether a change is
// worth a timeline entry (domain.ClassifyCalendarChange); this package
// decides what that entry says.

const (
	// backfillPast and backfillFuture bound the first pass. A CRM cares about
	// the meetings around now, and listing a decade of history costs quota to
	// find almost nothing. The future window is the longer of the two because
	// upcoming meetings are the ones a salesperson is about to walk into.
	backfillPast   = 90 * 24 * time.Hour
	backfillFuture = 180 * 24 * time.Hour

	// eventsPerPass bounds one calendar's share of a cycle, so a large
	// backfill cannot starve every other calendar. The cursor makes stopping
	// early free.
	eventsPerPass = 250

	listPageSize = 250

	// SyncLease is how long one pass may hold a calendar.
	SyncLease = 5 * time.Minute
)

// The bounded reason codes recorded on a calendar and reported as metrics.
const (
	ReasonNotConnected     = "not_connected"
	ReasonTokenUnavailable = "token_unavailable"  // #nosec G101 -- bounded status code, not a credential
	ReasonSyncTokenExpired = "sync_token_expired" // #nosec G101 -- bounded status code, not a credential
	ReasonProviderError    = "provider_error"
	ReasonStorageFailed    = "storage_failed"
)

// TokenSource hands out a usable access token for one connected account.
type TokenSource interface {
	AccessToken(ctx context.Context, scope domain.Scope, ownerID string) (string, error)
}

// Retainer applies the retention window to one mailbox's synced content. It
// is an interface, and optional, so this package does not depend on the
// privacy service — sync needs "please age this out", not an erasure engine.
type Retainer interface {
	ApplyRetention(ctx context.Context, scope domain.Scope, ownerID string) (domain.ErasureResult, error)
}

// API is the part of the Calendar client sync uses. Tests substitute it.
type API interface {
	List(ctx context.Context, accessToken string, opts ListOptions) (Page, error)
	CalendarTimeZone(ctx context.Context, accessToken, calendarID string) (string, error)
}

// Syncer walks connected calendars and records what matches.
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
		store: store, api: api, tokens: tokens, retention: retention,
		now: func() time.Time { return time.Now().UTC() }, log: log,
	}
}

// Writer returns the authenticated write service when the configured API
// client implements Google Calendar writes. Test read-only clients simply
// return nil, preserving the narrow sync interface.
func (s *Syncer) Writer() *Writer {
	if s == nil {
		return nil
	}
	api, ok := s.api.(WriteAPI)
	if !ok {
		return nil
	}
	return NewWriter(s.store, api, s.tokens)
}

// Result reports what one pass did.
type Result struct {
	// EventsExamined counts events read. Most will match nothing and are
	// never recorded.
	EventsExamined int
	EventsMatched  int
	// EntriesCreated counts timeline entries written — new meetings,
	// reschedules and cancellations. It is far lower than EventsMatched on a
	// steady-state pass, when nothing material has changed.
	EntriesCreated   int
	BackfillComplete bool
}

// SyncCalendar runs one pass over one connected calendar.
func (s *Syncer) SyncCalendar(
	ctx context.Context, scope domain.Scope, ownerID string,
) (Result, error) {
	state, err := s.store.ClaimCalendarSync(ctx, scope, ownerID, s.now(), SyncLease)
	if err != nil {
		return Result{}, err
	}

	result, syncErr := s.run(ctx, scope, ownerID, state)

	// Age out old content under the same lease — see the Gmail syncer for
	// why this lives in the pass rather than in a sweep of its own.
	if s.retention != nil {
		if _, err := s.retention.ApplyRetention(ctx, scope, ownerID); err != nil {
			s.log.Error("could not apply the retention window to a calendar", "error", err)
		}
	}

	// Release the lease and record the outcome whatever happened, so a failed
	// pass does not leave the calendar skipped until the lease expires.
	state.SyncingUntil = time.Time{}
	state.LastSyncedAt = s.now()
	if syncErr != nil {
		state.LastError = reasonFor(syncErr)
	} else {
		state.LastError = ""
	}
	if _, err := s.store.SaveCalendarSyncState(ctx, scope, state); err != nil {
		s.log.Error("could not save calendar sync state", "error", err)
		if syncErr == nil {
			syncErr = err
		}
	}
	result.BackfillComplete = state.BackfillComplete

	outcome := "synced"
	if syncErr != nil {
		outcome = state.LastError
	}
	metrics.CalendarSyncPassesTotal.WithLabelValues(outcome).Inc()
	if result.EntriesCreated > 0 {
		metrics.CalendarTimelineEntriesTotal.Add(float64(result.EntriesCreated))
	}
	return result, syncErr
}

func (s *Syncer) run(
	ctx context.Context, scope domain.Scope, ownerID string, state *domain.CalendarSyncState,
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
	if !hasCalendarRead(connection.GrantedScopes) {
		return Result{}, fmt.Errorf("%w: calendar access was not granted", errNotConnected)
	}

	token, err := s.tokens.AccessToken(ctx, scope, ownerID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", errTokenUnavailable, err)
	}

	// The calendar's own default zone is what an all-day event falls back to
	// when it carries none. Read once per pass rather than per event.
	defaultZone, err := s.api.CalendarTimeZone(ctx, token, "primary")
	if err != nil {
		// Not fatal: UTC is a defensible fallback and losing the whole pass
		// over a settings read would be worse.
		s.log.Warn("could not read the calendar's default time zone", "error", err)
		defaultZone = "UTC"
	}

	// Loaded once per pass, before anything is fetched, and failing closed:
	// the list records people who asked not to be tracked, so a pass that
	// cannot read it must not match anybody.
	suppressed, err := s.store.SuppressedSyncAddresses(ctx, scope)
	if err != nil {
		return Result{}, err
	}

	opts := ListOptions{CalendarID: "primary", PageSize: listPageSize}
	if state.NeedsBackfill() {
		now := s.now()
		opts.TimeMin = now.Add(-backfillPast)
		opts.TimeMax = now.Add(backfillFuture)
		opts.PageToken = state.BackfillPageToken
	} else {
		opts.SyncToken = state.SyncToken
	}
	return s.walk(ctx, scope, connection, state, token, defaultZone, opts, suppressed)
}

// walk pages through Google's answer, recording what matches.
//
// The sync token is stored only when a walk reaches its final page — Google
// sends nextSyncToken on that page alone, and storing anything earlier would
// mean resuming past events the remaining pages held.
func (s *Syncer) walk(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	state *domain.CalendarSyncState, token, defaultZone string, opts ListOptions,
	suppressed map[string]bool,
) (Result, error) {
	var result Result
	backfilling := opts.SyncToken == ""

	for result.EventsExamined < eventsPerPass {
		page, err := s.api.List(ctx, token, opts)
		if err != nil {
			if errors.Is(err, ErrSyncTokenExpired) {
				// Google has discarded the cursor, so an unknown amount was
				// missed. Re-list the window rather than resume from a
				// position that means nothing.
				state.SyncToken = ""
				state.BackfillPageToken = ""
				state.BackfillComplete = false
				return result, err
			}
			return result, err
		}

		if err := s.recordEvents(ctx, scope, connection, state, defaultZone,
			page.Events, suppressed, &result); err != nil {
			return result, err
		}

		if page.NextPageToken == "" {
			if page.NextSyncToken != "" {
				state.SyncToken = page.NextSyncToken
			}
			if backfilling {
				state.BackfillPageToken = ""
				state.BackfillComplete = true
			}
			break
		}
		opts.PageToken = page.NextPageToken
		if backfilling {
			state.BackfillPageToken = page.NextPageToken
		}
	}
	return result, nil
}

// recordEvents matches each event and records the ones with a CRM
// counterparty.
func (s *Syncer) recordEvents(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	state *domain.CalendarSyncState, defaultZone string, events []Event,
	suppressed map[string]bool, result *Result,
) error {
	for _, event := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if result.EventsExamined >= eventsPerPass {
			return nil
		}
		result.EventsExamined++

		matched, err := s.matchContacts(ctx, scope, connection, event, suppressed)
		if err != nil {
			return err
		}
		if len(matched) == 0 {
			// No CRM counterparty. Nothing is written and nothing about this
			// event is retained — not its title, not its attendees.
			continue
		}
		result.EventsMatched++
		state.EventsIndexed++
		if event.StartsAt.After(state.LastEventAt) {
			state.LastEventAt = event.StartsAt
		}

		for _, contact := range matched {
			change, err := s.recordEntry(ctx, scope, connection, event, contact, defaultZone)
			if err != nil {
				return err
			}
			if change != domain.CalendarUnchanged {
				result.EntriesCreated++
				state.ActivitiesCreated++
			}
		}
	}
	return nil
}

// matchContacts resolves attendees to Contacts, skipping suppressed
// addresses. The check comes before the lookup for the same reason it does in
// Gmail's matcher: a suppressed address that still resolved would be
// recorded, which is the failure this mechanism exists to prevent.
func (s *Syncer) matchContacts(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	event Event, suppressed map[string]bool,
) ([]*domain.Contact, error) {
	var matched []*domain.Contact
	seen := make(map[string]bool, 4)
	for _, address := range event.Participants(connection.GoogleAccountEmail) {
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

// recordEntry mirrors one event against one contact and lets the store decide
// whether it deserves a timeline entry.
func (s *Syncer) recordEntry(
	ctx context.Context, scope domain.Scope, connection *domain.GoogleConnection,
	event Event, contact *domain.Contact, defaultZone string,
) (domain.CalendarChange, error) {
	zone := event.TimeZone
	if zone == "" {
		zone = defaultZone
	}
	if zone == "" {
		zone = "UTC"
	}
	title := strings.TrimSpace(event.Title)
	if title == "" {
		title = "(no title)"
	}
	title = truncate(title, maxTitleLength)

	status := event.Status
	if status == "" {
		status = domain.CalendarStatusConfirmed
	}

	record := &domain.CalendarEvent{
		OwnerID:          connection.OwnerID,
		ProviderEventID:  event.ID,
		ProviderETag:     event.ETag,
		SubjectType:      domain.SubjectContact,
		SubjectID:        contact.ID,
		CalendarID:       "primary",
		ICalUID:          event.ICalUID,
		RecurringEventID: event.RecurringEventID,
		ActivityID:       domain.NewID(),
		Title:            title,
		Status:           status,
		StartsAt:         event.StartsAt,
		EndsAt:           event.EndsAt,
		AllDay:           event.AllDay,
		TimeZone:         zone,
		OrganizerAddress: strings.ToLower(strings.TrimSpace(event.Organizer)),
		AttendeeCount:    len(event.Attendees),
	}

	return s.store.RecordCalendarEvent(ctx, scope, record,
		func(change domain.CalendarChange) *domain.Activity {
			return &domain.Activity{
				ID:          domain.NewID(),
				Kind:        domain.CalendarActivityKind,
				SubjectType: domain.SubjectContact,
				SubjectID:   contact.ID,
				Notes:       timelineNote(change, record),
				ActorID:     connection.OwnerID,
				// The Activity is stamped with when the meeting is, not when
				// sync noticed. A timeline ordered by discovery time would
				// put next week's rescheduled meeting before last week's
				// completed one.
				OccurredAt: record.StartsAt,
			}
		})
}

// maxTitleLength bounds the one piece of event text kept.
const maxTitleLength = 300

// timelineNote renders what a person reads on the record's timeline.
//
// The time is always rendered in the event's own zone, never the reader's or
// the server's. A meeting at 09:00 in London is not 04:00 to the customer who
// attended it, and an all-day event on 1 August must not read as 31 July to
// anyone.
func timelineNote(change domain.CalendarChange, event *domain.CalendarEvent) string {
	verb := map[domain.CalendarChange]string{
		domain.CalendarScheduled:   "Meeting scheduled",
		domain.CalendarRescheduled: "Meeting rescheduled",
		domain.CalendarCancelled:   "Meeting cancelled",
	}[change]
	if verb == "" {
		verb = "Meeting updated"
	}
	return verb + ": " + event.Title + " (" + formatWhen(event) + ")"
}

// formatWhen renders the event's start for a human, in its own zone.
func formatWhen(event *domain.CalendarEvent) string {
	local := event.LocalStart()
	if event.AllDay {
		return local.Format("2 Jan 2006") + ", all day"
	}
	zone := event.TimeZone
	if zone == "" {
		zone = "UTC"
	}
	return local.Format("2 Jan 2006 15:04") + " " + zone
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// hasCalendarRead reports whether the granted scopes allow reading events. A
// user may grant Gmail and decline Calendar, which leaves a healthy connection
// this sync cannot use.
func hasCalendarRead(granted []string) bool {
	for _, scope := range granted {
		if strings.HasSuffix(scope, "/calendar.events") ||
			strings.HasSuffix(scope, "/calendar.events.readonly") ||
			strings.HasSuffix(scope, "/calendar.readonly") ||
			strings.HasSuffix(scope, "/calendar") {
			return true
		}
	}
	return false
}

var (
	errNotConnected     = errors.New("calendar: account is not connected")
	errTokenUnavailable = errors.New("calendar: no usable access token")
)

// reasonFor maps a failure onto a bounded code, so neither the stored state
// nor a metric label can carry a provider message.
func reasonFor(err error) string {
	switch {
	case errors.Is(err, errNotConnected):
		return ReasonNotConnected
	case errors.Is(err, errTokenUnavailable):
		return ReasonTokenUnavailable
	case errors.Is(err, ErrSyncTokenExpired):
		return ReasonSyncTokenExpired
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrNoTenant):
		return ReasonStorageFailed
	default:
		return ReasonProviderError
	}
}
