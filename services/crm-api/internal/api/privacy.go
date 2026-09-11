package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Data-subject requests against synchronised communication content.
//
// These differ from the sync RPCs in taking an argument at all. The sync ones
// act on the caller's own mailbox because consent belongs to whoever granted
// it; these act on behalf of somebody who is usually not a CRM user at all —
// a customer asking what a supplier holds about them — so an administrator
// has to be able to name the record. That is why they carry their own scope.

// EraseSyncedContentForContact removes the email and meetings sync collected
// about one Contact, and stops collecting more.
//
// Both halves matter. Erasing without suppressing is undone by the next poll,
// so this deliberately does not offer the option of doing only one.
func (s *CRMService) EraseSyncedContentForContact(
	ctx context.Context, req *connect.Request[crmv1.EraseSyncedContentForContactRequest],
) (*connect.Response[crmv1.EraseSyncedContentForContactResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.privacy.EraseForContact(ctx, scope,
		req.Msg.GetContactId(), principalSubject(ctx), req.Msg.GetReason())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.EraseSyncedContentForContactResponse{
		Result: erasureResultToProto(result),
	}), nil
}

// ListSyncSuppressions returns the addresses sync will never match.
func (s *CRMService) ListSyncSuppressions(
	ctx context.Context, _ *connect.Request[crmv1.ListSyncSuppressionsRequest],
) (*connect.Response[crmv1.ListSyncSuppressionsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.privacy.List(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.SyncSuppression, 0, len(rows))
	for _, row := range rows {
		out = append(out, &crmv1.SyncSuppression{
			Address:   row.Address,
			Reason:    row.Reason,
			CreatedBy: row.CreatedBy,
			CreatedAt: optionalTimestamp(row.CreatedAt),
		})
	}
	return connect.NewResponse(&crmv1.ListSyncSuppressionsResponse{Suppressions: out}), nil
}

// SuppressSyncAddress stops future collection for one address without erasing
// what is already held — for somebody who asks not to be tracked from now on.
func (s *CRMService) SuppressSyncAddress(
	ctx context.Context, req *connect.Request[crmv1.SuppressSyncAddressRequest],
) (*connect.Response[crmv1.SuppressSyncAddressResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.privacy.Suppress(ctx, scope,
		req.Msg.GetAddress(), principalSubject(ctx), req.Msg.GetReason()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.SuppressSyncAddressResponse{}), nil
}

// ReleaseSyncAddress removes an address suppressed in error.
func (s *CRMService) ReleaseSyncAddress(
	ctx context.Context, req *connect.Request[crmv1.ReleaseSyncAddressRequest],
) (*connect.Response[crmv1.ReleaseSyncAddressResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.privacy.Release(ctx, scope, req.Msg.GetAddress()); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound,
				errors.New("that address is not suppressed"))
		}
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ReleaseSyncAddressResponse{}), nil
}

func erasureResultToProto(r domain.ErasureResult) *crmv1.ErasureResult {
	return &crmv1.ErasureResult{
		ActivitiesDeleted:   boundedInt32(r.ActivitiesDeleted),
		MessagesRedacted:    boundedInt32(r.MessagesRedacted),
		EventsRedacted:      boundedInt32(r.EventsRedacted),
		AddressesSuppressed: boundedInt32(r.AddressesSuppressed),
	}
}

var _ = timestamppb.New
