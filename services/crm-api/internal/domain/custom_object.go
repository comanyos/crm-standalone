package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type customObjectReader interface {
	GetCustomObjectDefinition(context.Context, Scope, string) (*CustomObjectDefinition, error)
}

func requireKnownCustomObject(ctx context.Context, r customObjectReader, scope Scope, objectType string) error {
	if customObjectTypes[normalizeObjectType(objectType)] {
		return nil
	}
	v, err := r.GetCustomObjectDefinition(ctx, scope, objectType)
	if err != nil {
		return fmt.Errorf("%w: unknown custom object type", ErrInvalid)
	}
	if v.Archived() {
		return fmt.Errorf("%w: custom object is archived", ErrInvalid)
	}
	return nil
}

// CustomObjectDefinition defines a tenant-created record type. ApiName is its
// stable API/storage key; labels are the human-facing names that may change.
type CustomObjectDefinition struct {
	ID          string
	APIName     string
	Label       string
	PluralLabel string
	ArchivedAt  time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (v *CustomObjectDefinition) EntityID() string      { return v.ID }
func (v *CustomObjectDefinition) SetEntityID(id string) { v.ID = id }
func (v *CustomObjectDefinition) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		v.CreatedAt = created
	}
	v.UpdatedAt = updated
}
func (v *CustomObjectDefinition) Validate() error {
	if v == nil {
		return fmt.Errorf("%w: no custom object definition", ErrInvalid)
	}
	v.APIName = strings.ToLower(strings.TrimSpace(v.APIName))
	v.Label = strings.TrimSpace(v.Label)
	v.PluralLabel = strings.TrimSpace(v.PluralLabel)
	if !fieldKeyPattern.MatchString(v.APIName) || customObjectTypes[v.APIName] {
		return fmt.Errorf("%w: invalid custom object API name", ErrInvalid)
	}
	if v.Label == "" || len(v.Label) > 128 || v.PluralLabel == "" || len(v.PluralLabel) > 128 {
		return fmt.Errorf("%w: custom object labels must be 1..128 characters", ErrInvalid)
	}
	return nil
}
func (v *CustomObjectDefinition) Archived() bool { return v != nil && !v.ArchivedAt.IsZero() }
func cloneCustomObjectDefinition(v *CustomObjectDefinition) *CustomObjectDefinition {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// CustomRecord is a row of a tenant-defined object. Relations are named
// built-in object references and are verified against the same tenant on
// every write; custom-to-custom relationships follow after object references
// are available to the search/report graph.
type CustomRecord struct {
	ID         string
	ObjectType string
	Fields     CustomValues
	Relations  map[string]string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (v *CustomRecord) EntityID() string      { return v.ID }
func (v *CustomRecord) SetEntityID(id string) { v.ID = id }
func (v *CustomRecord) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		v.CreatedAt = created
	}
	v.UpdatedAt = updated
}
func (v *CustomRecord) Validate() error {
	if v == nil {
		return fmt.Errorf("%w: no custom record", ErrInvalid)
	}
	v.ObjectType = normalizeObjectType(v.ObjectType)
	if !fieldKeyPattern.MatchString(v.ObjectType) || customObjectTypes[v.ObjectType] {
		return fmt.Errorf("%w: invalid custom object type", ErrInvalid)
	}
	if len(v.Relations) > 16 {
		return fmt.Errorf("%w: more than 16 relations", ErrInvalid)
	}
	for kind, id := range v.Relations {
		if !lookupTargets[kind] || strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: invalid relation", ErrInvalid)
		}
	}
	return nil
}
func cloneCustomRecord(v *CustomRecord) *CustomRecord {
	if v == nil {
		return nil
	}
	c := *v
	c.Fields = v.Fields.Clone()
	if v.Relations != nil {
		c.Relations = make(map[string]string, len(v.Relations))
		for k, value := range v.Relations {
			c.Relations[k] = value
		}
	}
	return &c
}
