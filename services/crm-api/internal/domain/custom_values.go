package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The metadata layer's value half (task_v2 Phase 1.1, step 3 of
// docs/DESIGN_METADATA_LAYER.md §8).
//
// Option A of the accepted design trades database-level typing for an
// unchanged isolation story. That trade is only sound if the write boundary is
// genuinely strict rather than nominally strict, which is what this file is.
// Nothing else in the tree may construct a CustomValues document: every value
// that reaches storage has come through CoerceCustomValues and been checked
// against the definition that describes it.

// CustomValues is a record's custom-field document.
//
// Every value is a JSON-native type chosen by the field's DataType — string,
// float64, bool, or []string — so the document marshals to the jsonb column
// unchanged and survives the round trip. The map is only ever produced by
// CoerceCustomValues; a document assembled any other way has not been
// validated and must not be stored.
type CustomValues map[string]any

// Ceilings on one record's document. The values share a row with the record
// itself, so an unbounded document is how one tenant makes its own records
// expensive to read for everybody sharing the table.
const (
	// MaxCustomDocumentBytes caps the encoded document per record.
	MaxCustomDocumentBytes = 64 * 1024
	// MaxCustomTextLength caps a text field's value.
	MaxCustomTextLength = 1000
	// MaxCustomLongTextLength caps a long_text field's value.
	MaxCustomLongTextLength = 32000
	// MaxMultiPicklistSelections caps how many options one value may select.
	MaxMultiPicklistSelections = 100
	// MaxDecimalDigits caps the total significant digits of an exact decimal.
	MaxDecimalDigits = 28
)

// decimalPattern accepts an exact decimal: an optional sign, digits, and an
// optional fractional part. Deliberately no exponent — "1e3" is a float
// literal, and admitting float notation into a value whose entire purpose is
// to avoid float rounding would defeat the point.
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// lookupTargets are the object types a lookup field may point at.
//
// Narrower than customObjectTypes on purpose: these four are exactly what
// subjectReader can resolve, so a lookup reuses the existence check that
// Activity, Task and Note subjects already use rather than growing a second,
// wider reader that would need its own tenant-safety argument.
var lookupTargets = map[string]bool{
	SubjectAccount:     true,
	SubjectContact:     true,
	SubjectLead:        true,
	SubjectOpportunity: true,
}

// customFieldReader is what value validation needs from a store: the live
// definitions for an object, and the ability to resolve a lookup target.
type customFieldReader interface {
	subjectReader
	ListCustomFieldDefinitions(ctx context.Context, scope Scope, objectType string, includeArchived bool) ([]*CustomFieldDefinition, error)
}

// CoerceCustomValues validates a caller-supplied document against the live
// definitions for objectType and returns the document to store.
//
// existing is the record's current document on an update, and nil on a create.
//
// The document is REPLACED, not merged: a live field absent from incoming is
// cleared. That matches how the rest of this package updates — UpdateAccount
// and its siblings take the whole record — so a caller that round-trips what
// it read keeps its values, and one that sends a partial document is asking
// for the omitted fields to be empty. A merge would instead make clearing a
// field impossible.
//
// The exception is archived fields, whose values are carried forward from
// existing. They can no longer be written, so replacement would delete them
// with no way for the caller to prevent it — a tenant's data disappearing as
// a side effect of retiring a field.
//
// The rules, and why each one is a rule:
//
//   - An unknown key is rejected, never stored. A jsonb column that accepts
//     arbitrary keys is an undocumented schema, and it is also the obvious
//     place to smuggle data past the field-level security that Phase 1.4
//     applies by field.
//   - A value for an archived field is rejected. The definition that
//     described it is retired; accepting a new value under it would create
//     data nothing is willing to explain.
//   - formula and rollup fields are computed, never written. A caller
//     supplying one is asserting a value the server is supposed to derive.
//   - A required field must be present and non-empty.
//   - Types are coerced, not merely checked, so what reaches storage is the
//     canonical form and two writers cannot disagree about representation.
func CoerceCustomValues(
	ctx context.Context,
	r customFieldReader,
	scope Scope,
	objectType string,
	incoming CustomValues,
	existing CustomValues,
) (CustomValues, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	objectType = normalizeObjectType(objectType)

	definitions, err := r.ListCustomFieldDefinitions(ctx, scope, objectType, true)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*CustomFieldDefinition, len(definitions))
	for _, d := range definitions {
		byKey[d.FieldKey] = d
	}

	out := make(CustomValues, len(incoming))

	// Values under archived definitions survive untouched. They are not
	// writable, so they can only have come from a time when the field was
	// live.
	for key, value := range existing {
		if d, ok := byKey[key]; ok && d.Archived() {
			out[key] = value
		}
	}

	for _, key := range sortedKeys(incoming) {
		definition, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("%w: %s has no custom field %q", ErrInvalid, objectType, key)
		}
		if definition.Archived() {
			return nil, fmt.Errorf("%w: custom field %q on %s is archived and cannot be written",
				ErrInvalid, key, objectType)
		}
		if definition.DataType == FieldFormula || definition.DataType == FieldRollup {
			return nil, fmt.Errorf("%w: custom field %q is a %s and is computed, not written",
				ErrInvalid, key, definition.DataType)
		}

		coerced, err := coerceCustomValue(ctx, r, scope, definition, incoming[key])
		if err != nil {
			return nil, err
		}
		if coerced == nil {
			// An explicit null clears the field rather than storing a null,
			// so a reader never has to distinguish "absent" from "present but
			// empty" for the same meaning.
			continue
		}
		out[key] = coerced
	}

	for _, definition := range definitions {
		if definition.Archived() || !definition.Required {
			continue
		}
		if definition.DataType == FieldFormula || definition.DataType == FieldRollup {
			continue
		}
		if _, ok := out[definition.FieldKey]; !ok {
			return nil, fmt.Errorf("%w: custom field %q is required on %s",
				ErrInvalid, definition.FieldKey, objectType)
		}
	}
	for _, definition := range definitions {
		if definition.Archived() || definition.Config.ValidationExpression == "" {
			continue
		}
		value, err := EvaluateExpression(definition.Config.ValidationExpression, out)
		if err != nil {
			return nil, fmt.Errorf("%w: validation rule for custom field %q cannot be evaluated", ErrInvalid, definition.FieldKey)
		}
		valid, ok := value.(bool)
		if !ok || !valid {
			return nil, fmt.Errorf("%w: validation rule for custom field %q rejected this record", ErrInvalid, definition.FieldKey)
		}
	}

	if len(out) == 0 {
		// An empty document is stored as {} rather than omitted, matching the
		// column's NOT NULL DEFAULT and its object-shape CHECK.
		return CustomValues{}, nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("%w: custom fields: %v", ErrInvalid, err)
	}
	if len(encoded) > MaxCustomDocumentBytes {
		return nil, fmt.Errorf("%w: custom fields for this %s are %d bytes, over the %d byte limit",
			ErrInvalid, objectType, len(encoded), MaxCustomDocumentBytes)
	}
	return out, nil
}

// coerceCustomValue converts one caller-supplied value into the canonical form
// its definition calls for. A nil return means "clear this field".
func coerceCustomValue(
	ctx context.Context,
	r customFieldReader,
	scope Scope,
	d *CustomFieldDefinition,
	raw any,
) (any, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: custom field %q: "+format, append([]any{ErrInvalid, d.FieldKey}, args...)...)
	}
	if raw == nil {
		return nil, nil
	}

	switch d.DataType {
	case FieldText, FieldLongText:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid("expected text, got %s", jsonKind(raw))
		}
		limit := MaxCustomTextLength
		if d.DataType == FieldLongText {
			limit = MaxCustomLongTextLength
		}
		if len(s) > limit {
			return nil, invalid("is %d characters, over the %d limit", len(s), limit)
		}
		if s == "" {
			return nil, nil
		}
		return s, nil

	case FieldNumber:
		f, err := toFloat(raw)
		if err != nil {
			return nil, invalid("expected a number, got %s", jsonKind(raw))
		}
		return f, nil

	case FieldCurrency, FieldPercent:
		// Exact decimals travel as strings. A JSON number is an IEEE-754
		// double, and a CRM that rounds an opportunity amount because of its
		// transport is not acceptable — see the design's §2 and §5.
		s, ok := raw.(string)
		if !ok {
			return nil, invalid(
				"expected an exact decimal as a string (a JSON number would round), got %s", jsonKind(raw))
		}
		return coerceDecimal(s, d.Config.Scale, invalid)

	case FieldBoolean:
		b, ok := raw.(bool)
		if !ok {
			return nil, invalid("expected true or false, got %s", jsonKind(raw))
		}
		return b, nil

	case FieldDate:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid("expected a YYYY-MM-DD date, got %s", jsonKind(raw))
		}
		if s == "" {
			return nil, nil
		}
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return nil, invalid("%q is not a YYYY-MM-DD date", s)
		}
		return s, nil

	case FieldDateTime:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid("expected an RFC 3339 timestamp, got %s", jsonKind(raw))
		}
		if s == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, invalid("%q is not an RFC 3339 timestamp", s)
		}
		// Stored in UTC at the precision the rest of this package keeps, so
		// two clients writing the same instant in different zones store the
		// same string.
		return parsed.UTC().Truncate(storedTimePrecision).Format(time.RFC3339Nano), nil

	case FieldPicklist:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid("expected one of its options, got %s", jsonKind(raw))
		}
		if s == "" {
			return nil, nil
		}
		if !containsOption(d.Config.Options, s) {
			return nil, invalid("%q is not one of its options", s)
		}
		return s, nil

	case FieldMultiPicklist:
		selections, err := toStringSlice(raw)
		if err != nil {
			return nil, invalid("expected a list of its options, got %s", jsonKind(raw))
		}
		if len(selections) == 0 {
			return nil, nil
		}
		if len(selections) > MaxMultiPicklistSelections {
			return nil, invalid("selects %d options, over the %d limit",
				len(selections), MaxMultiPicklistSelections)
		}
		seen := make(map[string]bool, len(selections))
		for _, s := range selections {
			if !containsOption(d.Config.Options, s) {
				return nil, invalid("%q is not one of its options", s)
			}
			if seen[s] {
				return nil, invalid("selects %q twice", s)
			}
			seen[s] = true
		}
		return selections, nil

	case FieldLookup:
		s, ok := raw.(string)
		if !ok {
			return nil, invalid("expected a record id, got %s", jsonKind(raw))
		}
		if s == "" {
			return nil, nil
		}
		// A lookup is the one custom field type that can name another record,
		// so it gets the treatment ParentID and Activity subjects already
		// get: resolved through the caller's own scope, which makes a real
		// record in another tenant indistinguishable from one that does not
		// exist.
		if err := requireSubject(ctx, r, scope, "custom field "+d.FieldKey, d.Config.LookupObjectType, s); err != nil {
			return nil, err
		}
		return s, nil
	}

	return nil, invalid("has unsupported data type %q", d.DataType)
}

// coerceDecimal validates an exact decimal string and normalizes it to the
// definition's scale, so two writers cannot store "1.5" and "1.50" for the
// same amount and have them compare unequal.
func coerceDecimal(s string, scale int, invalid func(string, ...any) error) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !decimalPattern.MatchString(s) {
		return nil, invalid("%q is not an exact decimal", s)
	}

	negative := strings.HasPrefix(s, "-")
	digits := strings.TrimPrefix(s, "-")
	whole, fraction, _ := strings.Cut(digits, ".")
	if len(whole)+len(fraction) > MaxDecimalDigits {
		return nil, invalid("has %d significant digits, over the %d limit",
			len(whole)+len(fraction), MaxDecimalDigits)
	}
	if len(fraction) > scale {
		return nil, invalid("has %d decimal places, more than the %d this field keeps",
			len(fraction), scale)
	}
	// Pad rather than truncate: padding is lossless, and truncating here
	// would silently discard precision the caller sent.
	fraction += strings.Repeat("0", scale-len(fraction))

	normalized := whole
	if scale > 0 {
		normalized += "." + fraction
	}
	// Negative zero is a representation, not a value.
	if negative && strings.Trim(normalized, "0.") != "" {
		normalized = "-" + normalized
	}
	return normalized, nil
}

// UnmarshalJSON restores a document read back from jsonb.
//
// Plain json.Unmarshal into map[string]any yields []any for a multi-picklist,
// which would make a value that round-tripped through the database compare
// unequal to the one that was written. Normalizing here keeps a stored
// document and a freshly coerced one the same shape.
func (v *CustomValues) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		*v = nil
		return nil
	}
	out := make(CustomValues, len(raw))
	for key, value := range raw {
		if list, err := toStringSlice(value); err == nil {
			if _, isSlice := value.([]any); isSlice {
				out[key] = list
				continue
			}
		}
		out[key] = value
	}
	*v = out
	return nil
}

// MarshalDocument renders the document for the jsonb column. A nil or empty
// document is "{}" rather than null, matching the column's NOT NULL DEFAULT
// and its object-shape CHECK.
func (v CustomValues) MarshalDocument() ([]byte, error) {
	if len(v) == 0 {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(map[string]any(v))
	if err != nil {
		return nil, fmt.Errorf("%w: custom fields: %v", ErrInvalid, err)
	}
	return b, nil
}

// parseCustomValues decodes one jsonb document read from PostgreSQL. Database
// rows are trusted to satisfy the object-shape CHECK from migration 0016, but
// decoding still goes through CustomValues.UnmarshalJSON so multi-picklists
// regain their []string representation rather than leaking []any to callers.
func parseCustomValues(document []byte) (CustomValues, error) {
	values := CustomValues{}
	if len(document) == 0 {
		return values, nil
	}
	if err := values.UnmarshalJSON(document); err != nil {
		return nil, fmt.Errorf("decode custom fields: %w", err)
	}
	return values, nil
}

// Clone deep-copies a document so a caller mutating what it got back cannot
// reach into the store's copy. Slice values are copied explicitly; a shallow
// map copy would share their backing arrays.
func (v CustomValues) Clone() CustomValues {
	if v == nil {
		return nil
	}
	out := make(CustomValues, len(v))
	for key, value := range v {
		if list, ok := value.([]string); ok {
			out[key] = append([]string(nil), list...)
			continue
		}
		out[key] = value
	}
	return out
}

func containsOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}

// toFloat accepts the several shapes a JSON number arrives in depending on
// whether it came from a decoder, a proto conversion, or Go code.
func toFloat(raw any) (float64, error) {
	switch n := raw.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	}
	return 0, fmt.Errorf("not a number")
}

func toStringSlice(raw any) ([]string, error) {
	switch list := raw.(type) {
	case []string:
		return append([]string(nil), list...), nil
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("not a list of strings")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("not a list")
}

// jsonKind names the shape of a value for an error message. It deliberately
// reports the kind and never the value: an error about a custom field must not
// echo the tenant's data back into a log or a response.
func jsonKind(raw any) string {
	switch raw.(type) {
	case nil:
		return "null"
	case string:
		return "text"
	case bool:
		return "a boolean"
	case float64, float32, int, int32, int64, json.Number:
		return "a number"
	case []any, []string:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return "an unsupported value"
}

// sortedKeys makes validation order deterministic, so a document with two
// problems always reports the same one first.
func sortedKeys(v CustomValues) []string {
	out := make([]string, 0, len(v))
	for key := range v {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
