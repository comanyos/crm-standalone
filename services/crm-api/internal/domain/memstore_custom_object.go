package domain

import (
	"context"
	"fmt"
	"sort"
)

func (s *MemStore) CreateCustomObjectDefinition(_ context.Context, scope Scope, v *CustomObjectDefinition) (*CustomObjectDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.customObject.all(scope)
	if err != nil {
		return nil, err
	}
	for _, row := range existing {
		if row.APIName == v.APIName {
			return nil, fmt.Errorf("%w: custom object already exists", ErrConflict)
		}
	}
	return s.customObject.create(scope, v)
}
func (s *MemStore) GetCustomObjectDefinition(_ context.Context, scope Scope, apiName string) (*CustomObjectDefinition, error) {
	rows, err := s.customObject.all(scope)
	if err != nil {
		return nil, err
	}
	apiName = normalizeObjectType(apiName)
	for _, row := range rows {
		if row.APIName == apiName {
			return row, nil
		}
	}
	return nil, ErrNotFound
}
func (s *MemStore) ListCustomObjectDefinitions(_ context.Context, scope Scope, includeArchived bool) ([]*CustomObjectDefinition, error) {
	rows, err := s.customObject.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*CustomObjectDefinition, 0, len(rows))
	for _, row := range rows {
		if includeArchived || !row.Archived() {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].APIName < out[j].APIName })
	return out, nil
}
func (s *MemStore) ArchiveCustomObjectDefinition(ctx context.Context, scope Scope, apiName string) (*CustomObjectDefinition, error) {
	row, err := s.GetCustomObjectDefinition(ctx, scope, apiName)
	if err != nil {
		return nil, err
	}
	if row.Archived() {
		return row, nil
	}
	row.ArchivedAt = nowStamp()
	return s.customObject.update(scope, row)
}
func (s *MemStore) requireLiveCustomObject(ctx context.Context, scope Scope, objectType string) error {
	row, err := s.GetCustomObjectDefinition(ctx, scope, objectType)
	if err != nil {
		return err
	}
	if row.Archived() {
		return fmt.Errorf("%w: custom object is archived", ErrInvalid)
	}
	return nil
}
func (s *MemStore) CreateCustomRecord(ctx context.Context, scope Scope, v *CustomRecord) (*CustomRecord, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireLiveCustomObject(ctx, scope, v.ObjectType); err != nil {
		return nil, err
	}
	fields, err := CoerceCustomValues(ctx, s, scope, v.ObjectType, v.Fields, nil)
	if err != nil {
		return nil, err
	}
	v.Fields = fields
	for kind, id := range v.Relations {
		if err := requireSubject(ctx, s, scope, "custom record", kind, id); err != nil {
			return nil, err
		}
	}
	return s.customRecord.create(scope, v)
}
func (s *MemStore) GetCustomRecord(_ context.Context, scope Scope, objectType, id string) (*CustomRecord, error) {
	row, err := s.customRecord.get(scope, id)
	if err != nil {
		return nil, err
	}
	if row.ObjectType != normalizeObjectType(objectType) {
		return nil, ErrNotFound
	}
	return row, nil
}
func (s *MemStore) ListCustomRecords(_ context.Context, scope Scope, objectType string, page Page) ([]*CustomRecord, string, error) {
	rows, next, err := s.customRecord.list(scope, page)
	if err != nil {
		return nil, "", err
	}
	out := rows[:0]
	for _, row := range rows {
		if row.ObjectType == normalizeObjectType(objectType) {
			out = append(out, row)
		}
	}
	return out, next, nil
}
func (s *MemStore) UpdateCustomRecord(ctx context.Context, scope Scope, v *CustomRecord) (*CustomRecord, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	current, err := s.GetCustomRecord(ctx, scope, v.ObjectType, v.ID)
	if err != nil {
		return nil, err
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireLiveCustomObject(ctx, scope, v.ObjectType); err != nil {
		return nil, err
	}
	fields, err := CoerceCustomValues(ctx, s, scope, v.ObjectType, v.Fields, current.Fields)
	if err != nil {
		return nil, err
	}
	v.Fields = fields
	for kind, id := range v.Relations {
		if err := requireSubject(ctx, s, scope, "custom record", kind, id); err != nil {
			return nil, err
		}
	}
	return s.customRecord.update(scope, v)
}
func (s *MemStore) DeleteCustomRecord(ctx context.Context, scope Scope, objectType, id string) error {
	if _, err := s.GetCustomRecord(ctx, scope, objectType, id); err != nil {
		return err
	}
	return s.customRecord.delete(scope, id)
}
