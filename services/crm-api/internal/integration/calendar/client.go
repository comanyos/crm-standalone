// Package calendar reads Google Calendar and turns matching events into CRM
// timeline entries.
//
// As with Gmail, what is deliberately not fetched matters as much as what is.
// Every list asks for a fixed field mask that excludes the description and
// the location, so neither crosses the network — a calendar holds medical
// appointments and job interviews as readily as customer meetings, and a
// field that never arrives cannot be stored by a later change that adds a
// column without also widening this request.
package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL          = "https://www.googleapis.com/calendar/v3"
	maxResponseBytes = 4 << 20

	// fieldMask is the allowlist. Adding `items(description)` or
	// `items(location)` here is the change that would make this package start
	// handling private content, which is exactly the review it deserves.
	fieldMask = "nextPageToken,nextSyncToken,items(id,etag,iCalUID,status,summary,start,end," +
		"organizer(email,self),attendees(email,self,organizer,responseStatus)," +
		"recurringEventId,updated)"
)

// ErrSyncTokenExpired means Google has discarded the incremental cursor, which
// it answers with HTTP 410. The only correct response is to re-list the
// window: resuming from an invalid token would silently skip everything that
// changed while it was invalid.
var ErrSyncTokenExpired = errors.New("calendar: sync token has expired")

// Client is a minimal Google Calendar client. Like the Gmail one it holds no
// credential — tokens belong to users and are refreshed independently.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// New builds a client. A nil httpClient gets a bounded default.
func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// Attendee is one participant on an event.
type Attendee struct {
	Email string
	// Self marks the connected account's own row, which is how the mailbox
	// owner is excluded from matching without comparing addresses.
	Self      bool
	Organizer bool
	// Response is Google's responseStatus: needsAction, declined, tentative,
	// accepted. Stored nowhere yet; carried so matching can ignore declined
	// attendees later without another round trip.
	Response string
}

// Event is one calendar event's envelope. There is deliberately no
// description and no location field: nothing can put them somewhere.
type Event struct {
	ID               string
	ETag             string
	ICalUID          string
	RecurringEventID string
	Status           string
	Title            string
	Organizer        string
	// OrganizerIsSelf is Google's own answer to "did this account organize
	// it". It is needed separately from the attendee rows because an account
	// that organized a meeting is not always listed as an attendee of it.
	OrganizerIsSelf bool
	Attendees       []Attendee
	Description     string
	Location        string
	Recurrence      []string
	ReminderMinutes []int

	// StartsAt and EndsAt are absolute instants. For an all-day event they
	// are local midnight in TimeZone, which is why the zone travels with
	// them: the instant alone cannot say which calendar day this was.
	StartsAt time.Time
	EndsAt   time.Time
	AllDay   bool
	TimeZone string
}

// Participants returns every attendee address plus the organizer, normalized,
// deduplicated, with every address belonging to the connected account
// removed.
//
// "Belonging to the connected account" is deliberately more than one address.
// Google marks the account's own rows with self, and that is the answer to
// trust: a user with aliases, a delegated mailbox, or a Google Group
// membership organizes meetings under addresses the connection has never
// heard of. Comparing against the single configured address misses those, and
// the failure is quiet — the account matches itself as a Contact, and every
// meeting it attends lands on its own timeline.
//
// The self addresses are collected in a first pass because the organizer is
// read before the attendee list, and an alias is only revealed by the
// attendee row that carries it.
func (e Event) Participants(selfAddress string) []string {
	normalize := func(address string) string {
		return strings.ToLower(strings.TrimSpace(address))
	}

	self := map[string]bool{}
	if address := normalize(selfAddress); address != "" {
		self[address] = true
	}
	if e.OrganizerIsSelf {
		if address := normalize(e.Organizer); address != "" {
			self[address] = true
		}
	}
	for _, attendee := range e.Attendees {
		if attendee.Self {
			if address := normalize(attendee.Email); address != "" {
				self[address] = true
			}
		}
	}

	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	add := func(raw string) {
		address := normalize(raw)
		if address == "" || self[address] || seen[address] {
			return
		}
		seen[address] = true
		out = append(out, address)
	}
	add(e.Organizer)
	for _, attendee := range e.Attendees {
		add(attendee.Email)
	}
	return out
}

// Page is one page of events.
type Page struct {
	Events        []Event
	NextPageToken string
	// NextSyncToken is present only on the final page of a walk. Storing it
	// before then would skip whatever the remaining pages held.
	NextSyncToken string
}

// ListOptions selects what a call asks for. syncToken and the time window are
// mutually exclusive at Google: a request carrying a sync token must not also
// constrain the window, or Google rejects it.
type ListOptions struct {
	CalendarID string
	SyncToken  string
	PageToken  string
	TimeMin    time.Time
	TimeMax    time.Time
	PageSize   int
}

// List reads one page of events.
//
// singleEvents=true expands recurring series into their instances, which is
// what a CRM timeline wants: "the weekly check-in on 4 August" is a thing
// that happened, and the abstract series is not.
func (c *Client) List(ctx context.Context, accessToken string, opts ListOptions) (Page, error) {
	calendarID := opts.CalendarID
	if calendarID == "" {
		calendarID = "primary"
	}
	params := url.Values{
		"singleEvents": {"true"},
		"showDeleted":  {"true"}, // cancellations must reach the timeline
		"fields":       {fieldMask},
	}
	if opts.PageSize > 0 {
		params.Set("maxResults", strconv.Itoa(opts.PageSize))
	}
	if opts.PageToken != "" {
		params.Set("pageToken", opts.PageToken)
	}
	if opts.SyncToken != "" {
		// Google rejects a sync token combined with a time window or an
		// ordering, so the two request shapes stay strictly separate.
		params.Set("syncToken", opts.SyncToken)
	} else {
		params.Set("orderBy", "startTime")
		if !opts.TimeMin.IsZero() {
			params.Set("timeMin", opts.TimeMin.UTC().Format(time.RFC3339))
		}
		if !opts.TimeMax.IsZero() {
			params.Set("timeMax", opts.TimeMax.UTC().Format(time.RFC3339))
		}
	}

	var wire struct {
		NextPageToken string      `json:"nextPageToken"`
		NextSyncToken string      `json:"nextSyncToken"`
		Items         []wireEvent `json:"items"`
	}
	path := "/calendars/" + url.PathEscape(calendarID) + "/events"
	if err := c.get(ctx, accessToken, path, params, &wire); err != nil {
		return Page{}, err
	}

	page := Page{NextPageToken: wire.NextPageToken, NextSyncToken: wire.NextSyncToken}
	for _, item := range wire.Items {
		event, err := item.toEvent()
		if err != nil {
			// One unparseable event must not fail a whole page. It is skipped
			// and the walk continues; a calendar containing one strange row
			// should still sync the other two hundred.
			continue
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}

type wireTime struct {
	// DateTime is RFC3339 with an offset, for a timed event.
	DateTime string `json:"dateTime"`
	// Date is "2006-01-02", for an all-day event. Google sends one or the
	// other, never both.
	Date string `json:"date"`
	// TimeZone is the IANA name. It is present for timed events and often
	// absent for all-day ones, which is why the calendar's own default is
	// needed as a fallback.
	TimeZone string `json:"timeZone"`
}

type wireEvent struct {
	ID               string   `json:"id"`
	ETag             string   `json:"etag"`
	ICalUID          string   `json:"iCalUID"`
	Status           string   `json:"status"`
	Summary          string   `json:"summary"`
	RecurringEventID string   `json:"recurringEventId"`
	Start            wireTime `json:"start"`
	End              wireTime `json:"end"`
	Organizer        struct {
		Email string `json:"email"`
		Self  bool   `json:"self"`
	} `json:"organizer"`
	Attendees []struct {
		Email          string `json:"email"`
		Self           bool   `json:"self"`
		Organizer      bool   `json:"organizer"`
		ResponseStatus string `json:"responseStatus"`
	} `json:"attendees"`
}

func (w wireEvent) toEvent() (Event, error) {
	start, allDay, zone, err := w.Start.parse()
	if err != nil {
		return Event{}, err
	}
	end, _, _, endErr := w.End.parse()
	if endErr != nil {
		// A missing or malformed end is survivable; a missing start is not.
		end = time.Time{}
	}

	event := Event{
		ID:               w.ID,
		ETag:             w.ETag,
		ICalUID:          w.ICalUID,
		RecurringEventID: w.RecurringEventID,
		Status:           w.Status,
		Title:            w.Summary,
		Organizer:        w.Organizer.Email,
		OrganizerIsSelf:  w.Organizer.Self,
		StartsAt:         start,
		EndsAt:           end,
		AllDay:           allDay,
		TimeZone:         zone,
	}
	for _, attendee := range w.Attendees {
		event.Attendees = append(event.Attendees, Attendee{
			Email: attendee.Email, Self: attendee.Self,
			Organizer: attendee.Organizer, Response: attendee.ResponseStatus,
		})
	}
	return event, nil
}

// parse resolves one endpoint into an instant, whether it is a timestamp or
// a date.
//
// The all-day case is the one worth care. Google sends `date: "2026-08-01"`
// with no time and often no zone, meaning "the whole of 1 August wherever
// this calendar lives". Parsing that as UTC would put it at 2026-08-01T00:00Z,
// which for a calendar in Asia/Kolkata is 5.5 hours into the day and for one
// in America/Los_Angeles is the evening before — so the day boundary lands
// wrong for exactly the users who notice. Parsing it in the event's own zone
// keeps the instant honest, and carrying the zone lets it be rendered back as
// 1 August for everyone.
func (w wireTime) parse() (instant time.Time, allDay bool, zone string, err error) {
	if w.DateTime != "" {
		parsed, err := time.Parse(time.RFC3339, w.DateTime)
		if err != nil {
			return time.Time{}, false, "", fmt.Errorf("calendar: parse dateTime: %w", err)
		}
		return parsed.UTC(), false, w.TimeZone, nil
	}
	if w.Date == "" {
		return time.Time{}, false, "", errors.New("calendar: event endpoint has neither dateTime nor date")
	}

	location := time.UTC
	name := w.TimeZone
	if name != "" {
		if loaded, err := time.LoadLocation(name); err == nil {
			location = loaded
		} else {
			// An unrecognized zone name is kept as UTC rather than guessed
			// at; the stored zone then says UTC, which is at least true.
			name = "UTC"
		}
	} else {
		name = "UTC"
	}
	parsed, err := time.ParseInLocation("2006-01-02", w.Date, location)
	if err != nil {
		return time.Time{}, false, "", fmt.Errorf("calendar: parse date: %w", err)
	}
	return parsed, true, name, nil
}

// CalendarTimeZone reads the calendar's own default zone, used as the
// fallback for all-day events that carry none of their own.
func (c *Client) CalendarTimeZone(ctx context.Context, accessToken, calendarID string) (string, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	var wire struct {
		TimeZone string `json:"timeZone"`
	}
	path := "/calendars/" + url.PathEscape(calendarID)
	if err := c.get(ctx, accessToken, path, url.Values{"fields": {"timeZone"}}, &wire); err != nil {
		return "", err
	}
	return wire.TimeZone, nil
}

func (c *Client) get(ctx context.Context, accessToken, path string, params url.Values, into any) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("calendar: access token is required")
	}
	endpoint := c.baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("calendar: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calendar: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body close is best-effort after a bounded read

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode == http.StatusGone {
			return ErrSyncTokenExpired
		}
		// The body is not included: Google's error bodies echo request
		// parameters, and an error value here reaches a log line.
		return fmt.Errorf("calendar: %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(into); err != nil {
		return fmt.Errorf("calendar: decode %s response: %w", path, err)
	}
	return nil
}
