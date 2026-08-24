package domain

import (
	"fmt"
	"strings"
	"time"
)

// Saved views (M4 — Data productivity, migrations/0025_saved_views.sql).
//
// A saved view is a named, personal preset of filters, sort and column
// selection for one entity's list page. It is private by construction: every
// store method here takes an ownerID the same required way
// ListNotifications takes a recipientID (store.go), so there is no query
// shape that can return, update or delete another user's view — sharing a
// view with teammates is a real, wanted feature this version does not have,
// because it needs a decision about who else may read or edit it that
// nobody has made yet (task.md §1.4's record-level authorization gap). A
// scope in a token describes a type of record, never an instance, so "my
// teammate may see this view" cannot be expressed until that lands.
//
// SavedViewObjectTypes is the same shape of explicit allowlist tags.go uses:
// a saved view naming a type nothing renders is rejected at write time.
var SavedViewObjectTypes = map[string]bool{
	"account":     true,
	"contact":     true,
	"lead":        true,
	"opportunity": true,
	"task":        true,
}

// SortedSavedViewObjectTypes returns the allowlist, sorted, for the
// authoring UI to offer.
func SortedSavedViewObjectTypes() []string {
	out := make([]string, 0, len(SavedViewObjectTypes))
	for t := range SavedViewObjectTypes {
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
	maxSavedViewNameLen    = 100
	maxSavedViewFilters    = 20
	maxSavedViewFilterLen  = 500
	maxSavedViewColumns    = 30
	maxSavedViewColumnName = 100
)

// SavedView is one user's named preset for one entity's list page.
//
// Filters is a flat string map rather than a typed per-entity filter struct:
// AccountFilter, ContactFilter and the rest already vary in shape per
// entity, and a saved view exists to remember whatever a caller last put
// into one of them. The API layer is what turns Filters back into a typed
// ListX request for the object_type it names — this layer only stores and
// bounds it.
type SavedView struct {
	ID         string            `json:"id"`
	OwnerID    string            `json:"owner_id"`
	ObjectType string            `json:"object_type"`
	Name       string            `json:"name"`
	Filters    map[string]string `json:"filters"`
	Sort       string            `json:"sort"`
	Columns    []string          `json:"columns"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// Validate rejects a saved view that cannot be stored.
func (v *SavedView) Validate() error {
	if strings.TrimSpace(v.OwnerID) == "" {
		return fmt.Errorf("%w: a saved view needs an owner", ErrInvalid)
	}
	if !SavedViewObjectTypes[v.ObjectType] {
		return fmt.Errorf("%w: %q is not a type of list a saved view can apply to (allowed: %s)",
			ErrInvalid, v.ObjectType, strings.Join(SortedSavedViewObjectTypes(), ", "))
	}
	if strings.TrimSpace(v.Name) == "" {
		return fmt.Errorf("%w: a saved view needs a name", ErrInvalid)
	}
	if len([]rune(v.Name)) > maxSavedViewNameLen {
		return fmt.Errorf("%w: saved view name exceeds %d characters", ErrInvalid, maxSavedViewNameLen)
	}
	if len(v.Filters) > maxSavedViewFilters {
		return fmt.Errorf("%w: a saved view may have at most %d filters", ErrInvalid, maxSavedViewFilters)
	}
	for key, value := range v.Filters {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: a saved view filter key cannot be blank", ErrInvalid)
		}
		if len([]rune(value)) > maxSavedViewFilterLen {
			return fmt.Errorf("%w: saved view filter %q exceeds %d characters", ErrInvalid, key, maxSavedViewFilterLen)
		}
	}
	if len(v.Columns) > maxSavedViewColumns {
		return fmt.Errorf("%w: a saved view may select at most %d columns", ErrInvalid, maxSavedViewColumns)
	}
	for _, column := range v.Columns {
		if len([]rune(column)) > maxSavedViewColumnName {
			return fmt.Errorf("%w: saved view column name exceeds %d characters", ErrInvalid, maxSavedViewColumnName)
		}
	}
	return nil
}

func cloneSavedView(v *SavedView) *SavedView {
	if v == nil {
		return nil
	}
	c := *v
	if v.Filters != nil {
		c.Filters = make(map[string]string, len(v.Filters))
		for k, val := range v.Filters {
			c.Filters[k] = val
		}
	}
	c.Columns = append([]string(nil), v.Columns...)
	return &c
}
