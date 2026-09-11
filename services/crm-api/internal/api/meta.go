package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	metaintegration "github.com/santoshguru/crm/internal/integration/meta"
)

func metaErr(err error) error {
	if errors.Is(err, metaintegration.ErrNotConfigured) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connectErr(err)
}

func metaActor(ctx context.Context) (domain.Scope, string, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return domain.Scope{}, "", err
	}
	actor := principalSubject(ctx)
	if strings.TrimSpace(actor) == "" {
		return domain.Scope{}, "", connect.NewError(connect.CodePermissionDenied, errors.New("principal has no subject"))
	}
	return scope, actor, nil
}

func metaConnectionToProto(v domain.MetaConnectionView) *crmv1.MetaConnection {
	return &crmv1.MetaConnection{
		Id: v.ID, MetaUserId: v.MetaUserID, MetaUserName: v.MetaUserName,
		Status: v.Status, GrantedPermissions: v.GrantedPermissions,
		AccessTokenExpiresAt: optionalTimestamp(v.AccessTokenExpiresAt),
		BusinessId:           v.BusinessID, BusinessName: v.BusinessName,
		PageId: v.PageID, PageName: v.PageName, FormId: v.FormID, FormName: v.FormName,
		ConnectedBy: v.ConnectedBy, ConnectedAt: optionalTimestamp(v.ConnectedAt),
		LastWebhookAt: optionalTimestamp(v.LastWebhookAt), LastImportAt: optionalTimestamp(v.LastImportAt),
		DisconnectedAt: optionalTimestamp(v.DisconnectedAt), LastError: v.LastError,
	}
}

func (s *CRMService) ListMetaBusinesses(ctx context.Context, _ *connect.Request[crmv1.ListMetaBusinessesRequest]) (*connect.Response[crmv1.ListMetaBusinessesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	rows, err := s.meta.Businesses(ctx, scope)
	if err != nil {
		return nil, metaErr(err)
	}
	out := make([]*crmv1.MetaBusiness, 0, len(rows))
	for _, row := range rows {
		out = append(out, &crmv1.MetaBusiness{Id: row.ID, Name: row.Name})
	}
	return connect.NewResponse(&crmv1.ListMetaBusinessesResponse{Businesses: out}), nil
}

func (s *CRMService) StartMetaConnection(ctx context.Context, req *connect.Request[crmv1.StartMetaConnectionRequest]) (*connect.Response[crmv1.StartMetaConnectionResponse], error) {
	scope, actor, err := metaActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	value, err := s.meta.Start(ctx, scope, actor, req.Msg.GetReturnPath())
	if err != nil {
		return nil, metaErr(err)
	}
	return connect.NewResponse(&crmv1.StartMetaConnectionResponse{AuthorizationUrl: value}), nil
}

func (s *CRMService) GetMetaConnection(ctx context.Context, _ *connect.Request[crmv1.GetMetaConnectionRequest]) (*connect.Response[crmv1.GetMetaConnectionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	configured := s.meta != nil && s.meta.Configured()
	resp := &crmv1.GetMetaConnectionResponse{Configured: configured}
	if s.meta != nil {
		resp.RequestedPermissions = s.meta.RequestedPermissions()
	}
	if s.cfg.PublicBaseURL != "" {
		resp.WebhookUrl = s.cfg.PublicBaseURL + "/integrations/meta/webhook"
	}
	if !configured {
		resp.Connection = &crmv1.MetaConnection{}
		return connect.NewResponse(resp), nil
	}
	connection, err := s.meta.Connection(ctx, scope)
	if errors.Is(err, domain.ErrNotFound) {
		resp.Connection = &crmv1.MetaConnection{}
		return connect.NewResponse(resp), nil
	}
	if err != nil {
		return nil, metaErr(err)
	}
	resp.Connection = metaConnectionToProto(connection.View())
	return connect.NewResponse(resp), nil
}

func (s *CRMService) ListMetaPages(ctx context.Context, req *connect.Request[crmv1.ListMetaPagesRequest]) (*connect.Response[crmv1.ListMetaPagesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	rows, err := s.meta.Pages(ctx, scope, req.Msg.GetBusinessId())
	if err != nil {
		return nil, metaErr(err)
	}
	out := make([]*crmv1.MetaPage, 0, len(rows))
	for _, row := range rows {
		out = append(out, &crmv1.MetaPage{Id: row.ID, Name: row.Name})
	}
	return connect.NewResponse(&crmv1.ListMetaPagesResponse{Pages: out}), nil
}

func (s *CRMService) ListMetaForms(ctx context.Context, req *connect.Request[crmv1.ListMetaFormsRequest]) (*connect.Response[crmv1.ListMetaFormsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	rows, err := s.meta.Forms(ctx, scope, req.Msg.GetBusinessId(), req.Msg.GetPageId())
	if err != nil {
		return nil, metaErr(err)
	}
	out := make([]*crmv1.MetaLeadForm, 0, len(rows))
	for _, row := range rows {
		out = append(out, &crmv1.MetaLeadForm{Id: row.ID, Name: row.Name, Status: row.Status})
	}
	return connect.NewResponse(&crmv1.ListMetaFormsResponse{Forms: out}), nil
}

func (s *CRMService) SelectMetaLeadForm(ctx context.Context, req *connect.Request[crmv1.SelectMetaLeadFormRequest]) (*connect.Response[crmv1.SelectMetaLeadFormResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	connection, err := s.meta.SelectForm(ctx, scope, req.Msg.GetBusinessId(), req.Msg.GetPageId(), req.Msg.GetFormId())
	if err != nil {
		return nil, metaErr(err)
	}
	return connect.NewResponse(&crmv1.SelectMetaLeadFormResponse{Connection: metaConnectionToProto(connection.View())}), nil
}

func (s *CRMService) ImportMetaLeads(ctx context.Context, req *connect.Request[crmv1.ImportMetaLeadsRequest]) (*connect.Response[crmv1.ImportMetaLeadsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	result, err := s.meta.Import(ctx, scope, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, metaErr(err)
	}
	return connect.NewResponse(&crmv1.ImportMetaLeadsResponse{Examined: int32(result.Examined), Created: int32(result.Created), Duplicates: int32(result.Duplicates), Rejected: int32(result.Rejected)}), nil
}

func (s *CRMService) DisconnectMetaConnection(ctx context.Context, req *connect.Request[crmv1.DisconnectMetaConnectionRequest]) (*connect.Response[crmv1.DisconnectMetaConnectionResponse], error) {
	scope, _, err := metaActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.meta == nil {
		return nil, metaErr(metaintegration.ErrNotConfigured)
	}
	connection, err := s.meta.Disconnect(ctx, scope, req.Msg.GetPurge())
	if err != nil {
		return nil, metaErr(err)
	}
	resp := &crmv1.DisconnectMetaConnectionResponse{}
	if connection != nil {
		resp.Connection = metaConnectionToProto(connection.View())
	}
	return connect.NewResponse(resp), nil
}

func (s *CRMService) GetLeadMetaAttribution(ctx context.Context, req *connect.Request[crmv1.GetLeadMetaAttributionRequest]) (*connect.Response[crmv1.GetLeadMetaAttributionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	// Read the Lead first so a foreign/nonexistent lead has exactly one answer.
	if _, err := s.store.GetLead(ctx, scope, req.Msg.GetLeadId()); err != nil {
		return nil, connectErr(err)
	}
	row, err := s.store.GetMetaLeadAttribution(ctx, scope, req.Msg.GetLeadId())
	if errors.Is(err, domain.ErrNotFound) {
		return connect.NewResponse(&crmv1.GetLeadMetaAttributionResponse{}), nil
	}
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetLeadMetaAttributionResponse{Attribution: &crmv1.MetaLeadAttribution{
		ProviderLeadId: row.ProviderLeadID, Platform: row.Platform, PageId: row.PageID, FormId: row.FormID,
		AdId: row.AdID, AdName: row.AdName, AdsetId: row.AdSetID, AdsetName: row.AdSetName,
		CampaignId: row.CampaignID, CampaignName: row.CampaignName,
		ProviderCreatedAt: optionalTimestamp(row.ProviderCreatedAt), CapturedAt: optionalTimestamp(row.CapturedAt),
	}}), nil
}
