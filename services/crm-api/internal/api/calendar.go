package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/calendar"
)

// CreateGoogleCalendarEvent creates a meeting in the caller's own connected
// primary calendar and records its CRM link in the same tenant.
func (s *CRMService) CreateGoogleCalendarEvent(
	ctx context.Context, req *connect.Request[crmv1.CreateGoogleCalendarEventRequest],
) (*connect.Response[crmv1.CreateGoogleCalendarEventResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.calendarWriter == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google Calendar writes are not configured"))
	}
	in, err := calendarInputFromProto(req.Msg.GetEvent())
	if err != nil {
		return nil, connectErr(err)
	}
	event, row, err := s.calendarWriter.Create(ctx, scope, calendar.ManagedEventInput{
		RequestID: req.Msg.GetRequestId(), OwnerID: owner, ActorID: principalSubject(ctx),
		SubjectType: req.Msg.GetEvent().GetSubjectType(), SubjectID: req.Msg.GetEvent().GetSubjectId(), Event: in,
	})
	if err != nil {
		return nil, calendarWriteErr(err)
	}
	return connect.NewResponse(&crmv1.CreateGoogleCalendarEventResponse{Event: managedCalendarEventToProto(event, row)}), nil
}

func (s *CRMService) GetGoogleCalendarEvent(
	ctx context.Context, req *connect.Request[crmv1.GetGoogleCalendarEventRequest],
) (*connect.Response[crmv1.GetGoogleCalendarEventResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.calendarWriter == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google Calendar writes are not configured"))
	}
	event, row, err := s.calendarWriter.Get(ctx, scope, owner, req.Msg.GetProviderEventId())
	if err != nil {
		return nil, calendarWriteErr(err)
	}
	return connect.NewResponse(&crmv1.GetGoogleCalendarEventResponse{Event: managedCalendarEventToProto(event, row)}), nil
}

func (s *CRMService) ListGoogleCalendarEvents(
	ctx context.Context, req *connect.Request[crmv1.ListGoogleCalendarEventsRequest],
) (*connect.Response[crmv1.ListGoogleCalendarEventsResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	var from, to time.Time
	if req.Msg.GetFrom() != nil {
		if err := req.Msg.GetFrom().CheckValid(); err != nil {
			return nil, connectErr(domain.ErrInvalid)
		}
		from = req.Msg.GetFrom().AsTime()
	}
	if req.Msg.GetTo() != nil {
		if err := req.Msg.GetTo().CheckValid(); err != nil {
			return nil, connectErr(domain.ErrInvalid)
		}
		to = req.Msg.GetTo().AsTime()
	}
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		return nil, connectErr(domain.ErrInvalid)
	}
	rows, err := s.store.ListManagedCalendarEvents(ctx, scope, owner, from, to, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.GoogleCalendarEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, managedCalendarEventToProto(calendar.Event{}, row))
	}
	return connect.NewResponse(&crmv1.ListGoogleCalendarEventsResponse{Events: out}), nil
}

func (s *CRMService) UpdateGoogleCalendarEvent(
	ctx context.Context, req *connect.Request[crmv1.UpdateGoogleCalendarEventRequest],
) (*connect.Response[crmv1.UpdateGoogleCalendarEventResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.calendarWriter == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google Calendar writes are not configured"))
	}
	in, err := calendarInputFromProto(req.Msg.GetEvent())
	if err != nil {
		return nil, connectErr(err)
	}
	in.ID = req.Msg.GetEvent().GetProviderEventId()
	event, row, err := s.calendarWriter.Update(ctx, scope, calendar.ManagedEventInput{
		OwnerID: owner, ActorID: principalSubject(ctx), ETag: req.Msg.GetEvent().GetEtag(), Event: in,
	})
	if err != nil {
		return nil, calendarWriteErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateGoogleCalendarEventResponse{Event: managedCalendarEventToProto(event, row)}), nil
}

func (s *CRMService) CancelGoogleCalendarEvent(
	ctx context.Context, req *connect.Request[crmv1.CancelGoogleCalendarEventRequest],
) (*connect.Response[crmv1.CancelGoogleCalendarEventResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.calendarWriter == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Google Calendar writes are not configured"))
	}
	row, err := s.calendarWriter.Cancel(ctx, scope, owner, principalSubject(ctx), req.Msg.GetProviderEventId(), req.Msg.GetEtag())
	if err != nil {
		return nil, calendarWriteErr(err)
	}
	return connect.NewResponse(&crmv1.CancelGoogleCalendarEventResponse{Event: managedCalendarEventToProto(calendar.Event{}, row)}), nil
}

func calendarInputFromProto(v *crmv1.GoogleCalendarEvent) (calendar.EventInput, error) {
	if v == nil || v.GetStartsAt() == nil || v.GetEndsAt() == nil {
		return calendar.EventInput{}, domain.ErrInvalid
	}
	if err := v.GetStartsAt().CheckValid(); err != nil {
		return calendar.EventInput{}, domain.ErrInvalid
	}
	if err := v.GetEndsAt().CheckValid(); err != nil {
		return calendar.EventInput{}, domain.ErrInvalid
	}
	reminders := make([]int, 0, len(v.GetReminderMinutes()))
	for _, minutes := range v.GetReminderMinutes() {
		reminders = append(reminders, int(minutes))
	}
	in := calendar.EventInput{ID: v.GetProviderEventId(), Title: v.GetTitle(), Description: v.GetDescription(),
		Location: v.GetLocation(), StartsAt: v.GetStartsAt().AsTime(), EndsAt: v.GetEndsAt().AsTime(),
		TimeZone: v.GetTimeZone(), Attendees: append([]string(nil), v.GetAttendees()...),
		Recurrence: append([]string(nil), v.GetRecurrence()...), ReminderMinutes: reminders}
	if err := in.Validate(); err != nil {
		return calendar.EventInput{}, err
	}
	return in, nil
}

func managedCalendarEventToProto(event calendar.Event, row *domain.CalendarEvent) *crmv1.GoogleCalendarEvent {
	if row == nil {
		return nil
	}
	out := &crmv1.GoogleCalendarEvent{ProviderEventId: row.ProviderEventID, Etag: row.ProviderETag,
		Title: row.Title, StartsAt: optionalTimestamp(row.StartsAt), EndsAt: optionalTimestamp(row.EndsAt),
		TimeZone: row.TimeZone, Status: row.Status, SubjectType: row.SubjectType, SubjectId: row.SubjectID,
		AttendeeCount: boundedInt32(row.AttendeeCount), Editable: row.CreatedBy != ""}
	if event.ID != "" {
		out.Etag, out.Title, out.Description, out.Location = event.ETag, event.Title, event.Description, event.Location
		out.StartsAt, out.EndsAt, out.TimeZone = optionalTimestamp(event.StartsAt), optionalTimestamp(event.EndsAt), event.TimeZone
		out.Status, out.Recurrence = event.Status, append([]string(nil), event.Recurrence...)
		for _, attendee := range event.Attendees {
			if address := strings.TrimSpace(attendee.Email); address != "" {
				out.Attendees = append(out.Attendees, address)
			}
		}
		for _, minutes := range event.ReminderMinutes {
			out.ReminderMinutes = append(out.ReminderMinutes, boundedInt32(minutes))
		}
		out.AttendeeCount = boundedInt32(len(event.Attendees))
	}
	return out
}

func calendarWriteErr(err error) error {
	switch {
	case errors.Is(err, calendar.ErrCalendarNotConnected):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("connect Google Calendar before creating events"))
	case errors.Is(err, calendar.ErrEventChanged), errors.Is(err, calendar.ErrEventConflict), errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("the calendar event changed; refresh it and try again"))
	case errors.Is(err, calendar.ErrEventNotFound), errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("calendar event not found"))
	default:
		return connectErr(err)
	}
}

// SyncGoogleCalendar runs one synchronisation pass for the caller's own
// calendar. Like every other Google RPC it takes no owner: a request that
// could name whose calendar to read would expose who a colleague has been
// meeting.
func (s *CRMService) SyncGoogleCalendar(
	ctx context.Context, _ *connect.Request[crmv1.SyncGoogleCalendarRequest],
) (*connect.Response[crmv1.SyncGoogleCalendarResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.calendar == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("calendar synchronisation is not configured"))
	}

	result, syncErr := s.calendar.SyncCalendar(ctx, scope, owner)
	state, stateErr := s.store.GetCalendarSyncState(ctx, scope, owner)
	if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
		return nil, connectErr(stateErr)
	}
	if syncErr != nil {
		return nil, calendarSyncErr(syncErr)
	}

	return connect.NewResponse(&crmv1.SyncGoogleCalendarResponse{
		CalendarSync:   calendarSyncToProto(state),
		EventsExamined: boundedInt32(result.EventsExamined),
		EntriesCreated: boundedInt32(result.EntriesCreated),
	}), nil
}

// calendarSyncErr maps a sync failure onto a Connect code, with the same
// reasoning as gmailSyncErr: a pass another replica already holds is Aborted,
// so a client that retries on Aborted does the right thing by itself.
func calendarSyncErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted,
			errors.New("a synchronisation pass for this calendar is already running"))
	case errors.Is(err, calendar.ErrSyncTokenExpired):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the calendar sync cursor expired; a fresh pass over the window has been scheduled"))
	default:
		return connectErr(err)
	}
}

// calendarSyncToProto projects sync state for a browser-facing response. It
// takes the summary so no cursor, lease or page token is in scope to map by
// accident.
func calendarSyncToProto(state *domain.CalendarSyncState) *crmv1.CalendarSyncStatus {
	if state == nil {
		return nil
	}
	summary := state.Summary()
	return &crmv1.CalendarSyncStatus{
		BackfillComplete:  summary.BackfillComplete,
		LastSyncedAt:      optionalTimestamp(summary.LastSyncedAt),
		LastEventAt:       optionalTimestamp(summary.LastEventAt),
		EventsIndexed:     summary.EventsIndexed,
		ActivitiesCreated: summary.ActivitiesCreated,
		LastError:         summary.LastError,
	}
}
