package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/gmail"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SyncGoogleMailbox runs one synchronisation pass for the caller's own
// mailbox.
//
// Like the other Google RPCs it takes no owner: a request that could name
// whose mailbox to read would let one user pull a colleague's correspondence
// into the CRM under their own name.
//
// A pass is bounded — a fixed number of messages, then a stored cursor — so
// this returns quickly on a large mailbox and the caller simply asks again.
// That is also what keeps it safe as a user-triggered action: it cannot be
// turned into an unbounded amount of work by pressing a button repeatedly.
func (s *CRMService) SyncGoogleMailbox(
	ctx context.Context, _ *connect.Request[crmv1.SyncGoogleMailboxRequest],
) (*connect.Response[crmv1.SyncGoogleMailboxResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.gmail == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("gmail synchronisation is not configured"))
	}

	result, syncErr := s.gmail.SyncMailbox(ctx, scope, owner)
	// The state is read back regardless: a failed pass still records why, and
	// that reason is the most useful thing the UI can show.
	state, stateErr := s.store.GetGmailSyncState(ctx, scope, owner)
	if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
		return nil, connectErr(stateErr)
	}
	if syncErr != nil {
		return nil, gmailSyncErr(syncErr)
	}

	return connect.NewResponse(&crmv1.SyncGoogleMailboxResponse{
		GmailSync:        gmailSyncToProto(state),
		MessagesExamined: int32(result.MessagesExamined),
		EntriesCreated:   int32(result.EntriesCreated),
	}), nil
}

// gmailSyncErr maps a sync failure onto a Connect code.
//
// A mailbox another replica is already syncing is Aborted rather than
// Internal: nothing is wrong, the caller should simply try again in a moment,
// and a client that retries on Aborted does the right thing automatically.
func gmailSyncErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted,
			errors.New("a synchronisation pass for this mailbox is already running"))
	case errors.Is(err, gmail.ErrHistoryExpired):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the mailbox history cursor expired; a fresh backfill has been scheduled"))
	default:
		return connectErr(err)
	}
}

// gmailSyncToProto projects sync state for a browser-facing response. It
// takes the summary rather than the state so no cursor, lease or page token
// is in scope to map by accident.
func gmailSyncToProto(state *domain.GmailSyncState) *crmv1.GmailSyncStatus {
	if state == nil {
		return nil
	}
	summary := state.Summary()
	return &crmv1.GmailSyncStatus{
		BackfillComplete:  summary.BackfillComplete,
		LastSyncedAt:      optionalTimestamp(summary.LastSyncedAt),
		LastMessageAt:     optionalTimestamp(summary.LastMessageAt),
		MessagesIndexed:   summary.MessagesIndexed,
		ActivitiesCreated: summary.ActivitiesCreated,
		LastError:         summary.LastError,
	}
}

var _ = timestamppb.New
