package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
)

// CreateCustomFieldDefinition adds one tenant-owned field definition. The
// authorization layer reserves this administrative operation for crm_admin;
// the scope derived here still anchors the write to the caller's tenant.
func (s *CRMService) CreateCustomFieldDefinition(ctx context.Context, req *connect.Request[crmv1.CreateCustomFieldDefinitionRequest]) (*connect.Response[crmv1.CreateCustomFieldDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateCustomFieldDefinition(ctx, scope, customFieldDefinitionFromProto(req.Msg.GetDefinition()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateCustomFieldDefinitionResponse{Definition: customFieldDefinitionToProto(out)}), nil
}

func (s *CRMService) GetCustomFieldDefinition(ctx context.Context, req *connect.Request[crmv1.GetCustomFieldDefinitionRequest]) (*connect.Response[crmv1.GetCustomFieldDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetCustomFieldDefinition(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetCustomFieldDefinitionResponse{Definition: customFieldDefinitionToProto(out)}), nil
}

func (s *CRMService) ListCustomFieldDefinitions(ctx context.Context, req *connect.Request[crmv1.ListCustomFieldDefinitionsRequest]) (*connect.Response[crmv1.ListCustomFieldDefinitionsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListCustomFieldDefinitions(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetIncludeArchived())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.CustomFieldDefinition, 0, len(rows))
	for _, row := range rows {
		out = append(out, customFieldDefinitionToProto(row))
	}
	return connect.NewResponse(&crmv1.ListCustomFieldDefinitionsResponse{Definitions: out}), nil
}

func (s *CRMService) UpdateCustomFieldDefinition(ctx context.Context, req *connect.Request[crmv1.UpdateCustomFieldDefinitionRequest]) (*connect.Response[crmv1.UpdateCustomFieldDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.UpdateCustomFieldDefinition(ctx, scope, customFieldDefinitionFromProto(req.Msg.GetDefinition()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateCustomFieldDefinitionResponse{Definition: customFieldDefinitionToProto(out)}), nil
}

func (s *CRMService) ArchiveCustomFieldDefinition(ctx context.Context, req *connect.Request[crmv1.ArchiveCustomFieldDefinitionRequest]) (*connect.Response[crmv1.ArchiveCustomFieldDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.ArchiveCustomFieldDefinition(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ArchiveCustomFieldDefinitionResponse{Definition: customFieldDefinitionToProto(out)}), nil
}
