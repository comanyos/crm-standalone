package api

import (
	"connectrpc.com/connect"
	"context"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

func (s *CRMService) CreateCustomObjectDefinition(ctx context.Context, req *connect.Request[crmv1.CreateCustomObjectDefinitionRequest]) (*connect.Response[crmv1.CreateCustomObjectDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.CreateCustomObjectDefinition(ctx, scope, customObjectDefinitionFromProto(req.Msg.GetDefinition()))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateCustomObjectDefinitionResponse{Definition: customObjectDefinitionToProto(out)}), nil
}
func (s *CRMService) GetCustomObjectDefinition(ctx context.Context, req *connect.Request[crmv1.GetCustomObjectDefinitionRequest]) (*connect.Response[crmv1.GetCustomObjectDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetCustomObjectDefinition(ctx, scope, req.Msg.GetApiName())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.GetCustomObjectDefinitionResponse{Definition: customObjectDefinitionToProto(out)}), nil
}
func (s *CRMService) ListCustomObjectDefinitions(ctx context.Context, req *connect.Request[crmv1.ListCustomObjectDefinitionsRequest]) (*connect.Response[crmv1.ListCustomObjectDefinitionsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListCustomObjectDefinitions(ctx, scope, req.Msg.GetIncludeArchived())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.CustomObjectDefinition, 0, len(rows))
	for _, row := range rows {
		out = append(out, customObjectDefinitionToProto(row))
	}
	return connect.NewResponse(&crmv1.ListCustomObjectDefinitionsResponse{Definitions: out}), nil
}
func (s *CRMService) ArchiveCustomObjectDefinition(ctx context.Context, req *connect.Request[crmv1.ArchiveCustomObjectDefinitionRequest]) (*connect.Response[crmv1.ArchiveCustomObjectDefinitionResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.ArchiveCustomObjectDefinition(ctx, scope, req.Msg.GetApiName())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ArchiveCustomObjectDefinitionResponse{Definition: customObjectDefinitionToProto(out)}), nil
}
func (s *CRMService) CreateCustomRecord(ctx context.Context, req *connect.Request[crmv1.CreateCustomRecordRequest]) (*connect.Response[crmv1.CreateCustomRecordResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := customRecordFromProto(req.Msg.GetRecord())
	if err := s.authorizeCustomFieldWrite(ctx, scope, in.ObjectType, in.Fields); err != nil {
		return nil, err
	}
	out, err := s.store.CreateCustomRecord(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.customRecordForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.CreateCustomRecordResponse{Record: result}), nil
}
func (s *CRMService) GetCustomRecord(ctx context.Context, req *connect.Request[crmv1.GetCustomRecordRequest]) (*connect.Response[crmv1.GetCustomRecordResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.store.GetCustomRecord(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.customRecordForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.GetCustomRecordResponse{Record: result}), nil
}
func (s *CRMService) ListCustomRecords(ctx context.Context, req *connect.Request[crmv1.ListCustomRecordsRequest]) (*connect.Response[crmv1.ListCustomRecordsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.store.ListCustomRecords(ctx, scope, req.Msg.GetObjectType(), domain.Page{Size: int(req.Msg.GetPageSize()), Token: req.Msg.GetPageToken()})
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.CustomRecord, 0, len(rows))
	for _, row := range rows {
		result, err := s.customRecordForRead(ctx, scope, row)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return connect.NewResponse(&crmv1.ListCustomRecordsResponse{Records: out, NextPageToken: next}), nil
}
func (s *CRMService) UpdateCustomRecord(ctx context.Context, req *connect.Request[crmv1.UpdateCustomRecordRequest]) (*connect.Response[crmv1.UpdateCustomRecordResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	in := customRecordFromProto(req.Msg.GetRecord())
	if err := s.authorizeCustomFieldWrite(ctx, scope, in.ObjectType, in.Fields); err != nil {
		return nil, err
	}
	out, err := s.store.UpdateCustomRecord(ctx, scope, in)
	if err != nil {
		return nil, connectErr(err)
	}
	result, err := s.customRecordForRead(ctx, scope, out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&crmv1.UpdateCustomRecordResponse{Record: result}), nil
}
func (s *CRMService) DeleteCustomRecord(ctx context.Context, req *connect.Request[crmv1.DeleteCustomRecordRequest]) (*connect.Response[crmv1.DeleteCustomRecordResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteCustomRecord(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteCustomRecordResponse{}), nil
}
