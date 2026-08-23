package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/calendar"
)

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
		EventsExamined: int32(result.EventsExamined),
		EntriesCreated: int32(result.EntriesCreated),
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
