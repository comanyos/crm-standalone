package domain

import (
	"fmt"
	"strings"
	"time"
)

// Google Calendar synchronisation state and the event mirror
// (migrations/0021_calendar_sync.sql).
//
// The difference from Gmail that shapes everything here: a calendar event is
// mutable. Meetings move and meetings get cancelled, and a customer being
// told the wrong time is a real cost. So this file carries two things mail
// does not need — a mirror of Google's current state, and a fingerprint that
// decides which changes are worth telling a person about.

// The event statuses Google reports, mirrored by the CHECK constraint.
const (
	CalendarStatusConfirmed = "confirmed"
	CalendarStatusTentative = "tentative"
	CalendarStatusCancelled = "cancelled"
)

var calendarStatuses = map[string]bool{
	CalendarStatusConfirmed: true,
	CalendarStatusTentative: true,
	CalendarStatusCancelled: true,
}

// CalendarActivityKind is the Activity.Kind synced meetings are logged under.
// Distinct from a hand-logged meeting for the same reason GmailActivityKind
// is: only one of the two is evidence that the meeting was actually in
// somebody's calendar.
const CalendarActivityKind = "meeting_synced"

// CalendarSyncState is one connected calendar's cursor.
type CalendarSyncState struct {
	OwnerID string `json:"owner_id"`
	// SyncToken is Google's own incremental cursor. It expires into HTTP 410,
	// at which point the window must be re-listed.
	SyncToken         string    `json:"sync_token"`
	BackfillPageToken string    `json:"backfill_page_token"`
	BackfillComplete  bool      `json:"backfill_complete"`
	SyncingUntil      time.Time `json:"syncing_until"`
	LastSyncedAt      time.Time `json:"last_synced_at"`
	// LastEventAt is the start of the latest event indexed — usually in the
	// future, for a calendar. It is progress information, not a freshness
	// claim.
	LastEventAt       time.Time `json:"last_event_at"`
	EventsIndexed     int64     `json:"events_indexed"`
	ActivitiesCreated int64     `json:"activities_created"`
	LastError         string    `json:"last_error"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Validate enforces what the database also enforces.
func (s *CalendarSyncState) Validate() error {
	if strings.TrimSpace(s.OwnerID) == "" {
		return fmt.Errorf("%w: calendar sync state has no owner", ErrInvalid)
	}
	if s.EventsIndexed < 0 || s.ActivitiesCreated < 0 {
		return fmt.Errorf("%w: calendar sync counters cannot be negative", ErrInvalid)
	}
	if len(s.LastError) > maxGoogleErrorLength {
		return fmt.Errorf("%w: calendar sync error reason exceeds %d characters",
			ErrInvalid, maxGoogleErrorLength)
	}
	return nil
}

// NeedsBackfill reports whether the next pass must list the window rather
// than ask Google what changed. A calendar with no token has never synced, or
// had its token expire — and resuming from an expired token loses everything
// that changed while it was invalid.
func (s *CalendarSyncState) NeedsBackfill() bool {
	return s == nil || !s.BackfillComplete || s.SyncToken == ""
}

// CalendarEvent is one event as this CRM holds it: a mirror of Google's
// current state, plus the link to the record it was matched to.
//
// There is deliberately no description and no location. A calendar holds
// medical appointments and job interviews as readily as customer meetings,
// and every Activity here is readable by any colleague with
// crm:activity:read.
type CalendarEvent struct {
	OwnerID         string `json:"owner_id"`
	ProviderEventID string `json:"provider_event_id"`
	SubjectType     string `json:"subject_type"`
	SubjectID       string `json:"subject_id"`
	CalendarID      string `json:"calendar_id"`
	// ICalUID is stable across calendars, so the same meeting on the
	// organizer's and an attendee's calendar shares it. RecurringEventID
	// names the series an instance belongs to. Neither is read yet.
	ICalUID          string    `json:"ical_uid"`
	RecurringEventID string    `json:"recurring_event_id"`
	ActivityID       string    `json:"activity_id"`
	Title            string    `json:"title"`
	Status           string    `json:"status"`
	StartsAt         time.Time `json:"starts_at"`
	EndsAt           time.Time `json:"ends_at"`
	// AllDay and TimeZone travel together. An all-day event on 1 August in
	// Asia/Kolkata is 2026-07-31T18:30Z; without the zone a reader shows the
	// customer the wrong day.
	AllDay           bool   `json:"all_day"`
	TimeZone         string `json:"time_zone"`
	OrganizerAddress string `json:"organizer_address"`
	AttendeeCount    int    `json:"attendee_count"`
	// StateKey fingerprints the material state last posted to the timeline.
	StateKey  string    `json:"state_key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate enforces what the database also enforces.
func (e *CalendarEvent) Validate() error {
	switch {
	case strings.TrimSpace(e.OwnerID) == "":
		return fmt.Errorf("%w: calendar event has no owner", ErrInvalid)
	case strings.TrimSpace(e.ProviderEventID) == "":
		return fmt.Errorf("%w: calendar event has no provider id", ErrInvalid)
	case !knownSubjectTypes[e.SubjectType]:
		return fmt.Errorf("%w: calendar event subject_type %q is not a known subject type",
			ErrInvalid, e.SubjectType)
	case strings.TrimSpace(e.SubjectID) == "":
		return fmt.Errorf("%w: calendar event has no subject id", ErrInvalid)
	case strings.TrimSpace(e.ActivityID) == "":
		return fmt.Errorf("%w: calendar event names no activity", ErrInvalid)
	case !calendarStatuses[e.Status]:
		return fmt.Errorf("%w: calendar event status %q is not one Google reports",
			ErrInvalid, e.Status)
	case e.StartsAt.IsZero():
		return fmt.Errorf("%w: calendar event has no start", ErrInvalid)
	case !e.EndsAt.IsZero() && e.EndsAt.Before(e.StartsAt):
		return fmt.Errorf("%w: calendar event ends before it starts", ErrInvalid)
	case e.AllDay && strings.TrimSpace(e.TimeZone) == "":
		return fmt.Errorf(
			"%w: an all-day calendar event needs its time zone, or its date cannot be rendered correctly",
			ErrInvalid)
	}
	return nil
}

// MaterialStateKey fingerprints the part of an event a person would want to
// be told about.
//
// Status and start, and nothing else. A meeting that moved matters; a meeting
// that was cancelled matters; a corrected typo in the title, an added agenda
// line, or somebody flipping their RSVP does not — and posting a timeline
// entry for each of those would bury the two that do under noise nobody
// reads.
//
// The title is deliberately excluded even though it is stored. Renaming
// "Call" to "Discovery call" is not news; and if it were included, every such
// edit would look to a reader exactly like a rescheduled meeting.
func (e *CalendarEvent) MaterialStateKey() string {
	return e.Status + "@" + e.StartsAt.UTC().Format(time.RFC3339)
}

// CalendarChange describes what a sync pass found, so the timeline entry can
// say which of the three things happened.
type CalendarChange int

// The transitions worth a timeline entry.
const (
	// CalendarUnchanged means the material state matches what was already
	// posted. The mirror is still updated; the timeline is not.
	CalendarUnchanged CalendarChange = iota
	// CalendarScheduled is the first time this event reached this record.
	CalendarScheduled
	// CalendarRescheduled means the start moved.
	CalendarRescheduled
	// CalendarCancelled means Google now reports the event as cancelled.
	CalendarCancelled
)

// ClassifyCalendarChange decides what a pass should report. previous is the
// stored state key, empty when the event is new to this record.
func ClassifyCalendarChange(previousStateKey string, next *CalendarEvent) CalendarChange {
	if next == nil {
		return CalendarUnchanged
	}
	if previousStateKey == next.MaterialStateKey() {
		return CalendarUnchanged
	}
	switch {
	case next.Status == CalendarStatusCancelled:
		return CalendarCancelled
	case previousStateKey == "":
		return CalendarScheduled
	default:
		return CalendarRescheduled
	}
}

// LocalStart renders the event's start in its own time zone.
//
// This is what a timeline entry must say. Rendering in the viewer's zone
// moves an all-day event across midnight for anyone far enough east or west,
// which is how a customer gets told the wrong day. An unknown or unparseable
// zone falls back to UTC rather than to the server's local time, which is a
// property of the machine and not of anything the user chose.
func (e *CalendarEvent) LocalStart() time.Time {
	if e == nil {
		return time.Time{}
	}
	if e.TimeZone == "" {
		return e.StartsAt.UTC()
	}
	location, err := time.LoadLocation(e.TimeZone)
	if err != nil {
		return e.StartsAt.UTC()
	}
	return e.StartsAt.In(location)
}

// CalendarSyncSummary is the browser-facing projection: progress and health,
// with no meeting in it.
type CalendarSyncSummary struct {
	OwnerID           string    `json:"owner_id"`
	BackfillComplete  bool      `json:"backfill_complete"`
	LastSyncedAt      time.Time `json:"last_synced_at"`
	LastEventAt       time.Time `json:"last_event_at"`
	EventsIndexed     int64     `json:"events_indexed"`
	ActivitiesCreated int64     `json:"activities_created"`
	LastError         string    `json:"last_error"`
}

// Summary projects the state for a browser-facing response.
func (s *CalendarSyncState) Summary() CalendarSyncSummary {
	if s == nil {
		return CalendarSyncSummary{}
	}
	return CalendarSyncSummary{
		OwnerID:           s.OwnerID,
		BackfillComplete:  s.BackfillComplete,
		LastSyncedAt:      s.LastSyncedAt,
		LastEventAt:       s.LastEventAt,
		EventsIndexed:     s.EventsIndexed,
		ActivitiesCreated: s.ActivitiesCreated,
		LastError:         s.LastError,
	}
}

func cloneCalendarSyncState(s *CalendarSyncState) *CalendarSyncState {
	if s == nil {
		return nil
	}
	out := *s
	return &out
}

func cloneCalendarEvent(e *CalendarEvent) *CalendarEvent {
	if e == nil {
		return nil
	}
	out := *e
	return &out
}
