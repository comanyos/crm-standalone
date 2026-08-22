package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MemStore's half of the metadata layer's definitions (task_v2 Phase 1.1).
//
// It does not reuse memTable's generic create/update, because uniqueness on
// (object_type, field_key) and the immutability of the three identity fields
// are properties memTable knows nothing about. PgStore gets uniqueness from
// the migration's constraint; this store has to enforce the same rule itself,
// or a MemStore-backed test suite stays green while the deployed service
// rejects the write.

// CreateCustomFieldDefinition stores a new field definition.
func (s *MemStore) CreateCustomFieldDefinition(
	ctx context.Context, scope Scope, v *CustomFieldDefinition,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := requireKnownCustomObject(ctx, s, scope, v.ObjectType); err != nil {
		return nil, err
	}

	existing, err := s.customField.all(scope)
	if err != nil {
		return nil, err
	}
	live := 0
	for _, d := range existing {
		if d.ObjectType != v.ObjectType {
			continue
		}
		if !d.Archived() {
			live++
		}
		// An archived definition still owns its key: its values are still in
		// rows, so a new field reusing the key would inherit them.
		if d.FieldKey == v.FieldKey {
			return nil, fmt.Errorf("%w: custom field %q already exists on %s", ErrConflict, v.FieldKey, v.ObjectType)
		}
	}
	if live >= MaxFieldsPerObject {
		return nil, fmt.Errorf("%w: %s already has the maximum of %d custom fields", ErrConflict, v.ObjectType, MaxFieldsPerObject)
	}

	// Creation never accepts an archive stamp from the caller: a field that
	// arrives already retired is a definition nothing can ever write to.
	v.ArchivedAt = time.Time{}
	return s.customField.create(scope, v)
}

// GetCustomFieldDefinition returns one definition by id.
func (s *MemStore) GetCustomFieldDefinition(
	_ context.Context, scope Scope, id string,
) (*CustomFieldDefinition, error) {
	return s.customField.get(scope, id)
}

// ListCustomFieldDefinitions returns one object's definitions, by field key.
func (s *MemStore) ListCustomFieldDefinitions(
	_ context.Context, scope Scope, objectType string, includeArchived bool,
) ([]*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	objectType = strings.ToLower(strings.TrimSpace(objectType))
	all, err := s.customField.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*CustomFieldDefinition, 0, len(all))
	for _, d := range all {
		if d.ObjectType != objectType {
			continue
		}
		if d.Archived() && !includeArchived {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FieldKey < out[j].FieldKey })
	return out, nil
}

// UpdateCustomFieldDefinition changes what a field means without changing what
// it is.
func (s *MemStore) UpdateCustomFieldDefinition(
	_ context.Context, scope Scope, v *CustomFieldDefinition,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	current, err := s.customField.get(scope, v.EntityID())
	if err != nil {
		return nil, err
	}
	if err := applyCustomFieldUpdate(current, v); err != nil {
		return nil, err
	}
	return s.customField.update(scope, current)
}

// ArchiveCustomFieldDefinition retires a field. Idempotent.
func (s *MemStore) ArchiveCustomFieldDefinition(
	_ context.Context, scope Scope, id string,
) (*CustomFieldDefinition, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	current, err := s.customField.get(scope, id)
	if err != nil {
		return nil, err
	}
	if current.Archived() {
		return current, nil
	}
	current.ArchivedAt = nowStamp()
	return s.customField.update(scope, current)
}

// applyCustomFieldUpdate merges the mutable half of an incoming definition
// onto the stored one, and rejects a change to the immutable half.
//
// The three immutable fields each orphan data if changed. ObjectType would
// move the field to a different object, leaving its values behind on rows of
// the old one. FieldKey is the document key, so changing it strands every
// value already written. DataType would reinterpret stored values as a type
// they were never validated against — the one that turns a picklist's stored
// strings into "numbers" nothing can parse.
//
// A tenant that wants any of those creates a new field and archives this one,
// which keeps the old values readable and attributable.
func applyCustomFieldUpdate(current, incoming *CustomFieldDefinition) error {
	if incoming == nil {
		return fmt.Errorf("%w: no custom field definition", ErrInvalid)
	}
	if got := strings.ToLower(strings.TrimSpace(incoming.ObjectType)); got != "" && got != current.ObjectType {
		return fmt.Errorf("%w: a custom field's object type cannot change (create a new field on %s instead)", ErrInvalid, got)
	}
	if got := strings.ToLower(strings.TrimSpace(incoming.FieldKey)); got != "" && got != current.FieldKey {
		return fmt.Errorf("%w: a custom field's key cannot change: values already stored under %q would be orphaned", ErrInvalid, current.FieldKey)
	}
	if got := strings.ToLower(strings.TrimSpace(incoming.DataType)); got != "" && got != current.DataType {
		return fmt.Errorf("%w: a custom field's data type cannot change: values already stored as %s were never validated as %s",
			ErrInvalid, current.DataType, got)
	}

	updated := *current
	updated.Label = incoming.Label
	updated.Required = incoming.Required
	updated.Config = incoming.Config
	if err := updated.Validate(); err != nil {
		return err
	}

	current.Label = updated.Label
	current.Required = updated.Required
	current.Config = updated.Config
	return nil
}
