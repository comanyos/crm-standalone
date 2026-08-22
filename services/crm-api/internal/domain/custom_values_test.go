package domain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// valueFixture builds a store with one tenant and the definitions a test asks
// for, and returns everything a coercion call needs.
func valueFixture(t *testing.T, definitions ...*CustomFieldDefinition) (*MemStore, context.Context, Scope) {
	t.Helper()
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}
	for _, d := range definitions {
		if _, err := s.CreateCustomFieldDefinition(ctx, scope, d); err != nil {
			t.Fatalf("seed definition %q: %v", d.FieldKey, err)
		}
	}
	return s, ctx, scope
}

func def(key, dataType string, config FieldConfig) *CustomFieldDefinition {
	return &CustomFieldDefinition{
		ObjectType: "account", FieldKey: key, Label: key, DataType: dataType, Config: config,
	}
}

func TestCoerceCustomValuesByType(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition *CustomFieldDefinition
		in         any
		want       any
		wantErr    string
	}{
		{"text", def("k", FieldText, FieldConfig{}), "hello", "hello", ""},
		{"text rejects a number", def("k", FieldText, FieldConfig{}), 42.0, nil, "expected text, got a number"},
		{"text over the limit", def("k", FieldText, FieldConfig{}), strings.Repeat("a", MaxCustomTextLength+1), nil, "over the 1000 limit"},
		{"empty text clears", def("k", FieldText, FieldConfig{}), "", nil, ""},

		{"number", def("k", FieldNumber, FieldConfig{}), 42.5, 42.5, ""},
		{"number from int", def("k", FieldNumber, FieldConfig{}), 7, 7.0, ""},
		{"number rejects text", def("k", FieldNumber, FieldConfig{}), "42", nil, "expected a number, got text"},

		{"boolean", def("k", FieldBoolean, FieldConfig{}), true, true, ""},
		{"boolean rejects text", def("k", FieldBoolean, FieldConfig{}), "true", nil, "expected true or false, got text"},

		{"date", def("k", FieldDate, FieldConfig{}), "2026-08-22", "2026-08-22", ""},
		{"date rejects a timestamp", def("k", FieldDate, FieldConfig{}), "2026-08-22T10:00:00Z", nil, "is not a YYYY-MM-DD date"},
		{"date rejects nonsense", def("k", FieldDate, FieldConfig{}), "not-a-date", nil, "is not a YYYY-MM-DD date"},

		{"datetime normalizes to UTC", def("k", FieldDateTime, FieldConfig{}),
			"2026-08-22T12:00:00+02:00", "2026-08-22T10:00:00Z", ""},
		{"datetime rejects a bare date", def("k", FieldDateTime, FieldConfig{}), "2026-08-22", nil, "is not an RFC 3339 timestamp"},

		{"picklist", def("k", FieldPicklist, FieldConfig{Options: []string{"A", "B"}}), "A", "A", ""},
		{"picklist rejects an unlisted option",
			def("k", FieldPicklist, FieldConfig{Options: []string{"A"}}), "Z", nil, "is not one of its options"},

		{"multi picklist", def("k", FieldMultiPicklist, FieldConfig{Options: []string{"A", "B"}}),
			[]any{"A", "B"}, []string{"A", "B"}, ""},
		{"multi picklist rejects a duplicate",
			def("k", FieldMultiPicklist, FieldConfig{Options: []string{"A"}}), []any{"A", "A"}, nil, "selects \"A\" twice"},
		{"multi picklist rejects an unlisted option",
			def("k", FieldMultiPicklist, FieldConfig{Options: []string{"A"}}), []any{"Z"}, nil, "is not one of its options"},
		{"multi picklist rejects a bare string",
			def("k", FieldMultiPicklist, FieldConfig{Options: []string{"A"}}), "A", nil, "expected a list of its options"},

		// The design's §2 and §5 both turn on this: a JSON number is an
		// IEEE-754 double and would round an amount.
		{"currency", def("k", FieldCurrency, FieldConfig{Scale: 2}), "1234.56", "1234.56", ""},
		{"currency pads to scale", def("k", FieldCurrency, FieldConfig{Scale: 2}), "1234.5", "1234.50", ""},
		{"currency pads a whole number", def("k", FieldCurrency, FieldConfig{Scale: 2}), "1234", "1234.00", ""},
		{"currency keeps a negative", def("k", FieldCurrency, FieldConfig{Scale: 2}), "-1234.5", "-1234.50", ""},
		{"currency rejects a JSON number", def("k", FieldCurrency, FieldConfig{Scale: 2}), 1234.56, nil,
			"expected an exact decimal as a string (a JSON number would round)"},
		{"currency rejects excess precision", def("k", FieldCurrency, FieldConfig{Scale: 2}), "1.005", nil,
			"has 3 decimal places, more than the 2 this field keeps"},
		{"currency rejects exponent notation", def("k", FieldCurrency, FieldConfig{Scale: 2}), "1e3", nil,
			"is not an exact decimal"},
		{"currency rejects a huge value", def("k", FieldCurrency, FieldConfig{Scale: 2}),
			strings.Repeat("9", 30) + ".00", nil, "significant digits, over the 28 limit"},
		{"zero scale currency", def("k", FieldCurrency, FieldConfig{Scale: 0}), "1234", "1234", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx, scope := valueFixture(t, tc.definition)
			got, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{"k": tc.in}, nil)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got %#v", tc.wantErr, got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error should wrap ErrInvalid, got %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want == nil {
				if _, present := got["k"]; present {
					t.Fatalf("expected the field to be cleared, got %#v", got["k"])
				}
				return
			}
			if !jsonEqual(t, got["k"], tc.want) {
				t.Fatalf("got %#v, want %#v", got["k"], tc.want)
			}
		})
	}
}

// A jsonb column that accepts arbitrary keys is an undocumented schema, and
// the obvious place to smuggle data past per-field security.
func TestCoerceCustomValuesRejectsUnknownKeys(t *testing.T) {
	s, ctx, scope := valueFixture(t, def("known", FieldText, FieldConfig{}))

	_, err := CoerceCustomValues(ctx, s, scope, "account",
		CustomValues{"known": "ok", "smuggled": "payload"}, nil)
	if err == nil || !strings.Contains(err.Error(), `has no custom field "smuggled"`) {
		t.Fatalf("expected the unknown key to be rejected, got %v", err)
	}
}

func TestCoerceCustomValuesEnforcesRequired(t *testing.T) {
	required := def("region", FieldText, FieldConfig{})
	required.Required = true
	s, ctx, scope := valueFixture(t, required)

	if _, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{}, nil); err == nil ||
		!strings.Contains(err.Error(), `custom field "region" is required`) {
		t.Fatalf("expected required to be enforced, got %v", err)
	}
	// An explicit empty value clears the field, so it must fail the same way
	// rather than storing an empty string that satisfies nothing.
	if _, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{"region": ""}, nil); err == nil {
		t.Fatal("an empty value must not satisfy a required field")
	}
	if _, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{"region": "EMEA"}, nil); err != nil {
		t.Fatalf("a supplied required value should pass: %v", err)
	}
}

// Retiring a field must not delete what it already holds, but must stop new
// writes to it.
func TestArchivedFieldsAreCarriedForwardButNotWritable(t *testing.T) {
	s, ctx, scope := valueFixture(t, def("region", FieldText, FieldConfig{}), def("tier", FieldText, FieldConfig{}))

	stored, err := CoerceCustomValues(ctx, s, scope, "account",
		CustomValues{"region": "EMEA", "tier": "gold"}, nil)
	if err != nil {
		t.Fatalf("initial write: %v", err)
	}

	live, err := s.ListCustomFieldDefinitions(ctx, scope, "account", false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, d := range live {
		if d.FieldKey == "region" {
			if _, err := s.ArchiveCustomFieldDefinition(ctx, scope, d.ID); err != nil {
				t.Fatalf("archive: %v", err)
			}
		}
	}

	// Writing to the archived field is refused.
	if _, err := CoerceCustomValues(ctx, s, scope, "account",
		CustomValues{"region": "AMER"}, stored); err == nil ||
		!strings.Contains(err.Error(), "is archived and cannot be written") {
		t.Fatalf("expected a write to an archived field to be refused, got %v", err)
	}

	// An unrelated update keeps the archived field's existing value.
	next, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{"tier": "silver"}, stored)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if next["region"] != "EMEA" {
		t.Fatalf("archiving a field deleted its stored value: %#v", next)
	}
	if next["tier"] != "silver" {
		t.Fatalf("update did not apply: %#v", next)
	}
}

// Computed fields are derived by the server; a caller supplying one is
// asserting a value it does not get to decide.
func TestComputedFieldsCannotBeWritten(t *testing.T) {
	s, ctx, scope := valueFixture(t)

	// Formula definitions cannot be created yet, so reach past the store to
	// prove the value path refuses them independently of that.
	s.customField.rows[scope.TenantID] = map[string]*CustomFieldDefinition{
		"f1": {ID: "f1", ObjectType: "account", FieldKey: "score", Label: "Score", DataType: FieldFormula},
	}
	if _, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{"score": 1.0}, nil); err == nil ||
		!strings.Contains(err.Error(), "is computed, not written") {
		t.Fatalf("expected a computed field to be unwritable, got %v", err)
	}
}

// A lookup is the one custom field type that can name another record.
func TestLookupValuesAreResolvedInTheCallersTenant(t *testing.T) {
	s, ctx := NewMemStore(), context.Background()
	a, b := Scope{TenantID: "tenant-a"}, Scope{TenantID: "tenant-b"}

	for _, scope := range []Scope{a, b} {
		if _, err := s.CreateCustomFieldDefinition(ctx, scope,
			def("primary_contact", FieldLookup, FieldConfig{LookupObjectType: SubjectContact})); err != nil {
			t.Fatalf("seed definition: %v", err)
		}
	}

	accountA, err := s.CreateAccount(ctx, a, &Account{Name: "Acme", OwnerID: "o"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contactA, err := s.CreateContact(ctx, a, &Contact{
		AccountID: accountA.ID, Email: "a@example.com", FirstName: "A", LastName: "One",
	})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	if _, err := CoerceCustomValues(ctx, s, a, "account",
		CustomValues{"primary_contact": contactA.ID}, nil); err != nil {
		t.Fatalf("a lookup to a record in the same tenant should resolve: %v", err)
	}

	// From another tenant the very same id must look like it does not exist.
	_, err = CoerceCustomValues(ctx, s, b, "account", CustomValues{"primary_contact": contactA.ID}, nil)
	if err == nil || !strings.Contains(err.Error(), "in this tenant") {
		t.Fatalf("a cross-tenant lookup must be refused as not found, got %v", err)
	}

	if _, err := CoerceCustomValues(ctx, s, a, "account",
		CustomValues{"primary_contact": "no-such-record"}, nil); err == nil {
		t.Fatal("a lookup to a missing record must be refused")
	}
}

// Per-field limits alone do not bound a record: enough fields that each pass
// individually still add up to a document nobody wants to read.
func TestCoerceCustomValuesBoundsTheWholeDocument(t *testing.T) {
	s, ctx, scope := valueFixture(t,
		def("body_a", FieldLongText, FieldConfig{}),
		def("body_b", FieldLongText, FieldConfig{}),
		def("body_c", FieldLongText, FieldConfig{}),
	)

	big := strings.Repeat("x", MaxCustomLongTextLength)
	// Each value is exactly at its own limit, so none is individually invalid.
	for _, key := range []string{"body_a", "body_b"} {
		if _, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{key: big}, nil); err != nil {
			t.Fatalf("a value at the per-field limit should pass on its own: %v", err)
		}
	}

	_, err := CoerceCustomValues(ctx, s, scope, "account",
		CustomValues{"body_a": big, "body_b": big, "body_c": big}, nil)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("expected the document ceiling to apply, got %v", err)
	}
}

func TestCoerceCustomValuesRequiresATenant(t *testing.T) {
	s, ctx, _ := valueFixture(t)
	if _, err := CoerceCustomValues(ctx, s, Scope{}, "account", CustomValues{}, nil); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant, got %v", err)
	}
}

// An empty document must be {} rather than null: the column is NOT NULL with
// an object-shape CHECK.
func TestEmptyDocumentMarshalsAsAnObject(t *testing.T) {
	for _, v := range []CustomValues{nil, {}} {
		b, err := v.MarshalDocument()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != "{}" {
			t.Fatalf("got %q, want {}", b)
		}
	}
}

// A document read back from jsonb must have the same shape as one just
// coerced, or a stored value and a fresh one compare unequal.
func TestDocumentRoundTripsThroughJSON(t *testing.T) {
	s, ctx, scope := valueFixture(t,
		def("region", FieldPicklist, FieldConfig{Options: []string{"EMEA"}}),
		def("tags", FieldMultiPicklist, FieldConfig{Options: []string{"a", "b"}}),
		def("amount", FieldCurrency, FieldConfig{Scale: 2}),
		def("count", FieldNumber, FieldConfig{}),
		def("active", FieldBoolean, FieldConfig{}),
	)

	original, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{
		"region": "EMEA", "tags": []any{"a", "b"}, "amount": "10.5", "count": 3.0, "active": true,
	}, nil)
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}

	encoded, err := original.MarshalDocument()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored CustomValues
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(restored) != len(original) {
		t.Fatalf("round trip changed the key set: %#v vs %#v", restored, original)
	}
	for key, want := range original {
		if !jsonEqual(t, restored[key], want) {
			t.Fatalf("key %q round-tripped as %#v (%T), want %#v (%T)", key, restored[key], restored[key], want, want)
		}
	}
	// Specifically the shape json.Unmarshal would otherwise get wrong.
	if _, ok := restored["tags"].([]string); !ok {
		t.Fatalf("multi-picklist restored as %T, want []string", restored["tags"])
	}
}

func TestCustomValuesCloneDoesNotShareMemory(t *testing.T) {
	original := CustomValues{"tags": []string{"a", "b"}, "region": "EMEA"}
	clone := original.Clone()
	clone["tags"].([]string)[0] = "mutated"
	clone["region"] = "mutated"

	if original["tags"].([]string)[0] != "a" || original["region"] != "EMEA" {
		t.Fatalf("clone shares memory with the original: %#v", original)
	}
	if CustomValues(nil).Clone() != nil {
		t.Fatal("cloning nil should stay nil")
	}
}

// jsonEqual compares two values the way the jsonb column would see them.
func jsonEqual(t *testing.T, got, want any) bool {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		return false
	}
	w, err := json.Marshal(want)
	if err != nil {
		return false
	}
	return string(g) == string(w)
}

// Update semantics are replacement, matching UpdateAccount and its siblings:
// a caller that round-trips what it read keeps its values, and one that omits
// a live field is asking for it to be cleared. A merge would make clearing
// impossible.
func TestUpdateReplacesLiveFieldsAndClearsOmittedOnes(t *testing.T) {
	s, ctx, scope := valueFixture(t,
		def("region", FieldText, FieldConfig{}),
		def("tier", FieldText, FieldConfig{}),
	)

	stored, err := CoerceCustomValues(ctx, s, scope, "account",
		CustomValues{"region": "EMEA", "tier": "gold"}, nil)
	if err != nil {
		t.Fatalf("initial write: %v", err)
	}

	next, err := CoerceCustomValues(ctx, s, scope, "account", CustomValues{"region": "AMER"}, stored)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if next["region"] != "AMER" {
		t.Fatalf("update did not apply: %#v", next)
	}
	if _, present := next["tier"]; present {
		t.Fatalf("an omitted live field should be cleared, got %#v", next["tier"])
	}

	// Round-tripping the whole document keeps everything.
	same, err := CoerceCustomValues(ctx, s, scope, "account", stored, stored)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if same["region"] != "EMEA" || same["tier"] != "gold" {
		t.Fatalf("round-tripping the stored document lost values: %#v", same)
	}
}
