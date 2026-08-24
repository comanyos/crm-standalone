package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Saved views (M4 — data productivity, internal/domain/saved_views.go).
//
// Every RPC here scopes to the caller's own views by passing
// principalSubject(ctx) as the required ownerID — never a value out of the
// request — the same way ListNotifications/MarkNotificationRead never take
// a recipient from the caller. There is no request field that could name a
// different owner even if a client tried.

func savedViewToProto(v *domain.SavedView) *crmv1.SavedView {
	if v == nil {
		return nil
	}
	return &crmv1.SavedView{
		Id: v.ID, ObjectType: v.ObjectType, Name: v.Name, Filters: v.Filters, Sort: v.Sort,
		Columns: v.Columns, CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt),
	}
}

// CreateSavedView saves a new preset for the caller's own use.
func (s *CRMService) CreateSavedView(
	ctx context.Context, req *connect.Request[crmv1.CreateSavedViewRequest],
) (*connect.Response[crmv1.CreateSavedViewResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.store.CreateSavedView(ctx, scope, &domain.SavedView{
		OwnerID: principalSubject(ctx), ObjectType: req.Msg.GetObjectType(), Name: req.Msg.GetName(),
		Filters: req.Msg.GetFilters(), Sort: req.Msg.GetSort(), Columns: req.Msg.GetColumns(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateSavedViewResponse{View: savedViewToProto(view)}), nil
}

// ListSavedViews returns the caller's own views, optionally narrowed to one
// list.
func (s *CRMService) ListSavedViews(
	ctx context.Context, req *connect.Request[crmv1.ListSavedViewsRequest],
) (*connect.Response[crmv1.ListSavedViewsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	views, err := s.store.ListSavedViews(ctx, scope, principalSubject(ctx), req.Msg.GetObjectType())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.SavedView, 0, len(views))
	for _, v := range views {
		out = append(out, savedViewToProto(v))
	}
	return connect.NewResponse(&crmv1.ListSavedViewsResponse{Views: out}), nil
}

// UpdateSavedView replaces one of the caller's own views.
func (s *CRMService) UpdateSavedView(
	ctx context.Context, req *connect.Request[crmv1.UpdateSavedViewRequest],
) (*connect.Response[crmv1.UpdateSavedViewResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	// object_type is not accepted from the request: it identifies which list
	// the view belongs to and is not meant to change after creation, the
	// same way CreateWorkflowRuleVersion never lets a caller retarget a
	// version's rule. UpdateSavedView reads the existing view to carry it
	// forward.
	existing, err := s.store.GetSavedView(ctx, scope, principalSubject(ctx), req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	view, err := s.store.UpdateSavedView(ctx, scope, principalSubject(ctx), &domain.SavedView{
		ID: req.Msg.GetId(), ObjectType: existing.ObjectType, Name: req.Msg.GetName(),
		Filters: req.Msg.GetFilters(), Sort: req.Msg.GetSort(), Columns: req.Msg.GetColumns(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateSavedViewResponse{View: savedViewToProto(view)}), nil
}

// DeleteSavedView removes one of the caller's own views.
func (s *CRMService) DeleteSavedView(
	ctx context.Context, req *connect.Request[crmv1.DeleteSavedViewRequest],
) (*connect.Response[crmv1.DeleteSavedViewResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteSavedView(ctx, scope, principalSubject(ctx), req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteSavedViewResponse{}), nil
}
