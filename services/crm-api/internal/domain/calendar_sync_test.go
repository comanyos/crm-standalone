package domain

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Calendar synchronisation storage, against both implementations.
//
// The properties here are different from Gmail's, because events are mutable.
// What has to hold: a meeting that moves posts once, a meeting that is
// cancelled posts once, and a meeting whose description was edited posts
// nothing — while the mirror stays current in all three cases.

func eachCalendarStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

func calendarScope(label string) Scope {
	return Scope{
		TenantID: "tenant-cal-" + label + "-" + gmailRun,
		OrgID:    "org-cal-" + label + "-" + gmailRun,
	}
}

func calendarFixture(t *testing.T, s Store, scope Scope) *Contact {
	t.Helper()
	account, err := s.CreateAccount(context.Background(), scope, &Account{
		Name: "Acme " + gmailRun, OwnerID: "owner-1",
	})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := s.CreateContact(context.Background(), scope, &Contact{
		Email: "attendee-" + gmailRun + "@acme.example", AccountID: account.ID,
		FirstName: "Ada", LastName: "Attendee",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return contact
}

func calendarEvent(owner, eventID string, contact *Contact, starts time.Time) *CalendarEvent {
	return &CalendarEvent{
		OwnerID:          owner,
		ProviderEventID:  eventID,
		SubjectType:      SubjectContact,
		SubjectID:        contact.ID,
		CalendarID:       "primary",
		ICalUID:          eventID + "@google.com",
		ActivityID:       NewID(),
		Title:            "Quarterly review",
		Status:           CalendarStatusConfirmed,
		StartsAt:         starts.UTC().Truncate(storedTimePrecision),
		EndsAt:           starts.UTC().Add(time.Hour).Truncate(storedTimePrecision),
		TimeZone:         "Europe/London",
		OrganizerAddress: contact.Email,
		AttendeeCount:    2,
	}
}

// activityForEvent builds the timeline entry a change deserves. Sync does the
// same thing; this is the test's stand-in for it.
func activityForEvent(event *CalendarEvent) func(CalendarChange) *Activity {
	return func(change CalendarChange) *Activity {
		note := map[CalendarChange]string{
			CalendarScheduled:   "Meeting scheduled",
			CalendarRescheduled: "Meeting rescheduled",
			CalendarCancelled:   "Meeting cancelled",
		}[change]
		return &Activity{
			ID:          NewID(),
			Kind:        CalendarActivityKind,
			SubjectType: event.SubjectType,
			SubjectID:   event.SubjectID,
			Notes:       note + ": " + event.Title,
			ActorID:     event.OwnerID,
			OccurredAt:  event.StartsAt,
		}
	}
}

// TestARescheduledMeetingPostsExactlyOnce is the core of the conflict policy.
// A customer told twice that a meeting moved, or never told at all, are both
// failures — and both are what happens without the state fingerprint.
func TestARescheduledMeetingPostsExactlyOnce(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("reschedule-" + name)
		contact := calendarFixture(t, s, scope)
		start := time.Now().UTC().Add(24 * time.Hour)

		event := calendarEvent("user-1", "evt-move", contact, start)
		change, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event))
		if err != nil {
			t.Fatalf("first record: %v", err)
		}
		if change != CalendarScheduled {
			t.Fatalf("first record reported %v, want CalendarScheduled", change)
		}

		// Google reports the same event again, unchanged. Nothing new.
		same := calendarEvent("user-1", "evt-move", contact, start)
		change, err = s.RecordCalendarEvent(ctx, scope, same, activityForEvent(same))
		if err != nil {
			t.Fatalf("unchanged record: %v", err)
		}
		if change != CalendarUnchanged {
			t.Errorf("re-reporting an unchanged event posted %v", change)
		}

		// The meeting moves.
		moved := calendarEvent("user-1", "evt-move", contact, start.Add(2*time.Hour))
		change, err = s.RecordCalendarEvent(ctx, scope, moved, activityForEvent(moved))
		if err != nil {
			t.Fatalf("moved record: %v", err)
		}
		if change != CalendarRescheduled {
			t.Errorf("a moved meeting reported %v, want CalendarRescheduled", change)
		}

		// And the same moved state again posts nothing.
		movedAgain := calendarEvent("user-1", "evt-move", contact, start.Add(2*time.Hour))
		change, err = s.RecordCalendarEvent(ctx, scope, movedAgain, activityForEvent(movedAgain))
		if err != nil {
			t.Fatalf("moved again: %v", err)
		}
		if change != CalendarUnchanged {
			t.Errorf("re-reporting the moved meeting posted %v again", change)
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 2 {
			t.Errorf("timeline holds %d entries, want 2 (scheduled, then rescheduled)", len(activities))
		}
	})
}

// TestAnImmaterialEditUpdatesTheMirrorSilently. A retitled meeting must stay
// current in the CRM without posting to the customer's timeline — otherwise
// the two entries that matter are buried under edits nobody needs to read.
func TestAnImmaterialEditUpdatesTheMirrorSilently(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("retitle-" + name)
		contact := calendarFixture(t, s, scope)
		start := time.Now().UTC().Add(24 * time.Hour)

		event := calendarEvent("user-1", "evt-title", contact, start)
		if _, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event)); err != nil {
			t.Fatalf("first record: %v", err)
		}

		renamed := calendarEvent("user-1", "evt-title", contact, start)
		renamed.Title = "Quarterly review — agenda attached"
		renamed.AttendeeCount = 5
		change, err := s.RecordCalendarEvent(ctx, scope, renamed, activityForEvent(renamed))
		if err != nil {
			t.Fatalf("retitled record: %v", err)
		}
		if change != CalendarUnchanged {
			t.Errorf("a retitled meeting posted %v; only status and start are material", change)
		}

		// The mirror moved even though the timeline did not.
		events, err := s.ListCalendarEventsForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("mirror holds %d rows, want 1", len(events))
		}
		if events[0].Title != "Quarterly review — agenda attached" {
			t.Errorf("mirror title = %q; the mirror should follow Google even when the timeline does not",
				events[0].Title)
		}
		if events[0].AttendeeCount != 5 {
			t.Errorf("mirror attendee count = %d, want 5", events[0].AttendeeCount)
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 1 {
			t.Errorf("timeline holds %d entries after an immaterial edit, want 1", len(activities))
		}
	})
}

// TestACancelledMeetingPostsOnce. A cancellation is the change a customer
// most needs to see, and the one most likely to be reported repeatedly:
// Google keeps returning cancelled events in subsequent syncs.
func TestACancelledMeetingPostsOnce(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("cancel-" + name)
		contact := calendarFixture(t, s, scope)
		start := time.Now().UTC().Add(24 * time.Hour)

		event := calendarEvent("user-1", "evt-cancel", contact, start)
		if _, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event)); err != nil {
			t.Fatalf("schedule: %v", err)
		}

		for pass := 0; pass < 3; pass++ {
			cancelled := calendarEvent("user-1", "evt-cancel", contact, start)
			cancelled.Status = CalendarStatusCancelled
			change, err := s.RecordCalendarEvent(ctx, scope, cancelled, activityForEvent(cancelled))
			if err != nil {
				t.Fatalf("cancel pass %d: %v", pass, err)
			}
			want := CalendarUnchanged
			if pass == 0 {
				want = CalendarCancelled
			}
			if change != want {
				t.Errorf("cancel pass %d reported %v, want %v", pass, change, want)
			}
		}

		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 2 {
			t.Errorf("timeline holds %d entries, want 2 (scheduled, then cancelled)", len(activities))
		}
	})
}

// TestConcurrentPassesPostOneEntry. Two replicas whose leases overlapped both
// see "the start moved"; without the row lock both would post, and the
// customer's timeline would say the meeting was rescheduled twice.
func TestConcurrentPassesPostOneEntry(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("cal-race-" + name)
		contact := calendarFixture(t, s, scope)
		start := time.Now().UTC().Add(24 * time.Hour)

		const racers = 8
		var (
			wg     sync.WaitGroup
			mu     sync.Mutex
			posted int
		)
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				event := calendarEvent("user-1", "evt-race", contact, start)
				change, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event))
				if err == nil && change != CalendarUnchanged {
					mu.Lock()
					posted++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if posted != 1 {
			t.Errorf("%d of %d concurrent passes posted, want exactly 1", posted, racers)
		}
		activities, err := s.ListActivitiesForSubject(ctx, scope, SubjectContact, contact.ID)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		if len(activities) != 1 {
			t.Errorf("timeline holds %d entries, want 1", len(activities))
		}
	})
}

// TestAllDayEventsKeepTheirZone. An all-day event on 1 August in
// Asia/Kolkata is 2026-07-31T18:30Z; a store that loses the zone shows the
// customer the wrong day.
func TestAllDayEventsKeepTheirZone(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("allday-" + name)
		contact := calendarFixture(t, s, scope)

		kolkata, err := time.LoadLocation("Asia/Kolkata")
		if err != nil {
			t.Skipf("no tzdata for Asia/Kolkata: %v", err)
		}
		localMidnight := time.Date(2026, 8, 1, 0, 0, 0, 0, kolkata)

		event := calendarEvent("user-1", "evt-allday", contact, localMidnight)
		event.AllDay = true
		event.TimeZone = "Asia/Kolkata"
		event.EndsAt = time.Time{}
		if _, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event)); err != nil {
			t.Fatalf("record: %v", err)
		}

		events, err := s.ListCalendarEventsForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		stored := events[0]
		if !stored.AllDay {
			t.Error("the all-day flag did not survive")
		}
		if stored.TimeZone != "Asia/Kolkata" {
			t.Errorf("time zone = %q, want Asia/Kolkata", stored.TimeZone)
		}
		// The instant is the same one, whatever zone it is stored in.
		if !stored.StartsAt.Equal(localMidnight) {
			t.Errorf("start = %v, want the same instant as %v", stored.StartsAt, localMidnight)
		}
		// And rendering it in the event's own zone gives back 1 August, which
		// is the whole point — in UTC it is 31 July.
		local := stored.LocalStart()
		if local.Day() != 1 || local.Month() != time.August {
			t.Errorf("LocalStart = %v, want 1 August in the event's own zone", local)
		}
		if stored.StartsAt.UTC().Day() != 31 {
			t.Fatal("the fixture is wrong: this instant should be 31 July in UTC, " +
				"or the test proves nothing")
		}
	})
}

// TestAnAllDayEventWithoutAZoneIsRejected: a row that cannot be rendered
// correctly is worse than one that is absent.
func TestAnAllDayEventWithoutAZoneIsRejected(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("nozone-" + name)
		contact := calendarFixture(t, s, scope)

		event := calendarEvent("user-1", "evt-nozone", contact, time.Now().UTC())
		event.AllDay = true
		event.TimeZone = ""
		if _, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event)); !errors.Is(err, ErrInvalid) {
			t.Errorf("all-day event with no zone: %v, want ErrInvalid", err)
		}
	})
}

// TestCalendarEventsAreTenantScoped: a meeting names who a person met and
// when, which is exactly the sort of row a cross-tenant read must not return.
func TestCalendarEventsAreTenantScoped(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := calendarScope("cal-victim-" + name)
		attacker := calendarScope("cal-attacker-" + name)
		contact := calendarFixture(t, s, victim)

		event := calendarEvent("user-1", "evt-private", contact, time.Now().UTC().Add(time.Hour))
		if _, err := s.RecordCalendarEvent(ctx, victim, event, activityForEvent(event)); err != nil {
			t.Fatalf("seed: %v", err)
		}

		events, err := s.ListCalendarEventsForSubject(ctx, attacker, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list as attacker: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("another tenant read %d meetings", len(events))
		}
		if _, err := s.GetCalendarSyncState(ctx, attacker, "user-1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("cross-tenant sync state read: %v, want ErrNotFound", err)
		}

		// And an event naming another tenant's contact cannot be written.
		cross := calendarEvent("user-1", "evt-cross", contact, time.Now().UTC())
		if _, err := s.RecordCalendarEvent(ctx, attacker, cross, activityForEvent(cross)); !errors.Is(err, ErrInvalid) {
			t.Errorf("cross-tenant subject: %v, want ErrInvalid", err)
		}
	})
}

// TestCalendarLeaseAdmitsOnePass.
func TestCalendarLeaseAdmitsOnePass(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("cal-lease-" + name)
		now := time.Now().UTC()

		if _, err := s.ClaimCalendarSync(ctx, scope, "user-1", now, time.Minute); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if _, err := s.ClaimCalendarSync(ctx, scope, "user-1", now, time.Minute); !errors.Is(err, ErrConflict) {
			t.Fatalf("second claim: %v, want ErrConflict", err)
		}
		if _, err := s.ClaimCalendarSync(ctx, scope, "user-1", now.Add(2*time.Minute), time.Minute); err != nil {
			t.Fatalf("claim after expiry: %v", err)
		}
	})
}

// TestCalendarCursorRoundTripsEveryField — the same round-trip check the
// Google connection lacked when a field it never stored cost a working
// authorization.
func TestCalendarCursorRoundTripsEveryField(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("cal-cursor-" + name)
		now := time.Now().UTC().Truncate(storedTimePrecision)

		want := &CalendarSyncState{
			OwnerID: "user-1", SyncToken: "CAESBhIEChIKEA==",
			BackfillPageToken: "page-token-xyz", BackfillComplete: true,
			SyncingUntil: now.Add(time.Minute), LastSyncedAt: now,
			LastEventAt:   now.Add(48 * time.Hour),
			EventsIndexed: 31, ActivitiesCreated: 7, LastError: "sync_token_expired",
		}
		if _, err := s.SaveCalendarSyncState(ctx, scope, want); err != nil {
			t.Fatalf("save: %v", err)
		}
		got, err := s.GetCalendarSyncState(ctx, scope, "user-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.SyncToken != want.SyncToken {
			t.Errorf("SyncToken = %q, want %q — incremental sync would restart", got.SyncToken, want.SyncToken)
		}
		if got.BackfillPageToken != want.BackfillPageToken || got.BackfillComplete != want.BackfillComplete {
			t.Errorf("backfill state = %q/%v, want %q/%v",
				got.BackfillPageToken, got.BackfillComplete, want.BackfillPageToken, want.BackfillComplete)
		}
		if !got.LastEventAt.Equal(want.LastEventAt) {
			t.Errorf("LastEventAt = %v, want %v", got.LastEventAt, want.LastEventAt)
		}
		if !got.SyncingUntil.Equal(want.SyncingUntil) || !got.LastSyncedAt.Equal(want.LastSyncedAt) {
			t.Errorf("lease/sync times = %v/%v, want %v/%v",
				got.SyncingUntil, got.LastSyncedAt, want.SyncingUntil, want.LastSyncedAt)
		}
		if got.EventsIndexed != want.EventsIndexed || got.ActivitiesCreated != want.ActivitiesCreated {
			t.Errorf("counters = %d/%d, want %d/%d",
				got.EventsIndexed, got.ActivitiesCreated, want.EventsIndexed, want.ActivitiesCreated)
		}
		if got.LastError != want.LastError {
			t.Errorf("LastError = %q, want %q", got.LastError, want.LastError)
		}
	})
}

// TestCalendarDisconnectKeepsTheMirror, for the same reason Gmail keeps its
// index: without it a reconnect reposts every meeting.
func TestCalendarDisconnectKeepsTheMirror(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := calendarScope("cal-purge-" + name)
		contact := calendarFixture(t, s, scope)

		event := calendarEvent("user-1", "evt-purge", contact, time.Now().UTC().Add(time.Hour))
		if _, err := s.RecordCalendarEvent(ctx, scope, event, activityForEvent(event)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := s.SaveCalendarSyncState(ctx, scope, &CalendarSyncState{
			OwnerID: "user-1", SyncToken: "tok", BackfillComplete: true,
		}); err != nil {
			t.Fatalf("seed state: %v", err)
		}

		removed, err := s.PurgeCalendarDataForOwner(ctx, scope, "user-1", false)
		if err != nil {
			t.Fatalf("stop sync: %v", err)
		}
		if removed != 0 {
			t.Errorf("stopping sync removed %d mirror rows, want 0", removed)
		}
		if _, err := s.GetCalendarSyncState(ctx, scope, "user-1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("cursor survived: %v", err)
		}
		events, err := s.ListCalendarEventsForSubject(ctx, scope, SubjectContact, contact.ID, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("mirror holds %d rows after a disconnect, want 1", len(events))
		}

		removed, err = s.PurgeCalendarDataForOwner(ctx, scope, "user-1", true)
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if removed != 1 {
			t.Errorf("purged %d mirror rows, want 1", removed)
		}
	})
}

// TestCalendarStoreRejectsAnEmptyScope.
func TestCalendarStoreRejectsAnEmptyScope(t *testing.T) {
	eachCalendarStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		var none Scope
		now := time.Now().UTC()

		if _, err := s.GetCalendarSyncState(ctx, none, "u"); !errors.Is(err, ErrNoTenant) {
			t.Errorf("GetCalendarSyncState: %v, want ErrNoTenant", err)
		}
		if _, err := s.SaveCalendarSyncState(ctx, none, &CalendarSyncState{OwnerID: "u"}); !errors.Is(err, ErrNoTenant) {
			t.Errorf("SaveCalendarSyncState: %v, want ErrNoTenant", err)
		}
		if _, err := s.ClaimCalendarSync(ctx, none, "u", now, time.Minute); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ClaimCalendarSync: %v, want ErrNoTenant", err)
		}
		if _, err := s.RecordCalendarEvent(ctx, none, &CalendarEvent{},
			func(CalendarChange) *Activity { return nil }); !errors.Is(err, ErrNoTenant) {
			t.Errorf("RecordCalendarEvent: %v, want ErrNoTenant", err)
		}
		if _, err := s.ListCalendarEventsForSubject(ctx, none, SubjectContact, "c", 0); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListCalendarEventsForSubject: %v, want ErrNoTenant", err)
		}
		if _, err := s.PurgeCalendarDataForOwner(ctx, none, "u", true); !errors.Is(err, ErrNoTenant) {
			t.Errorf("PurgeCalendarDataForOwner: %v, want ErrNoTenant", err)
		}
	})
}

// TestClassifyCalendarChangeStatesThePolicyDirectly, rather than leaving it
// to be inferred from the store tests above.
func TestClassifyCalendarChangeStatesThePolicyDirectly(t *testing.T) {
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	confirmed := &CalendarEvent{Status: CalendarStatusConfirmed, StartsAt: start}
	moved := &CalendarEvent{Status: CalendarStatusConfirmed, StartsAt: start.Add(time.Hour)}
	cancelled := &CalendarEvent{Status: CalendarStatusCancelled, StartsAt: start}

	cases := []struct {
		name     string
		previous string
		next     *CalendarEvent
		want     CalendarChange
	}{
		{"first sighting", "", confirmed, CalendarScheduled},
		{"same state again", confirmed.MaterialStateKey(), confirmed, CalendarUnchanged},
		{"start moved", confirmed.MaterialStateKey(), moved, CalendarRescheduled},
		{"cancelled", confirmed.MaterialStateKey(), cancelled, CalendarCancelled},
		{"cancelled again", cancelled.MaterialStateKey(), cancelled, CalendarUnchanged},
		// A cancelled event that reappears at a new time is a reinstatement,
		// which reads as a reschedule — the customer needs to know it is on
		// again and when.
		{"reinstated", cancelled.MaterialStateKey(), moved, CalendarRescheduled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyCalendarChange(tc.previous, tc.next); got != tc.want {
				t.Errorf("ClassifyCalendarChange = %v, want %v", got, tc.want)
			}
		})
	}

	// The title is deliberately not material: renaming "Call" to "Discovery
	// call" must not look to a reader exactly like a rescheduled meeting.
	titled := &CalendarEvent{Status: CalendarStatusConfirmed, StartsAt: start, Title: "Discovery call"}
	if ClassifyCalendarChange(confirmed.MaterialStateKey(), titled) != CalendarUnchanged {
		t.Error("a retitled meeting was classified as a change")
	}
}
