package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The metadata layer's definition half (task_v2 Phase 1.1, step 1 of
// docs/DESIGN_METADATA_LAYER.md §8).
//
// A CustomFieldDefinition describes one tenant-authored field on one object.
// The values it describes are NOT here: they live in a `custom_fields` jsonb
// document on the row they belong to, keyed by FieldKey. That split is the
// accepted design's load-bearing decision — a value sits on a row an existing
// RLS policy already covers, so the metadata layer adds no new isolation
// surface for values at all.
//
// Adding a field is an INSERT here. Nothing in this package issues DDL, which
// is what keeps crm_app on DML grants only and keeps one tenant's schema
// changes from taking a lock across every other tenant.

// The data types a custom field may declare. These are the values the
// migration's CHECK constraint enumerates; keep the two in step, and add a new
// one in a new migration rather than editing 0015.
const (
	FieldText          = "text"
	FieldLongText      = "long_text"
	FieldNumber        = "number"
	FieldCurrency      = "currency"
	FieldPercent       = "percent"
	FieldBoolean       = "boolean"
	FieldDate          = "date"
	FieldDateTime      = "datetime"
	FieldPicklist      = "picklist"
	FieldMultiPicklist = "multi_picklist"
	FieldLookup        = "lookup"
	FieldFormula       = "formula"
	FieldRollup        = "rollup"
)

var fieldDataTypes = map[string]bool{
	FieldText: true, FieldLongText: true, FieldNumber: true,
	FieldCurrency: true, FieldPercent: true, FieldBoolean: true,
	FieldDate: true, FieldDateTime: true, FieldPicklist: true,
	FieldMultiPicklist: true, FieldLookup: true, FieldFormula: true,
	FieldRollup: true,
}

// customObjectTypes lists the shipped objects a field may extend today.
// Phase 1.2 adds tenant-defined object types, at which point this becomes a
// lookup against custom_object_definitions rather than a fixed set. It is an
// allowlist for the same reason query.go's sorts are: object_type reaches a
// query, so it never comes straight from a caller.
var customObjectTypes = map[string]bool{
	"account":     true,
	"contact":     true,
	"lead":        true,
	"opportunity": true,
	"task":        true,
	"note":        true,
	"activity":    true,
}

// fieldKeyPattern mirrors the migration's CHECK exactly: lowercase, starting
// with a letter, alphanumeric and underscore, at most 63 characters.
//
// It is enforced in both places on purpose. The database constraint is what
// makes the rule true of the data; this one is what produces an ErrInvalid the
// API can explain, instead of a constraint violation surfacing as a 500. The
// 63-character ceiling is PostgreSQL's identifier limit — a key never becomes
// an identifier under this design, and constraining it as though it might is
// what keeps a later phase free to index on it.
var fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// MaxPicklistOptions bounds a single picklist's option list. Unbounded options
// would let one definition carry an arbitrary payload into every read of every
// record of that object.
const MaxPicklistOptions = 500

// MaxFieldsPerObject bounds how many live fields one tenant may define on one
// object. The values share a jsonb document on each row, so an unbounded field
// count is how a tenant makes its own records expensive to read.
const MaxFieldsPerObject = 200

// CustomFieldDefinition is one tenant-authored field on one object.
type CustomFieldDefinition struct {
	ID string `json:"id"`

	// ObjectType names what this field extends ("account", "contact", …).
	ObjectType string `json:"object_type"`

	// FieldKey is the stable key this field occupies in the owning row's
	// custom_fields document, and it appears verbatim in API payloads. It is
	// immutable after creation: changing it would orphan every value already
	// written under the old key. Label is what a tenant renames instead.
	FieldKey string `json:"field_key"`

	Label    string `json:"label"`
	DataType string `json:"data_type"`

	// Config carries the per-type extras — picklist options, lookup target,
	// decimal scale, formula source. Its shape depends on DataType and is
	// validated by validateFieldConfig, not by a database CHECK, because it
	// varies by type in ways a CHECK cannot express usefully.
	Config FieldConfig `json:"config"`

	// Required rejects a write that omits this field. It is deliberately not
	// a NOT NULL: rows written before the definition existed have no value
	// for it, and retrofitting one would mean inventing data.
	Required bool `json:"required"`

	// ArchivedAt retires the field without deleting it. Deleting a definition
	// would orphan values already written into rows, and an orphaned value is
	// how a "removed" field reappears later still carrying data. An archived
	// field stops being offered for new writes and keeps describing what is
	// already stored.
	ArchivedAt time.Time `json:"archived_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FieldConfig is the per-type extra configuration. Only the members meaningful
// for a given DataType may be set; validateFieldConfig rejects the rest, so a
// definition cannot carry configuration that nothing will ever read.
type FieldConfig struct {
	// Options enumerates the allowed values for picklist and multi_picklist.
	Options []string `json:"options,omitempty"`

	// LookupObjectType names the object a lookup points at. A lookup is the
	// one field type that can create a reference between records, so its
	// target is constrained to a known object here and its value is checked
	// to exist in the same tenant at write time.
	LookupObjectType string `json:"lookup_object_type,omitempty"`

	// Scale is the number of decimal places retained for currency and
	// percent. Those two are transported and stored as exact decimal strings
	// rather than JSON numbers — a JSON number is an IEEE-754 double, and a
	// CRM that rounds an opportunity amount is not acceptable.
	Scale int `json:"scale,omitempty"`

	// Expression is the source of a formula or rollup. It is stored but not
	// evaluated yet: the sandboxed evaluator is step 6 of the design's §8, and
	// accepting an expression before there is something safe to run it in
	// would be storing an unvalidated program.
	Expression string `json:"expression,omitempty"`

	// VisibleToRoles and EditableToRoles are allowlists of CRM membership role
	// keys. Empty means all roles. They are intentionally metadata rather than
	// browser state: API handlers apply them to every read and write.
	VisibleToRoles       []string `json:"visible_to_roles,omitempty"`
	EditableToRoles      []string `json:"editable_to_roles,omitempty"`
	ValidationExpression string   `json:"validation_expression,omitempty"`
}

// MaxDecimalScale bounds currency/percent precision.
const MaxDecimalScale = 12

// EntityID returns the record's identifier.
func (d *CustomFieldDefinition) EntityID() string { return d.ID }

// SetEntityID assigns the record's identifier.
func (d *CustomFieldDefinition) SetEntityID(id string) { d.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (d *CustomFieldDefinition) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		d.CreatedAt = created
	}
	d.UpdatedAt = updated
}

// Created returns the record's creation time.
func (d *CustomFieldDefinition) Created() time.Time { return d.CreatedAt }

// Archived reports whether the field has been retired.
func (d *CustomFieldDefinition) Archived() bool { return d != nil && !d.ArchivedAt.IsZero() }

// Validate rejects a definition that cannot be stored. It normalizes first —
// trimming the label, lowercasing the key and object type — so that a
// definition differing only in whitespace or case cannot slip past the
// uniqueness constraint and claim a second document slot.
func (d *CustomFieldDefinition) Validate() error {
	if d == nil {
		return fmt.Errorf("%w: no custom field definition", ErrInvalid)
	}

	d.ObjectType = strings.ToLower(strings.TrimSpace(d.ObjectType))
	d.FieldKey = strings.ToLower(strings.TrimSpace(d.FieldKey))
	d.Label = strings.TrimSpace(d.Label)
	d.DataType = strings.ToLower(strings.TrimSpace(d.DataType))

	if !fieldKeyPattern.MatchString(d.ObjectType) {
		return fmt.Errorf("%w: invalid object type %q", ErrInvalid, d.ObjectType)
	}
	if !fieldKeyPattern.MatchString(d.FieldKey) {
		return fmt.Errorf(
			"%w: field key %q must start with a letter and contain only lowercase letters, digits and underscores (max 63)",
			ErrInvalid, d.FieldKey)
	}
	if reservedFieldKeys[d.FieldKey] {
		return fmt.Errorf("%w: field key %q is reserved", ErrInvalid, d.FieldKey)
	}
	if d.Label == "" {
		return fmt.Errorf("%w: custom field %q has no label", ErrInvalid, d.FieldKey)
	}
	if len(d.Label) > 128 {
		return fmt.Errorf("%w: custom field %q label is longer than 128 characters", ErrInvalid, d.FieldKey)
	}
	if !fieldDataTypes[d.DataType] {
		return fmt.Errorf("%w: custom field %q has unknown data type %q", ErrInvalid, d.FieldKey, d.DataType)
	}
	return validateFieldConfig(d)
}

// reservedFieldKeys are keys a custom field may not claim, because a reader
// merging custom values with shipped ones would not be able to tell which it
// had. Shipped column names are reserved for the same reason.
var reservedFieldKeys = map[string]bool{
	"id": true, "tenant_id": true, "created_at": true, "updated_at": true,
	"created_by": true, "custom_fields": true, "type": true, "object_type": true,
}

// validateFieldConfig enforces that a definition carries exactly the
// configuration its type uses — no more, and no less.
//
// Rejecting surplus configuration matters as much as requiring the necessary
// kind. A text field carrying picklist options is a definition whose author
// believed something false about it, and storing it silently means the
// misunderstanding surfaces later as "why is this field not a dropdown".
func validateFieldConfig(d *CustomFieldDefinition) error {
	c := &d.Config
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: custom field %q: "+format, append([]any{ErrInvalid, d.FieldKey}, args...)...)
	}

	usesOptions := d.DataType == FieldPicklist || d.DataType == FieldMultiPicklist
	usesScale := d.DataType == FieldCurrency || d.DataType == FieldPercent
	usesLookup := d.DataType == FieldLookup
	usesExpression := d.DataType == FieldFormula || d.DataType == FieldRollup

	if !usesOptions && len(c.Options) > 0 {
		return invalid("a %s field takes no options", d.DataType)
	}
	if !usesScale && c.Scale != 0 {
		return invalid("a %s field takes no scale", d.DataType)
	}
	if !usesLookup && c.LookupObjectType != "" {
		return invalid("a %s field takes no lookup target", d.DataType)
	}
	if !usesExpression && c.Expression != "" {
		return invalid("a %s field takes no expression", d.DataType)
	}
	if err := normalizeRoleAllowlist(&c.VisibleToRoles, "visible_to_roles", invalid); err != nil {
		return err
	}
	if err := normalizeRoleAllowlist(&c.EditableToRoles, "editable_to_roles", invalid); err != nil {
		return err
	}
	if strings.TrimSpace(c.ValidationExpression) != "" {
		if err := ValidateExpression(c.ValidationExpression); err != nil {
			return invalid("invalid validation expression")
		}
		c.ValidationExpression = strings.TrimSpace(c.ValidationExpression)
	}

	switch {
	case usesOptions:
		if len(c.Options) == 0 {
			return invalid("a %s field needs at least one option", d.DataType)
		}
		if len(c.Options) > MaxPicklistOptions {
			return invalid("has %d options, more than the %d allowed", len(c.Options), MaxPicklistOptions)
		}
		seen := make(map[string]bool, len(c.Options))
		normalized := make([]string, 0, len(c.Options))
		for _, option := range c.Options {
			option = strings.TrimSpace(option)
			if option == "" {
				return invalid("has a blank option")
			}
			if len(option) > 128 {
				return invalid("option %q is longer than 128 characters", option)
			}
			// Duplicates would make the stored value ambiguous about which
			// option was chosen once one of them is renamed.
			if seen[option] {
				return invalid("option %q is listed twice", option)
			}
			seen[option] = true
			normalized = append(normalized, option)
		}
		c.Options = normalized

	case usesScale:
		if c.Scale < 0 || c.Scale > MaxDecimalScale {
			return invalid("scale %d is outside 0..%d", c.Scale, MaxDecimalScale)
		}

	case usesLookup:
		c.LookupObjectType = strings.ToLower(strings.TrimSpace(c.LookupObjectType))
		// Narrower than customObjectTypes: a lookup must be resolvable, and
		// lookupTargets is exactly what subjectReader can resolve. Admitting a
		// target the value validator cannot check would mean storing a
		// reference nothing ever verified.
		if !lookupTargets[c.LookupObjectType] {
			return invalid("lookup target %q must be one of account, contact, lead, opportunity", c.LookupObjectType)
		}

	case usesExpression:
		if strings.TrimSpace(c.Expression) == "" {
			return invalid("a %s field needs an expression", d.DataType)
		}
		if d.DataType == FieldRollup {
			return invalid("rollup fields require the reporting aggregation layer and are not available yet")
		}
		if err := ValidateExpression(c.Expression); err != nil {
			return invalid("invalid formula expression")
		}
	}

	return nil
}

// normalizeRoleAllowlist keeps policy configuration bounded and unambiguous.
// Role keys are identity-system data, not caller-supplied expressions, so the
// narrow shape also prevents arbitrary strings becoming durable access rules.
func normalizeRoleAllowlist(values *[]string, name string, invalid func(string, ...any) error) error {
	if len(*values) > 50 {
		return invalid("%s has more than 50 roles", name)
	}
	seen := make(map[string]bool, len(*values))
	normalized := make([]string, 0, len(*values))
	for _, role := range *values {
		role = strings.TrimSpace(role)
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(role) {
			return invalid("%s has invalid role %q", name, role)
		}
		if seen[role] {
			return invalid("%s lists role %q twice", name, role)
		}
		seen[role] = true
		normalized = append(normalized, role)
	}
	*values = normalized
	return nil
}

// MarshalConfig renders Config for the jsonb column. It never emits null: the
// column is NOT NULL with an object CHECK, so an absent config is "{}".
func (d *CustomFieldDefinition) MarshalConfig() ([]byte, error) {
	b, err := json.Marshal(d.Config)
	if err != nil {
		return nil, fmt.Errorf("%w: custom field %q config: %v", ErrInvalid, d.FieldKey, err)
	}
	if len(b) == 0 || string(b) == "null" {
		return []byte("{}"), nil
	}
	return b, nil
}

// cloneCustomFieldDefinition deep-copies a definition, so a caller mutating
// what it got back cannot reach into the store's copy. The Options slice is
// copied explicitly — a shallow struct copy would share its backing array.
func cloneCustomFieldDefinition(v *CustomFieldDefinition) *CustomFieldDefinition {
	if v == nil {
		return nil
	}
	c := *v
	if v.Config.Options != nil {
		c.Config.Options = append([]string(nil), v.Config.Options...)
	}
	if v.Config.VisibleToRoles != nil {
		c.Config.VisibleToRoles = append([]string(nil), v.Config.VisibleToRoles...)
	}
	if v.Config.EditableToRoles != nil {
		c.Config.EditableToRoles = append([]string(nil), v.Config.EditableToRoles...)
	}
	c.Config.ValidationExpression = v.Config.ValidationExpression
	return &c
}

// normalizeObjectType lowercases and trims an object type before it reaches a
// query parameter. It is a value, never an identifier, but the stores compare
// it against normalized stored values, so an unnormalized one would silently
// match nothing rather than erroring.
func normalizeObjectType(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}
