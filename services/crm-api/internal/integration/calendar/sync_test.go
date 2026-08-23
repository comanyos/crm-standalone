package calendar

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

// The calendar sync engine, against a stub Google.
//
// The privacy test comes first, as it does for Gmail. After that the tests
// are about mutability, which is what makes calendars harder than mail.

type stubAPI struct {
	pages    []Page
	listErr  error
	zone     string
	zoneErr  error
	optsSeen []ListOptions
	zoneCall int
}

func (s *stubAPI) List(_ context.Context, _ string, opts ListOptions) (Page, error) {
	s.optsSeen = append(s.optsSeen, opts)
	if s.listErr != nil {
		return Page{}, s.listErr
	}
	index := 0
	if opts.PageToken != "" {
		for i, page := range s.pages {
			if page.NextPageToken == opts.PageToken {
				index = i + 1
				break
			}
		}
	}
	if index >= len(s.pages) {
		return Page{NextSyncToken: "tok-end"}, nil
	}
	return s.pages[index], nil
}

func (s *stubAPI) CalendarTimeZone(context.Context, string, string) (string, error) {
	s.zoneCall++
	if s.zoneErr != nil {
		return "", s.zoneErr
	}
	if s.zone == "" {
		return "Europe/London", nil
	}
	return s.zone, nil
}

type staticToken struct{ err error }

func (t staticToken) AccessToken(context.Context, domain.Scope, string) (string, error) {
	if t.err != nil {
		return "", t.err
	}
	return "access-token", nil
}

const owner = "user-1"
const ownerAddress = "rep@ourcompany.example"

func testScope() domain.Scope { return domain.Scope{TenantID: "tenant-1", OrgID: "org-1"} }

func syncFixture(t *testing.T) (domain.Store, *domain.Contact) {
	t.Helper()
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := testScope()

	if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID: owner, GoogleAccountEmail: ownerAddress,
		Status:          domain.GoogleStatusConnected,
		GrantedScopes:   []string{"https://www.googleapis.com/auth/calendar.events"},
		TokenCiphertext: []byte("sealed"),
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	account, err := store.CreateAccount(ctx, scope, &domain.Account{Name: "Acme", OwnerID: "o"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := store.CreateContact(ctx, scope, &domain.Contact{
		Email: "ada@acme.example", AccountID: account.ID, FirstName: "Ada", LastName: "A",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return store, contact
}

func event(id, title, attendee string, start time.Time) Event {
	return Event{
		ID: id, ICalUID: id + "@google.com", Status: "confirmed", Title: title,
		Organizer: ownerAddress,
		Attendees: []Attendee{
			{Email: ownerAddress, Self: true, Organizer: true},
			{Email: attendee, Response: "accepted"},
		},
		StartsAt: start.UTC(), EndsAt: start.UTC().Add(time.Hour), TimeZone: "Europe/London",
	}
}

// TestOnlyEventsInvolvingAContactAreRecorded is the load-bearing test, for
// the same reason as its Gmail twin. A calendar names who somebody met and
// when — a job interview, a hospital appointment — and every Activity is
// readable by colleagues.
func TestOnlyEventsInvolvingAContactAreRecorded(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	start := time.Now().UTC().Add(24 * time.Hour)

	api := &stubAPI{pages: []Page{{Events: []Event{
		event("e-crm", "Quarterly review", contact.Email, start),
		event("e-doctor", "Cardiology follow-up", "clinic@hospital.example", start),
		event("e-interview", "Interview with a competitor", "hr@rival.example", start),
	}, NextSyncToken: "tok-1"}}}

	result, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.EventsExamined != 3 {
		t.Errorf("examined %d events, want 3", result.EventsExamined)
	}
	if result.EventsMatched != 1 || result.EntriesCreated != 1 {
		t.Errorf("matched %d / created %d, want 1 / 1", result.EventsMatched, result.EntriesCreated)
	}

	events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Title != "Quarterly review" {
		t.Fatalf("stored events = %+v", events)
	}

	activities, _, err := store.ListActivities(ctx, scope, domain.Page{Size: 500})
	if err != nil {
		t.Fatalf("list activities: %v", err)
	}
	for _, activity := range activities {
		if strings.Contains(activity.Notes, "Cardiology") || strings.Contains(activity.Notes, "competitor") {
			t.Fatalf("an event with no CRM counterparty reached the timeline: %q", activity.Notes)
		}
	}
	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.EventsIndexed != 1 {
		t.Errorf("indexed counter = %d, want 1 — unmatched events must leave no trace",
			state.EventsIndexed)
	}
}

// TestARescheduledMeetingReachesTheTimelineOnce is the conflict policy from
// the sync side.
func TestARescheduledMeetingReachesTheTimelineOnce(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	api := &stubAPI{pages: []Page{{
		Events: []Event{event("e-1", "Quarterly review", contact.Email, start)}, NextSyncToken: "tok-1",
	}}}
	syncer := NewSyncer(store, api, staticToken{}, nil)
	if _, err := syncer.SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Google reports it again unchanged, then moved, then moved again.
	api.pages = []Page{{
		Events: []Event{event("e-1", "Quarterly review", contact.Email, start)}, NextSyncToken: "tok-2",
	}}
	second, err := syncer.SyncCalendar(ctx, scope, owner)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.EntriesCreated != 0 {
		t.Errorf("an unchanged event posted %d entries", second.EntriesCreated)
	}

	moved := event("e-1", "Quarterly review", contact.Email, start.Add(3*time.Hour))
	api.pages = []Page{{Events: []Event{moved}, NextSyncToken: "tok-3"}}
	third, err := syncer.SyncCalendar(ctx, scope, owner)
	if err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if third.EntriesCreated != 1 {
		t.Errorf("a moved meeting posted %d entries, want 1", third.EntriesCreated)
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("timeline holds %d entries, want 2", len(activities))
	}
	var sawScheduled, sawRescheduled bool
	for _, activity := range activities {
		if strings.HasPrefix(activity.Notes, "Meeting scheduled") {
			sawScheduled = true
		}
		if strings.HasPrefix(activity.Notes, "Meeting rescheduled") {
			sawRescheduled = true
		}
		if activity.Kind != domain.CalendarActivityKind {
			t.Errorf("activity kind = %q, want %q", activity.Kind, domain.CalendarActivityKind)
		}
	}
	if !sawScheduled || !sawRescheduled {
		t.Errorf("the timeline does not say what happened: %+v", activities)
	}
}

// TestACancelledMeetingReachesTheTimeline. Google keeps returning cancelled
// events, so this must post once and then stop.
func TestACancelledMeetingReachesTheTimeline(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	start := time.Now().UTC().Add(24 * time.Hour)

	api := &stubAPI{pages: []Page{{
		Events: []Event{event("e-1", "Quarterly review", contact.Email, start)}, NextSyncToken: "t1",
	}}}
	syncer := NewSyncer(store, api, staticToken{}, nil)
	if _, err := syncer.SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	cancelled := event("e-1", "Quarterly review", contact.Email, start)
	cancelled.Status = "cancelled"
	for pass := 0; pass < 3; pass++ {
		api.pages = []Page{{Events: []Event{cancelled}, NextSyncToken: "t2"}}
		result, err := syncer.SyncCalendar(ctx, scope, owner)
		if err != nil {
			t.Fatalf("cancel pass %d: %v", pass, err)
		}
		want := 0
		if pass == 0 {
			want = 1
		}
		if result.EntriesCreated != want {
			t.Errorf("cancel pass %d created %d entries, want %d", pass, result.EntriesCreated, want)
		}
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(activities) != 2 {
		t.Fatalf("timeline holds %d entries, want 2", len(activities))
	}
	found := false
	for _, activity := range activities {
		if strings.HasPrefix(activity.Notes, "Meeting cancelled") {
			found = true
		}
	}
	if !found {
		t.Error("the cancellation never reached the timeline")
	}

	// And the mirror reflects Google's current state.
	events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if events[0].Status != domain.CalendarStatusCancelled {
		t.Errorf("mirror status = %q, want cancelled", events[0].Status)
	}
}

// TestAllDayEventsAreNotShiftedAcrossMidnight is the timezone test that
// matters. An all-day event on 1 August in a +05:30 calendar is 31 July in
// UTC, and a timeline that says 31 July tells the customer the wrong day.
func TestAllDayEventsAreNotShiftedAcrossMidnight(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	allDay := Event{
		ID: "e-allday", Status: "confirmed", Title: "Customer on site",
		Organizer: ownerAddress,
		Attendees: []Attendee{
			{Email: ownerAddress, Self: true},
			{Email: contact.Email},
		},
		StartsAt: time.Date(2026, 8, 1, 0, 0, 0, 0, kolkata),
		AllDay:   true, TimeZone: "Asia/Kolkata",
	}
	api := &stubAPI{pages: []Page{{Events: []Event{allDay}, NextSyncToken: "t1"}}}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}

	events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if events[0].StartsAt.UTC().Day() != 31 {
		t.Fatal("the fixture is wrong: this instant should be 31 July in UTC, " +
			"or the test proves nothing")
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	note := activities[0].Notes
	if !strings.Contains(note, "1 Aug 2026") {
		t.Errorf("timeline says %q; an all-day event must read as the day it is in its own zone", note)
	}
	if !strings.Contains(note, "all day") {
		t.Errorf("timeline says %q; an all-day event should not imply a time", note)
	}
	if strings.Contains(note, "31 Jul") {
		t.Errorf("the all-day event was shifted across midnight: %q", note)
	}
}

// TestTimedEventsRenderInTheirOwnZone. A meeting at 09:00 in London is not
// 04:00 to the customer who attended it.
func TestTimedEventsRenderInTheirOwnZone(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	timed := event("e-timed", "Discovery call", contact.Email,
		time.Date(2026, 8, 3, 9, 0, 0, 0, london))
	api := &stubAPI{pages: []Page{{Events: []Event{timed}, NextSyncToken: "t1"}}}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}

	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if !strings.Contains(activities[0].Notes, "09:00 Europe/London") {
		t.Errorf("timeline says %q, want the time in the event's own zone", activities[0].Notes)
	}
}

// TestAnEventWithNoZoneFallsBackToTheCalendarsOwn. Google often omits the
// zone on all-day events.
func TestAnEventWithNoZoneFallsBackToTheCalendarsOwn(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	zoneless := event("e-nozone", "Check-in", contact.Email, time.Now().UTC().Add(time.Hour))
	zoneless.TimeZone = ""
	api := &stubAPI{zone: "America/New_York", pages: []Page{{Events: []Event{zoneless}, NextSyncToken: "t1"}}}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if api.zoneCall == 0 {
		t.Error("the calendar's default zone was never read")
	}

	events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if events[0].TimeZone != "America/New_York" {
		t.Errorf("stored zone = %q, want the calendar's own", events[0].TimeZone)
	}
}

// TestTheOwnersOwnAttendanceDoesNotMatch. Google marks the connected
// account's attendee row with self, which is more reliable than comparing
// addresses when aliases are involved.
func TestTheOwnersOwnAttendanceDoesNotMatch(t *testing.T) {
	// The organizer here is an alias the connection has never heard of.
	// Google marks it as self, and that is the only signal that says so.
	e := Event{
		Organizer: "alias@ourcompany.example",
		Attendees: []Attendee{
			{Email: "alias@ourcompany.example", Self: true},
			{Email: "ada@acme.example"},
			{Email: "ADA@ACME.EXAMPLE"},
		},
	}
	got := e.Participants(ownerAddress)
	if len(got) != 1 || got[0] != "ada@acme.example" {
		t.Errorf("participants = %v, want just the normalized contact — self and duplicates dropped", got)
	}

	// And when Google marks only the organizer field, with no attendee row
	// for the account at all — which is what a meeting organized by a
	// delegated account looks like.
	delegated := Event{
		Organizer: "assistant@ourcompany.example", OrganizerIsSelf: true,
		Attendees: []Attendee{{Email: "ada@acme.example"}},
	}
	got = delegated.Participants(ownerAddress)
	if len(got) != 1 || got[0] != "ada@acme.example" {
		t.Errorf("participants = %v; a delegated organizer address was treated as a contact", got)
	}
}

// TestAnExpiredSyncTokenRestartsTheWindow.
func TestAnExpiredSyncTokenRestartsTheWindow(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	if _, err := store.SaveCalendarSyncState(ctx, scope, &domain.CalendarSyncState{
		OwnerID: owner, SyncToken: "stale", BackfillComplete: true,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	api := &stubAPI{listErr: ErrSyncTokenExpired}
	_, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner)
	if !errors.Is(err, ErrSyncTokenExpired) {
		t.Fatalf("sync: %v, want ErrSyncTokenExpired", err)
	}

	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.SyncToken != "" || state.BackfillComplete {
		t.Error("an expired token left the calendar believing it was up to date")
	}
	if state.LastError != ReasonSyncTokenExpired {
		t.Errorf("recorded reason = %q, want %q", state.LastError, ReasonSyncTokenExpired)
	}
	if !state.SyncingUntil.IsZero() {
		t.Error("a failed pass kept its lease")
	}
}

// TestABackfillUsesAWindowAndAnIncrementalPassUsesTheToken — the two request
// shapes must stay separate, because Google rejects a sync token combined
// with a time window.
func TestABackfillUsesAWindowAndAnIncrementalPassUsesTheToken(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	api := &stubAPI{pages: []Page{{
		Events:        []Event{event("e-1", "Review", contact.Email, time.Now().UTC().Add(time.Hour))},
		NextSyncToken: "tok-after-backfill",
	}}}
	syncer := NewSyncer(store, api, staticToken{}, nil)
	if _, err := syncer.SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	first := api.optsSeen[0]
	if first.SyncToken != "" {
		t.Error("the first pass sent a sync token it could not have had")
	}
	if first.TimeMin.IsZero() || first.TimeMax.IsZero() {
		t.Error("the backfill was unbounded; it would walk the whole calendar history")
	}
	if !first.TimeMax.After(first.TimeMin) {
		t.Errorf("backfill window is inverted: %v to %v", first.TimeMin, first.TimeMax)
	}

	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.SyncToken != "tok-after-backfill" || !state.BackfillComplete {
		t.Fatalf("state after backfill = %+v", state)
	}

	api.optsSeen = nil
	api.pages = []Page{{NextSyncToken: "tok-2"}}
	if _, err := syncer.SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("incremental: %v", err)
	}
	second := api.optsSeen[0]
	if second.SyncToken != "tok-after-backfill" {
		t.Errorf("the incremental pass sent token %q, want the stored one", second.SyncToken)
	}
	if !second.TimeMin.IsZero() || !second.TimeMax.IsZero() {
		t.Error("a sync token was combined with a time window, which Google rejects")
	}
}

// TestASyncTokenIsStoredOnlyAfterTheLastPage. Google sends nextSyncToken on
// the final page alone; storing anything earlier skips the rest.
func TestASyncTokenIsStoredOnlyAfterTheLastPage(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	start := time.Now().UTC().Add(time.Hour)

	api := &stubAPI{pages: []Page{
		{Events: []Event{event("e-1", "One", contact.Email, start)}, NextPageToken: "p2"},
		{Events: []Event{event("e-2", "Two", contact.Email, start)}, NextSyncToken: "tok-final"},
	}}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}

	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.SyncToken != "tok-final" {
		t.Errorf("stored token = %q, want the one from the last page", state.SyncToken)
	}
	events, err := store.ListCalendarEventsForSubject(ctx, scope, domain.SubjectContact, contact.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("stored %d events across two pages, want 2", len(events))
	}
}

// TestPartialConsentIsReportedNotRetried: a user may grant Gmail and decline
// Calendar.
func TestPartialConsentIsReportedNotRetried(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	connection, err := store.GetGoogleConnection(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.GrantedScopes = []string{"https://www.googleapis.com/auth/gmail.readonly"}
	if _, err := store.SaveGoogleConnection(ctx, scope, connection); err != nil {
		t.Fatalf("narrow consent: %v", err)
	}

	api := &stubAPI{}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err == nil {
		t.Fatal("sync ran without calendar consent")
	}
	if len(api.optsSeen) != 0 {
		t.Error("sync called Google despite not holding calendar consent")
	}
	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastError != ReasonNotConnected {
		t.Errorf("recorded reason = %q, want %q", state.LastError, ReasonNotConnected)
	}
}

// TestFailuresRecordABoundedReason.
func TestFailuresRecordABoundedReason(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	const leak = "quota exceeded for calendar rep@ourcompany.example"
	_, err := NewSyncer(store, &stubAPI{}, staticToken{err: errors.New(leak)}, nil).
		SyncCalendar(ctx, scope, owner)
	if err == nil {
		t.Fatal("sync succeeded with no usable token")
	}
	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.LastError != ReasonTokenUnavailable {
		t.Errorf("recorded reason = %q, want %q", state.LastError, ReasonTokenUnavailable)
	}
	if strings.Contains(state.LastError, "quota") || strings.Contains(state.LastError, "@") {
		t.Errorf("the stored reason carries provider detail: %q", state.LastError)
	}
}

// TestALeasedCalendarIsNotSyncedTwice.
func TestALeasedCalendarIsNotSyncedTwice(t *testing.T) {
	store, _ := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	if _, err := store.ClaimCalendarSync(ctx, scope, owner, time.Now().UTC(), time.Hour); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}
	api := &stubAPI{}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("sync of a leased calendar: %v, want ErrConflict", err)
	}
	if len(api.optsSeen) != 0 {
		t.Error("a second replica walked a calendar another was already syncing")
	}
}

// TestATitlelessEventStillReadsSensibly.
func TestATitlelessEventStillReadsSensibly(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()

	untitled := event("e-untitled", "   ", contact.Email, time.Now().UTC().Add(time.Hour))
	api := &stubAPI{pages: []Page{{Events: []Event{untitled}, NextSyncToken: "t1"}}}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if !strings.Contains(activities[0].Notes, "(no title)") {
		t.Errorf("timeline entry for an untitled meeting = %q", activities[0].Notes)
	}
}

// TestTheActivityIsStampedWithTheMeetingNotTheSync. A timeline ordered by
// discovery time puts next week's rescheduled meeting before last week's
// completed one.
func TestTheActivityIsStampedWithTheMeetingNotTheSync(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	meetingAt := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	api := &stubAPI{pages: []Page{{
		Events: []Event{event("e-1", "Future review", contact.Email, meetingAt)}, NextSyncToken: "t1",
	}}}
	if _, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner); err != nil {
		t.Fatalf("sync: %v", err)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if !activities[0].OccurredAt.Equal(meetingAt) {
		t.Errorf("activity occurred_at = %v, want the meeting time %v",
			activities[0].OccurredAt, meetingAt)
	}
}

// TestOnePassIsBounded so one busy calendar cannot starve the others.
func TestOnePassIsBounded(t *testing.T) {
	store, contact := syncFixture(t)
	ctx := context.Background()
	scope := testScope()
	start := time.Now().UTC().Add(time.Hour)

	var pages []Page
	for p := 0; p < 3; p++ {
		var events []Event
		for i := 0; i < 200; i++ {
			events = append(events, event(
				"e-"+strconv.Itoa(p*1000+i), "Noise", "stranger@nowhere.example", start))
		}
		page := Page{Events: events}
		if p < 2 {
			page.NextPageToken = "p" + strconv.Itoa(p+1)
		} else {
			page.NextSyncToken = "tok"
		}
		pages = append(pages, page)
	}
	api := &stubAPI{pages: pages}

	result, err := NewSyncer(store, api, staticToken{}, nil).SyncCalendar(ctx, scope, owner)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.EventsExamined > eventsPerPass {
		t.Errorf("one pass examined %d events, want at most %d", result.EventsExamined, eventsPerPass)
	}
	state, err := store.GetCalendarSyncState(ctx, scope, owner)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.BackfillComplete {
		t.Error("a bounded pass claimed the backfill was finished")
	}
	if state.BackfillPageToken == "" {
		t.Error("a bounded pass saved no resume point")
	}
	_ = contact
}
