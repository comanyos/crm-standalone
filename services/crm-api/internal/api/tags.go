package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Tags (M4 — data productivity, internal/domain/tags.go).

// maxTaggedRecordsPage bounds ListTaggedRecords however large a page_size
// the caller asks for.
const maxTaggedRecordsPage = 200

func tagToProto(v *domain.TagDefinition) *crmv1.Tag {
	if v == nil {
		return nil
	}
	return &crmv1.Tag{
		Id: v.ID, Name: v.Name, Color: v.Color, CreatedBy: v.CreatedBy,
		CreatedAt: optionalTimestamp(v.CreatedAt), UpdatedAt: optionalTimestamp(v.UpdatedAt),
	}
}

func tagsToProto(in []*domain.TagDefinition) []*crmv1.Tag {
	out := make([]*crmv1.Tag, 0, len(in))
	for _, v := range in {
		out = append(out, tagToProto(v))
	}
	return out
}

// CreateTag defines a new tag for the tenant.
func (s *CRMService) CreateTag(
	ctx context.Context, req *connect.Request[crmv1.CreateTagRequest],
) (*connect.Response[crmv1.CreateTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	tag, err := s.store.CreateTagDefinition(ctx, scope, &domain.TagDefinition{
		Name: req.Msg.GetName(), Color: req.Msg.GetColor(), CreatedBy: principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.CreateTagResponse{Tag: tagToProto(tag)}), nil
}

// ListTags returns every tag the tenant has defined.
func (s *CRMService) ListTags(
	ctx context.Context, _ *connect.Request[crmv1.ListTagsRequest],
) (*connect.Response[crmv1.ListTagsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := s.store.ListTagDefinitions(ctx, scope)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ListTagsResponse{Tags: tagsToProto(tags)}), nil
}

// UpdateTag renames or recolors a tag.
func (s *CRMService) UpdateTag(
	ctx context.Context, req *connect.Request[crmv1.UpdateTagRequest],
) (*connect.Response[crmv1.UpdateTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	tag, err := s.store.UpdateTagDefinition(ctx, scope, &domain.TagDefinition{
		ID: req.Msg.GetId(), Name: req.Msg.GetName(), Color: req.Msg.GetColor(),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.UpdateTagResponse{Tag: tagToProto(tag)}), nil
}

// DeleteTag removes a tag and every assignment of it.
func (s *CRMService) DeleteTag(
	ctx context.Context, req *connect.Request[crmv1.DeleteTagRequest],
) (*connect.Response[crmv1.DeleteTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteTagDefinition(ctx, scope, req.Msg.GetId()); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DeleteTagResponse{}), nil
}

// AssignTag attaches a tag to a record and returns the record's full current
// tag list, so the UI can refresh a badge list from one round trip.
func (s *CRMService) AssignTag(
	ctx context.Context, req *connect.Request[crmv1.AssignTagRequest],
) (*connect.Response[crmv1.AssignTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.AssignTag(ctx, scope, &domain.TagAssignment{
		TagID: req.Msg.GetTagId(), ObjectType: req.Msg.GetObjectType(), ObjectID: req.Msg.GetObjectId(),
		CreatedBy: principalSubject(ctx),
	}); err != nil {
		return nil, connectErr(err)
	}
	tags, err := s.store.ListTagsForRecord(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetObjectId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.AssignTagResponse{Tags: tagsToProto(tags)}), nil
}

// RemoveTag detaches a tag from a record and returns the record's remaining
// tags.
func (s *CRMService) RemoveTag(
	ctx context.Context, req *connect.Request[crmv1.RemoveTagRequest],
) (*connect.Response[crmv1.RemoveTagResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.RemoveTag(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetObjectId(), req.Msg.GetTagId()); err != nil {
		return nil, connectErr(err)
	}
	tags, err := s.store.ListTagsForRecord(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetObjectId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.RemoveTagResponse{Tags: tagsToProto(tags)}), nil
}

// ListTagsForRecord returns the tags attached to one record.
func (s *CRMService) ListTagsForRecord(
	ctx context.Context, req *connect.Request[crmv1.ListTagsForRecordRequest],
) (*connect.Response[crmv1.ListTagsForRecordResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := s.store.ListTagsForRecord(ctx, scope, req.Msg.GetObjectType(), req.Msg.GetObjectId())
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.ListTagsForRecordResponse{Tags: tagsToProto(tags)}), nil
}

// ListTaggedRecords returns one tag's assignments, optionally narrowed to one
// record type — the filter surface a list page uses to answer "show me
// every X tagged Y".
func (s *CRMService) ListTaggedRecords(
	ctx context.Context, req *connect.Request[crmv1.ListTaggedRecordsRequest],
) (*connect.Response[crmv1.ListTaggedRecordsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	pageSize := int(req.Msg.GetPageSize())
	if pageSize <= 0 || pageSize > maxTaggedRecordsPage {
		pageSize = maxTaggedRecordsPage
	}
	assignments, next, err := s.store.ListTaggedRecords(ctx, scope, req.Msg.GetTagId(), req.Msg.GetObjectType(),
		domain.Page{Size: pageSize, Token: req.Msg.GetPageToken()})
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.TaggedRecord, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, &crmv1.TaggedRecord{
			ObjectType: a.ObjectType, ObjectId: a.ObjectID, TaggedAt: optionalTimestamp(a.CreatedAt),
		})
	}
	return connect.NewResponse(&crmv1.ListTaggedRecordsResponse{Records: out, NextPageToken: next}), nil
}
