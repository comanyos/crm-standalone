package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Message templates (task_crm.md §5, internal/domain/message_template.go).
//
// Thin transport, as this layer is meant to be. The one thing worth noticing
// is what is absent: there is no render endpoint. Substitution happens in the
// browser against the record already on screen, so the server never expands a
// placeholder and never has to decide whether the caller may read the field
// behind one.

func messageTemplateToProto(v *domain.MessageTemplate) *crmv1.MessageTemplate {
	if v == nil {
		return nil
	}
	return &crmv1.MessageTemplate{
		Id: v.ID, Name: v.Name, ChannelKind: v.ChannelKind,
		Subject: v.Subject, Body: v.Body, CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt),
	}
}

// CreateMessageTemplate stores new wording for the tenant.
func (s *CRMService) CreateMessageTemplate(
	ctx context.Context, req *connect.Request[crmv1.CreateMessageTemplateRequest],
) (*connect.Response[crmv1.CreateMessageTemplateResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	template, err := s.store.CreateMessageTemplate(ctx, scope, &domain.MessageTemplate{
		Name:        req.Msg.GetName(),
		ChannelKind: req.Msg.GetChannelKind(),
		Subject:     req.Msg.GetSubject(),
		Body:        req.Msg.GetBody(),
		CreatedBy:   principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateMessageTemplateResponse{
		Template: messageTemplateToProto(template),
	}), nil
}

// ListMessageTemplates returns the tenant's templates for one channel, or all.
func (s *CRMService) ListMessageTemplates(
	ctx context.Context, req *connect.Request[crmv1.ListMessageTemplatesRequest],
) (*connect.Response[crmv1.ListMessageTemplatesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	templates, err := s.store.ListMessageTemplates(ctx, scope, req.Msg.GetChannelKind())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.MessageTemplate, 0, len(templates))
	for _, template := range templates {
		out = append(out, messageTemplateToProto(template))
	}
	return connect.NewResponse(&crmv1.ListMessageTemplatesResponse{Templates: out}), nil
}

// UpdateMessageTemplate rewrites one template's wording.
func (s *CRMService) UpdateMessageTemplate(
	ctx context.Context, req *connect.Request[crmv1.UpdateMessageTemplateRequest],
) (*connect.Response[crmv1.UpdateMessageTemplateResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	template, err := s.store.UpdateMessageTemplate(ctx, scope, &domain.MessageTemplate{
		ID:          req.Msg.GetId(),
		Name:        req.Msg.GetName(),
		ChannelKind: req.Msg.GetChannelKind(),
		Subject:     req.Msg.GetSubject(),
		Body:        req.Msg.GetBody(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateMessageTemplateResponse{
		Template: messageTemplateToProto(template),
	}), nil
}

// DeleteMessageTemplate removes one.
func (s *CRMService) DeleteMessageTemplate(
	ctx context.Context, req *connect.Request[crmv1.DeleteMessageTemplateRequest],
) (*connect.Response[crmv1.DeleteMessageTemplateResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteMessageTemplate(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteMessageTemplateResponse{}), nil
}

// DescribeMessageTemplateCatalog reports the allowlists the editor must offer.
func (s *CRMService) DescribeMessageTemplateCatalog(
	ctx context.Context, _ *connect.Request[crmv1.DescribeMessageTemplateCatalogRequest],
) (*connect.Response[crmv1.DescribeMessageTemplateCatalogResponse], error) {
	if _, err := scopeFrom(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.DescribeMessageTemplateCatalogResponse{
		ChannelKinds: domain.SortedTemplateChannelKinds(),
		MergeFields:  domain.SortedMergeFields(),
	}), nil
}
