package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/google"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The Google connection RPCs (docs/GOOGLE_INTEGRATION.md).
//
// One rule runs through all of them: the owner is always the calling
// principal's subject, taken from the verified token and never from the
// request. There is no owner field on any request message for that reason —
// a client that could name whose mailbox to disconnect, or whose
// authorization to begin, would be able to act on a colleague's Google
// account, and no scope granularity fixes that.

// googleOwner resolves the caller's tenant scope and their own owner id
// together, since every one of these RPCs needs both.
func googleOwner(ctx context.Context) (domain.Scope, string, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return domain.Scope{}, "", err
	}
	subject := principalSubject(ctx)
	if subject == "" {
		return domain.Scope{}, "", connect.NewError(connect.CodePermissionDenied,
			errors.New("principal carries no subject, so no Google connection can be attributed to it"))
	}
	return scope, subject, nil
}

// googleErr maps this integration's failures onto Connect codes. An
// unconfigured deployment is FailedPrecondition rather than Internal: nothing
// is broken, the operator simply has not supplied Google credentials, and the
// UI should say so instead of showing an error.
func googleErr(err error) error {
	if errors.Is(err, google.ErrNotConfigured) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connectErr(err)
}

// StartGoogleConnection returns the consent URL for the calling user.
//
// The one-time state and PKCE verifier are persisted server-side before this
// returns; neither is in the response. The client's only job is to send the
// browser to the URL.
func (s *CRMService) StartGoogleConnection(
	ctx context.Context, req *connect.Request[crmv1.StartGoogleConnectionRequest],
) (*connect.Response[crmv1.StartGoogleConnectionResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.google == nil {
		return nil, googleErr(google.ErrNotConfigured)
	}
	authorizationURL, err := s.google.Start(ctx, scope, owner, req.Msg.GetReturnPath())
	if err != nil {
		return nil, googleErr(err)
	}
	return connect.NewResponse(&crmv1.StartGoogleConnectionResponse{
		AuthorizationUrl: authorizationURL,
	}), nil
}

// GetGoogleConnection returns the caller's own connection status.
//
// Having no connection is not an error — it is the normal state of most
// users — so it comes back as a connection with an empty status rather than
// NotFound, which the UI would otherwise have to tell apart from a real
// failure.
func (s *CRMService) GetGoogleConnection(
	ctx context.Context, _ *connect.Request[crmv1.GetGoogleConnectionRequest],
) (*connect.Response[crmv1.GetGoogleConnectionResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	resp := &crmv1.GetGoogleConnectionResponse{
		Configured: s.google.Configured(),
	}
	if s.google != nil {
		resp.RequestedScopes = s.google.RequestedScopes()
	}
	if !resp.Configured {
		// Without provider configuration there is no connection to report,
		// and saying so is the whole answer the UI needs.
		resp.Connection = &crmv1.GoogleConnection{OwnerId: owner}
		return connect.NewResponse(resp), nil
	}
	view, err := s.google.Status(ctx, scope, owner)
	if err != nil {
		return nil, googleErr(err)
	}
	resp.Connection = googleConnectionToProto(view)
	return connect.NewResponse(resp), nil
}

// ListGoogleConnections is the administrative view of who in this tenant has
// linked a Google account. It returns status only, never credentials, and is
// gated on its own scope so reading colleagues' connection state is a
// deliberate grant rather than a side effect of using the integration.
func (s *CRMService) ListGoogleConnections(
	ctx context.Context, _ *connect.Request[crmv1.ListGoogleConnectionsRequest],
) (*connect.Response[crmv1.ListGoogleConnectionsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.google == nil {
		return connect.NewResponse(&crmv1.ListGoogleConnectionsResponse{}), nil
	}
	views, err := s.google.List(ctx, scope)
	if err != nil {
		return nil, googleErr(err)
	}
	out := make([]*crmv1.GoogleConnection, 0, len(views))
	for _, view := range views {
		out = append(out, googleConnectionToProto(view))
	}
	return connect.NewResponse(&crmv1.ListGoogleConnectionsResponse{Connections: out}), nil
}

// DisconnectGoogleConnection revokes the caller's own grant at Google and
// erases the stored credential. With purge set it also deletes the record.
func (s *CRMService) DisconnectGoogleConnection(
	ctx context.Context, req *connect.Request[crmv1.DisconnectGoogleConnectionRequest],
) (*connect.Response[crmv1.DisconnectGoogleConnectionResponse], error) {
	scope, owner, err := googleOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.google == nil {
		return nil, googleErr(google.ErrNotConfigured)
	}
	view, err := s.google.Disconnect(ctx, scope, owner, owner, req.Msg.GetPurge())
	if err != nil {
		return nil, googleErr(err)
	}
	return connect.NewResponse(&crmv1.DisconnectGoogleConnectionResponse{
		Connection: googleConnectionToProto(view),
	}), nil
}

// googleConnectionToProto projects the domain view onto the wire type. It
// takes GoogleConnectionView rather than GoogleConnection so there is no
// ciphertext field in scope to accidentally map.
func googleConnectionToProto(v domain.GoogleConnectionView) *crmv1.GoogleConnection {
	return &crmv1.GoogleConnection{
		Id:                   v.ID,
		OwnerId:              v.OwnerID,
		GoogleAccountEmail:   v.GoogleAccountEmail,
		Status:               v.Status,
		GrantedScopes:        v.GrantedScopes,
		AccessTokenExpiresAt: optionalTimestamp(v.AccessTokenExpiresAt),
		ConnectedBy:          v.ConnectedBy,
		ConnectedAt:          optionalTimestamp(v.ConnectedAt),
		LastRefreshedAt:      optionalTimestamp(v.LastRefreshedAt),
		DisconnectedAt:       optionalTimestamp(v.DisconnectedAt),
		LastError:            v.LastError,
	}
}

// optionalTimestamp leaves an absent time absent rather than sending the zero
// instant, which a client would render as the year 1.
func optionalTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
