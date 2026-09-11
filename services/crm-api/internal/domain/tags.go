package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tags (M4 — Data productivity, migrations/0024_tags.sql).
//
// A tag is a tenant-authored label; an assignment attaches one to one record.
// Both are deliberately small: a tag has a name and an optional color, and an
// assignment is nothing but the pair (tag, record) plus who made it. There is
// no hierarchy, no tag-of-a-tag, and no cross-tenant sharing — those are
// product decisions nobody has made, not omissions.

// TaggableObjectTypes is the explicit allowlist of record kinds that may
// carry a tag assignment. A tag assignment naming anything outside it is
// rejected at write time, so a typo or a future entity nobody decided to tag
// cannot silently accumulate rows nothing will ever query back out.
var TaggableObjectTypes = map[string]bool{
	"account":     true,
	"contact":     true,
	"lead":        true,
	"opportunity": true,
	"task":        true,
}

// SortedTaggableObjectTypes returns the allowlist, sorted, for the authoring
// UI to offer.
func SortedTaggableObjectTypes() []string {
	out := make([]string, 0, len(TaggableObjectTypes))
	for t := range TaggableObjectTypes {
		out = append(out, t)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

const (
	maxTagNameLen  = 100
	maxTagColorLen = 7 // "#rrggbb"
)

// TagDefinition is one tenant-authored label.
type TagDefinition struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate rejects a tag definition that cannot be stored.
func (t *TagDefinition) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("%w: a tag needs a name", ErrInvalid)
	}
	if len([]rune(t.Name)) > maxTagNameLen {
		return fmt.Errorf("%w: tag name exceeds %d characters", ErrInvalid, maxTagNameLen)
	}
	if t.Color != "" && !looksLikeHexColor(t.Color) {
		return fmt.Errorf("%w: tag color must be a #rrggbb hex code", ErrInvalid)
	}
	return nil
}

// looksLikeHexColor is a deliberately light check, matching looksLikeEmail's
// posture elsewhere in this package: it catches an obviously wrong value
// without trying to be a full CSS color parser.
func looksLikeHexColor(s string) bool {
	if len(s) != maxTagColorLen || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func cloneTagDefinition(v *TagDefinition) *TagDefinition {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// TagAssignment attaches one TagDefinition to one record.
type TagAssignment struct {
	ID         string    `json:"id"`
	TagID      string    `json:"tag_id"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// Validate rejects a tag assignment that cannot be stored.
func (a *TagAssignment) Validate() error {
	if strings.TrimSpace(a.TagID) == "" {
		return fmt.Errorf("%w: a tag assignment needs a tag", ErrInvalid)
	}
	if !TaggableObjectTypes[a.ObjectType] {
		return fmt.Errorf("%w: %q is not a taggable record type (allowed: %s)",
			ErrInvalid, a.ObjectType, strings.Join(SortedTaggableObjectTypes(), ", "))
	}
	if strings.TrimSpace(a.ObjectID) == "" {
		return fmt.Errorf("%w: a tag assignment needs a record id", ErrInvalid)
	}
	return nil
}

func cloneTagAssignment(v *TagAssignment) *TagAssignment {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// taggableObjectReader is what requireTaggableObject needs from a store: one
// tenant-scoped Get per taggable type. subjectReader already covers four of
// the five; task.go's Task is the one taggable type that is not also a
// polymorphic subject, so it is added explicitly.
type taggableObjectReader interface {
	subjectReader
	GetTask(ctx context.Context, scope Scope, id string) (*Task, error)
}

// requireTaggableObject confirms objectID names a real record of objectType
// in the caller's own tenant before AssignTag lets a tag point at it.
//
// Without this, a tag assignment naming an id nothing resolves would insert
// silently — the row satisfies every constraint the schema can express, and
// nothing would ever surface it as wrong until a UI tried to render "the
// tagged account" and found none. The lookup always goes through the
// caller's own scope, so a real record belonging to another tenant is
// indistinguishable from one that does not exist — the same property
// requireSubject holds for Activity/Task/Note subjects.
func requireTaggableObject(ctx context.Context, r taggableObjectReader, scope Scope, objectType, objectID string) error {
	var err error
	switch objectType {
	case "account":
		_, err = r.GetAccount(ctx, scope, objectID)
	case "contact":
		_, err = r.GetContact(ctx, scope, objectID)
	case "lead":
		_, err = r.GetLead(ctx, scope, objectID)
	case "opportunity":
		_, err = r.GetOpportunity(ctx, scope, objectID)
	case "task":
		_, err = r.GetTask(ctx, scope, objectID)
	default:
		return fmt.Errorf("%w: %q is not a taggable record type (allowed: %s)",
			ErrInvalid, objectType, strings.Join(SortedTaggableObjectTypes(), ", "))
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: no %s %q exists in this tenant", ErrInvalid, objectType, objectID)
	}
	return err
}
