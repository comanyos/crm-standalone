package calendar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

var (
	ErrEventNotFound        = errors.New("calendar: event not found")
	ErrEventConflict        = errors.New("calendar: event already exists")
	ErrEventChanged         = errors.New("calendar: event changed since it was opened")
	ErrCalendarNotConnected = errors.New("calendar: Google Calendar is not connected")
)

// EventInput is the provider-neutral shape accepted by the CRM calendar.
// StartsAt and EndsAt are absolute instants; TimeZone preserves the wall-clock
// choice used to create them.
type EventInput struct {
	ID              string
	Title           string
	Description     string
	Location        string
	StartsAt        time.Time
	EndsAt          time.Time
	TimeZone        string
	Attendees       []string
	Recurrence      []string
	ReminderMinutes []int
}

func (in *EventInput) Validate() error {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Location = strings.TrimSpace(in.Location)
	in.TimeZone = strings.TrimSpace(in.TimeZone)
	if in.Title == "" || len([]rune(in.Title)) > 500 {
		return fmt.Errorf("%w: event title is required and may contain at most 500 characters", domain.ErrInvalid)
	}
	if len([]rune(in.Description)) > 10000 || len([]rune(in.Location)) > 500 {
		return fmt.Errorf("%w: event description or location is too long", domain.ErrInvalid)
	}
	if in.StartsAt.IsZero() || in.EndsAt.IsZero() || !in.EndsAt.After(in.StartsAt) {
		return fmt.Errorf("%w: event end must be after its start", domain.ErrInvalid)
	}
	if in.TimeZone == "" {
		in.TimeZone = "UTC"
	}
	if _, err := time.LoadLocation(in.TimeZone); err != nil {
		return fmt.Errorf("%w: %q is not an IANA time zone", domain.ErrInvalid, in.TimeZone)
	}
	if len(in.Attendees) > 50 {
		return fmt.Errorf("%w: an event may have at most 50 attendees", domain.ErrInvalid)
	}
	seen := make(map[string]bool, len(in.Attendees))
	clean := make([]string, 0, len(in.Attendees))
	for _, raw := range in.Attendees {
		address := strings.ToLower(strings.TrimSpace(raw))
		parsed, err := mail.ParseAddress(address)
		if err != nil || strings.ToLower(parsed.Address) != address {
			return fmt.Errorf("%w: %q is not a valid attendee email address", domain.ErrInvalid, raw)
		}
		if !seen[address] {
			seen[address] = true
			clean = append(clean, address)
		}
	}
	sort.Strings(clean)
	in.Attendees = clean
	if len(in.Recurrence) > 1 {
		return fmt.Errorf("%w: only one recurrence rule is supported", domain.ErrInvalid)
	}
	for index, rule := range in.Recurrence {
		upper := strings.ToUpper(strings.TrimSpace(rule))
		if !validRecurrence(upper) {
			return fmt.Errorf("%w: unsupported recurrence rule", domain.ErrInvalid)
		}
		in.Recurrence[index] = upper
	}
	if len(in.ReminderMinutes) > 5 {
		return fmt.Errorf("%w: an event may have at most five reminders", domain.ErrInvalid)
	}
	seenReminder := map[int]bool{}
	reminders := make([]int, 0, len(in.ReminderMinutes))
	for _, minutes := range in.ReminderMinutes {
		if minutes < 0 || minutes > 40320 {
			return fmt.Errorf("%w: reminders must be between 0 and 40320 minutes", domain.ErrInvalid)
		}
		if !seenReminder[minutes] {
			seenReminder[minutes] = true
			reminders = append(reminders, minutes)
		}
	}
	sort.Ints(reminders)
	in.ReminderMinutes = reminders
	return nil
}

func validRecurrence(rule string) bool {
	if len(rule) > 256 || strings.ContainsAny(rule, "\r\n") {
		return false
	}
	parts := strings.Split(rule, ";")
	if len(parts) == 0 || len(parts) > 2 {
		return false
	}
	switch parts[0] {
	case "RRULE:FREQ=DAILY", "RRULE:FREQ=WEEKLY", "RRULE:FREQ=MONTHLY", "RRULE:FREQ=YEARLY":
	default:
		return false
	}
	if len(parts) == 1 {
		return true
	}
	if !strings.HasPrefix(parts[1], "COUNT=") {
		return false
	}
	count, err := strconv.Atoi(strings.TrimPrefix(parts[1], "COUNT="))
	return err == nil && count >= 1 && count <= 1000
}

type eventWire struct {
	ID          string   `json:"id,omitempty"`
	ETag        string   `json:"etag,omitempty"`
	ICalUID     string   `json:"iCalUID,omitempty"`
	Status      string   `json:"status,omitempty"`
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	Location    string   `json:"location,omitempty"`
	Start       wireTime `json:"start"`
	End         wireTime `json:"end"`
	Organizer   struct {
		Email string `json:"email"`
		Self  bool   `json:"self"`
	} `json:"organizer,omitempty"`
	Attendees []struct {
		Email          string `json:"email"`
		Self           bool   `json:"self,omitempty"`
		Organizer      bool   `json:"organizer,omitempty"`
		ResponseStatus string `json:"responseStatus,omitempty"`
	} `json:"attendees,omitempty"`
	Recurrence []string `json:"recurrence,omitempty"`
	Reminders  struct {
		UseDefault bool `json:"useDefault"`
		Overrides  []struct {
			Method  string `json:"method"`
			Minutes int    `json:"minutes"`
		} `json:"overrides,omitempty"`
	} `json:"reminders"`
}

func eventWireFromInput(in EventInput) eventWire {
	w := eventWire{ID: in.ID, Summary: in.Title, Description: in.Description, Location: in.Location,
		Start:      wireTime{DateTime: in.StartsAt.Format(time.RFC3339), TimeZone: in.TimeZone},
		End:        wireTime{DateTime: in.EndsAt.Format(time.RFC3339), TimeZone: in.TimeZone},
		Recurrence: append([]string(nil), in.Recurrence...)}
	for _, address := range in.Attendees {
		w.Attendees = append(w.Attendees, struct {
			Email          string `json:"email"`
			Self           bool   `json:"self,omitempty"`
			Organizer      bool   `json:"organizer,omitempty"`
			ResponseStatus string `json:"responseStatus,omitempty"`
		}{Email: address})
	}
	for _, minutes := range in.ReminderMinutes {
		w.Reminders.Overrides = append(w.Reminders.Overrides, struct {
			Method  string `json:"method"`
			Minutes int    `json:"minutes"`
		}{Method: "popup", Minutes: minutes})
	}
	return w
}

func (w eventWire) event() (Event, error) {
	base := wireEvent{ID: w.ID, ETag: w.ETag, ICalUID: w.ICalUID, Status: w.Status,
		Summary: w.Summary, Start: w.Start, End: w.End, Organizer: w.Organizer}
	for _, a := range w.Attendees {
		base.Attendees = append(base.Attendees, struct {
			Email          string `json:"email"`
			Self           bool   `json:"self"`
			Organizer      bool   `json:"organizer"`
			ResponseStatus string `json:"responseStatus"`
		}{a.Email, a.Self, a.Organizer, a.ResponseStatus})
	}
	event, err := base.toEvent()
	if err != nil {
		return Event{}, err
	}
	event.Description, event.Location = w.Description, w.Location
	event.Recurrence = append([]string(nil), w.Recurrence...)
	for _, reminder := range w.Reminders.Overrides {
		if reminder.Method == "popup" || reminder.Method == "email" {
			event.ReminderMinutes = append(event.ReminderMinutes, reminder.Minutes)
		}
	}
	return event, nil
}

func (c *Client) Create(ctx context.Context, accessToken, calendarID string, in EventInput) (Event, error) {
	if err := in.Validate(); err != nil {
		return Event{}, err
	}
	var out eventWire
	err := c.eventRequest(ctx, accessToken, http.MethodPost, calendarID, "", "", in, &out)
	if err != nil {
		return Event{}, err
	}
	return out.event()
}

func (c *Client) Get(ctx context.Context, accessToken, calendarID, eventID string) (Event, error) {
	if calendarID == "" {
		calendarID = "primary"
	}
	var out eventWire
	path := "/calendars/" + url.PathEscape(calendarID) + "/events/" + url.PathEscape(eventID)
	if err := c.request(ctx, accessToken, http.MethodGet, path, nil, "", &out); err != nil {
		return Event{}, err
	}
	return out.event()
}

func (c *Client) Update(ctx context.Context, accessToken, calendarID, eventID, etag string, in EventInput) (Event, error) {
	if strings.TrimSpace(etag) == "" {
		return Event{}, fmt.Errorf("%w: event version is required", domain.ErrInvalid)
	}
	if err := in.Validate(); err != nil {
		return Event{}, err
	}
	var out eventWire
	if err := c.eventRequest(ctx, accessToken, http.MethodPut, calendarID, eventID, etag, in, &out); err != nil {
		return Event{}, err
	}
	return out.event()
}

func (c *Client) Cancel(ctx context.Context, accessToken, calendarID, eventID, etag string) error {
	if strings.TrimSpace(etag) == "" {
		return fmt.Errorf("%w: event version is required", domain.ErrInvalid)
	}
	if calendarID == "" {
		calendarID = "primary"
	}
	path := "/calendars/" + url.PathEscape(calendarID) + "/events/" + url.PathEscape(eventID) + "?sendUpdates=all"
	return c.request(ctx, accessToken, http.MethodDelete, path, nil, etag, nil)
}

func (c *Client) eventRequest(ctx context.Context, accessToken, method, calendarID, eventID, etag string, in EventInput, out any) error {
	if calendarID == "" {
		calendarID = "primary"
	}
	path := "/calendars/" + url.PathEscape(calendarID) + "/events"
	if eventID != "" {
		path += "/" + url.PathEscape(eventID)
	}
	path += "?sendUpdates=all"
	body, err := json.Marshal(eventWireFromInput(in))
	if err != nil {
		return fmt.Errorf("calendar: encode event: %w", err)
	}
	return c.request(ctx, accessToken, method, path, body, etag, out)
}

func (c *Client) request(ctx context.Context, accessToken, method, path string, body []byte, etag string, into any) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("calendar: access token is required")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("calendar: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calendar: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusGone:
			return ErrEventNotFound
		case http.StatusConflict:
			return ErrEventConflict
		case http.StatusPreconditionFailed:
			return ErrEventChanged
		default:
			return fmt.Errorf("calendar: provider returned HTTP %d", resp.StatusCode)
		}
	}
	if into == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(into); err != nil {
		return fmt.Errorf("calendar: decode event response: %w", err)
	}
	return nil
}

// Writer binds Calendar API calls to one tenant and connected owner.
type WriteAPI interface {
	Create(context.Context, string, string, EventInput) (Event, error)
	Get(context.Context, string, string, string) (Event, error)
	Update(context.Context, string, string, string, string, EventInput) (Event, error)
	Cancel(context.Context, string, string, string, string) error
}

type Writer struct {
	store  domain.Store
	api    WriteAPI
	tokens TokenSource
	now    func() time.Time
}

func NewWriter(store domain.Store, api WriteAPI, tokens TokenSource) *Writer {
	return &Writer{store: store, api: api, tokens: tokens, now: func() time.Time { return time.Now().UTC() }}
}

type ManagedEventInput struct {
	RequestID, OwnerID, ActorID, SubjectType, SubjectID, ETag string
	Event                                                     EventInput
}

func (w *Writer) Create(ctx context.Context, scope domain.Scope, in ManagedEventInput) (Event, *domain.CalendarEvent, error) {
	if strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 128 {
		return Event{}, nil, fmt.Errorf("%w: request_id is required", domain.ErrInvalid)
	}
	if err := validateManaged(in); err != nil {
		return Event{}, nil, err
	}
	if err := w.requireSubject(ctx, scope, in.SubjectType, in.SubjectID); err != nil {
		return Event{}, nil, err
	}
	token, err := w.access(ctx, scope, in.OwnerID)
	if err != nil {
		return Event{}, nil, err
	}
	in.Event.ID = providerID(scope.TenantID, in.OwnerID, in.RequestID)
	event, err := w.api.Create(ctx, token, "primary", in.Event)
	if errors.Is(err, ErrEventConflict) {
		event, err = w.api.Get(ctx, token, "primary", in.Event.ID)
		if err == nil && (event.Title != in.Event.Title || !event.StartsAt.Equal(in.Event.StartsAt.UTC()) || !event.EndsAt.Equal(in.Event.EndsAt.UTC())) {
			return Event{}, nil, domain.ErrConflict
		}
	}
	if err != nil {
		return Event{}, nil, err
	}
	row, _, err := w.record(ctx, scope, in, event)
	return event, row, err
}

func (w *Writer) requireSubject(ctx context.Context, scope domain.Scope, subjectType, subjectID string) error {
	switch subjectType {
	case domain.SubjectContact:
		_, err := w.store.GetContact(ctx, scope, subjectID)
		return err
	case domain.SubjectLead:
		_, err := w.store.GetLead(ctx, scope, subjectID)
		return err
	case domain.SubjectOpportunity:
		_, err := w.store.GetOpportunity(ctx, scope, subjectID)
		return err
	default:
		return domain.ErrInvalid
	}
}

func (w *Writer) Get(ctx context.Context, scope domain.Scope, ownerID, providerID string) (Event, *domain.CalendarEvent, error) {
	row, err := w.store.GetManagedCalendarEvent(ctx, scope, ownerID, providerID)
	if err != nil {
		return Event{}, nil, err
	}
	token, err := w.access(ctx, scope, ownerID)
	if err != nil {
		return Event{}, nil, err
	}
	event, err := w.api.Get(ctx, token, row.CalendarID, providerID)
	return event, row, err
}

func (w *Writer) Update(ctx context.Context, scope domain.Scope, in ManagedEventInput) (Event, *domain.CalendarEvent, error) {
	row, err := w.store.GetManagedCalendarEvent(ctx, scope, in.OwnerID, in.Event.ID)
	if err != nil {
		return Event{}, nil, err
	}
	in.SubjectType, in.SubjectID = row.SubjectType, row.SubjectID
	if err := validateManaged(in); err != nil {
		return Event{}, nil, err
	}
	if err := w.requireSubject(ctx, scope, in.SubjectType, in.SubjectID); err != nil {
		return Event{}, nil, err
	}
	token, err := w.access(ctx, scope, in.OwnerID)
	if err != nil {
		return Event{}, nil, err
	}
	event, err := w.api.Update(ctx, token, row.CalendarID, row.ProviderEventID, in.ETag, in.Event)
	if err != nil {
		return Event{}, nil, err
	}
	updated, change, err := w.record(ctx, scope, in, event)
	if err == nil && change == domain.CalendarUnchanged {
		_, err = w.store.CreateActivity(ctx, scope,
			managedActivity(updated, in.ActorID, "Meeting updated", w.now()))
	}
	return event, updated, err
}

func (w *Writer) Cancel(ctx context.Context, scope domain.Scope, ownerID, actorID, eventID, etag string) (*domain.CalendarEvent, error) {
	row, err := w.store.GetManagedCalendarEvent(ctx, scope, ownerID, eventID)
	if err != nil {
		return nil, err
	}
	token, err := w.access(ctx, scope, ownerID)
	if err != nil {
		return nil, err
	}
	if err := w.api.Cancel(ctx, token, row.CalendarID, eventID, etag); err != nil && !errors.Is(err, ErrEventNotFound) {
		return nil, err
	}
	row.Status, row.ProviderETag, row.CreatedBy = domain.CalendarStatusCancelled, etag, actorID
	_, err = w.store.RecordCalendarEvent(ctx, scope, row, func(domain.CalendarChange) *domain.Activity {
		return managedActivity(row, actorID, "Meeting cancelled", w.now())
	})
	return row, err
}

func validateManaged(in ManagedEventInput) error {
	if strings.TrimSpace(in.OwnerID) == "" || strings.TrimSpace(in.ActorID) == "" {
		return fmt.Errorf("%w: calendar owner and actor are required", domain.ErrInvalid)
	}
	if in.SubjectType != domain.SubjectContact && in.SubjectType != domain.SubjectLead && in.SubjectType != domain.SubjectOpportunity {
		return fmt.Errorf("%w: events may link to a contact, lead, or opportunity", domain.ErrInvalid)
	}
	if strings.TrimSpace(in.SubjectID) == "" {
		return fmt.Errorf("%w: linked CRM record is required", domain.ErrInvalid)
	}
	return in.Event.Validate()
}

func providerID(tenantID, ownerID, requestID string) string {
	digest := sha256.Sum256([]byte(tenantID + "\x00" + ownerID + "\x00" + requestID))
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:]))
}

func (w *Writer) access(ctx context.Context, scope domain.Scope, ownerID string) (string, error) {
	connection, err := w.store.GetGoogleConnection(ctx, scope, ownerID)
	if err != nil || connection.Status != domain.GoogleStatusConnected {
		return "", ErrCalendarNotConnected
	}
	found := false
	for _, granted := range connection.GrantedScopes {
		if granted == "https://www.googleapis.com/auth/calendar.events" {
			found = true
			break
		}
	}
	if !found {
		return "", ErrCalendarNotConnected
	}
	return w.tokens.AccessToken(ctx, scope, ownerID)
}

func (w *Writer) record(ctx context.Context, scope domain.Scope, in ManagedEventInput, event Event) (*domain.CalendarEvent, domain.CalendarChange, error) {
	status := event.Status
	if status == "" {
		status = domain.CalendarStatusConfirmed
	}
	row := &domain.CalendarEvent{OwnerID: in.OwnerID, ProviderEventID: event.ID, ProviderETag: event.ETag,
		CreatedBy: in.ActorID, SubjectType: in.SubjectType, SubjectID: in.SubjectID,
		CalendarID: "primary", ICalUID: event.ICalUID, RecurringEventID: event.RecurringEventID,
		ActivityID: domain.NewID(), Title: event.Title, Status: status, StartsAt: event.StartsAt,
		EndsAt: event.EndsAt, TimeZone: event.TimeZone, OrganizerAddress: event.Organizer,
		AttendeeCount: len(event.Attendees)}
	change, err := w.store.RecordCalendarEvent(ctx, scope, row, func(change domain.CalendarChange) *domain.Activity {
		label := "Meeting scheduled"
		if change == domain.CalendarRescheduled {
			label = "Meeting rescheduled"
		}
		if change == domain.CalendarCancelled {
			label = "Meeting cancelled"
		}
		return managedActivity(row, in.ActorID, label, w.now())
	})
	return row, change, err
}

func managedActivity(row *domain.CalendarEvent, actor, label string, now time.Time) *domain.Activity {
	return &domain.Activity{ID: domain.NewID(), Kind: domain.CalendarActivityKind,
		SubjectType: row.SubjectType, SubjectID: row.SubjectID,
		Notes: label + ": " + row.Title, ActorID: actor, OccurredAt: now}
}
