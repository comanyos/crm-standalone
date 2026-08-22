package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func textField(key string) *CustomFieldDefinition {
	return &CustomFieldDefinition{
		ObjectType: "account", FieldKey: key, Label: "Label", DataType: FieldText,
	}
}

func TestCustomFieldDefinitionValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *CustomFieldDefinition
		want string // substring of the expected error; "" means it must validate
	}{
		{"minimal text field", textField("region"), ""},
		{"unknown object", &CustomFieldDefinition{
			ObjectType: "invoice", FieldKey: "k", Label: "L", DataType: FieldText,
		}, "unknown object type"},
		{"hyphen in key", textField("bad-key"), "field key"},
		{"leading digit in key", textField("9region"), "field key"},
		{"key too long", textField(strings.Repeat("a", 64)), "field key"},
		{"reserved key", textField("tenant_id"), "reserved"},
		{"blank label", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "   ", DataType: FieldText,
		}, "no label"},
		{"unknown data type", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: "geo",
		}, "unknown data type"},

		// Config must match the type exactly — in both directions.
		{"picklist without options", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldPicklist,
		}, "needs at least one option"},
		{"text with options", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldText,
			Config: FieldConfig{Options: []string{"a"}},
		}, "takes no options"},
		{"text with scale", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldText,
			Config: FieldConfig{Scale: 2},
		}, "takes no scale"},
		{"text with lookup target", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldText,
			Config: FieldConfig{LookupObjectType: "contact"},
		}, "takes no lookup target"},
		{"picklist with duplicate options", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldPicklist,
			Config: FieldConfig{Options: []string{"A", "A"}},
		}, "listed twice"},
		{"picklist with blank option", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldPicklist,
			Config: FieldConfig{Options: []string{"A", "  "}},
		}, "blank option"},
		{"valid picklist", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldPicklist,
			Config: FieldConfig{Options: []string{"A", "B"}},
		}, ""},
		{"currency scale out of range", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldCurrency,
			Config: FieldConfig{Scale: 99},
		}, "outside 0..12"},
		{"valid currency", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldCurrency,
			Config: FieldConfig{Scale: 2},
		}, ""},
		{"lookup to unknown object", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldLookup,
			Config: FieldConfig{LookupObjectType: "invoice"},
		}, "not a known object type"},
		{"valid lookup", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldLookup,
			Config: FieldConfig{LookupObjectType: "contact"},
		}, ""},

		// A formula is refused outright until there is a sandbox to run it in.
		// Storing one now would be storing an unvalidated program.
		{"formula not available yet", &CustomFieldDefinition{
			ObjectType: "account", FieldKey: "k", Label: "L", DataType: FieldFormula,
			Config: FieldConfig{Expression: "1 + 1"},
		}, "not available yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.want)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error should wrap ErrInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// A definition differing only in case or whitespace must not be able to claim
// a second document slot under what is effectively the same key.
func TestCustomFieldDefinitionNormalizesBeforeUniqueness(t *testing.T) {
	d := &CustomFieldDefinition{
		ObjectType: "  Account ", FieldKey: "  Region  ", Label: "  Region  ", DataType: " TEXT ",
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if d.ObjectType != "account" || d.FieldKey != "region" || d.DataType != "text" || d.Label != "Region" {
		t.Fatalf("not normalized: %+v", d)
	}
}

func TestCreateCustomFieldRejectsDuplicateKey(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	if _, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate key should conflict, got %v", err)
	}
}

// An archived field still owns its key: its values are still sitting in rows
// under it, so a new field reusing the key would inherit data it never wrote.
func TestArchivedCustomFieldStillOwnsItsKey(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	created, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.ArchiveCustomFieldDefinition(ctx, scope, created.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region")); !errors.Is(err, ErrConflict) {
		t.Fatalf("reusing an archived key should conflict, got %v", err)
	}
}

func TestArchiveCustomFieldIsIdempotentAndHidesFromLiveList(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	created, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := s.ArchiveCustomFieldDefinition(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	second, err := s.ArchiveCustomFieldDefinition(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("re-archive should be a no-op, got %v", err)
	}
	if !second.ArchivedAt.Equal(first.ArchivedAt) {
		t.Fatal("re-archiving moved the stamp; when it was retired must survive a repeated call")
	}

	live, err := s.ListCustomFieldDefinitions(ctx, scope, "account", false)
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("archived field still listed as live: %+v", live)
	}
	all, err := s.ListCustomFieldDefinitions(ctx, scope, "account", true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("archived field should still be listed with includeArchived, got %d", len(all))
	}
}

// The three identity fields each orphan stored data if they change.
func TestCustomFieldIdentityIsImmutable(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	created, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, tc := range []struct {
		name, want string
		mutate     func(*CustomFieldDefinition)
	}{
		{"object type", "object type cannot change", func(d *CustomFieldDefinition) { d.ObjectType = "contact" }},
		{"field key", "key cannot change", func(d *CustomFieldDefinition) { d.FieldKey = "territory" }},
		{"data type", "data type cannot change", func(d *CustomFieldDefinition) { d.DataType = FieldNumber }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incoming := cloneCustomFieldDefinition(created)
			tc.mutate(incoming)
			_, err := s.UpdateCustomFieldDefinition(ctx, scope, incoming)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestUpdateCustomFieldChangesTheMutableHalf(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	created, err := s.CreateCustomFieldDefinition(ctx, scope, &CustomFieldDefinition{
		ObjectType: "account", FieldKey: "region", Label: "Region", DataType: FieldPicklist,
		Config: FieldConfig{Options: []string{"EMEA"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	incoming := cloneCustomFieldDefinition(created)
	incoming.Label = "Sales Region"
	incoming.Required = true
	incoming.Config.Options = []string{"EMEA", "AMER"}

	updated, err := s.UpdateCustomFieldDefinition(ctx, scope, incoming)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Label != "Sales Region" || !updated.Required || len(updated.Config.Options) != 2 {
		t.Fatalf("update did not apply: %+v", updated)
	}
	// An update that would make the definition invalid must be refused, not
	// half-applied.
	broken := cloneCustomFieldDefinition(created)
	broken.Config.Options = nil
	if _, err := s.UpdateCustomFieldDefinition(ctx, scope, broken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("emptying a picklist's options should be invalid, got %v", err)
	}
}

// The property this whole design is built to preserve.
func TestCustomFieldDefinitionsAreTenantIsolated(t *testing.T) {
	s, ctx := NewMemStore(), context.Background()
	a, b := Scope{TenantID: "tenant-a"}, Scope{TenantID: "tenant-b"}

	mine, err := s.CreateCustomFieldDefinition(ctx, a, textField("region"))
	if err != nil {
		t.Fatalf("create in tenant-a: %v", err)
	}

	if _, err := s.GetCustomFieldDefinition(ctx, b, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant-b read tenant-a's definition by id, got %v", err)
	}
	listed, err := s.ListCustomFieldDefinitions(ctx, b, "account", true)
	if err != nil {
		t.Fatalf("list in tenant-b: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("tenant-b listed tenant-a's definitions: %+v", listed)
	}
	if _, err := s.ArchiveCustomFieldDefinition(ctx, b, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant-b archived tenant-a's definition, got %v", err)
	}

	// The same key in another tenant is a different field, not a collision.
	if _, err := s.CreateCustomFieldDefinition(ctx, b, textField("region")); err != nil {
		t.Fatalf("tenant-b should be able to use the same key: %v", err)
	}
}

func TestCustomFieldStoreRequiresATenant(t *testing.T) {
	s, ctx, none := NewMemStore(), context.Background(), Scope{}

	if _, err := s.CreateCustomFieldDefinition(ctx, none, textField("region")); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("create without a tenant: %v", err)
	}
	if _, err := s.ListCustomFieldDefinitions(ctx, none, "account", false); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("list without a tenant: %v", err)
	}
	if _, err := s.ArchiveCustomFieldDefinition(ctx, none, "any"); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("archive without a tenant: %v", err)
	}
}

// A caller must not be able to create a field that is already retired — a
// definition nothing can ever write to.
func TestCreateIgnoresCallerSuppliedArchiveStamp(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	in := textField("region")
	in.ArchivedAt = in.CreatedAt.AddDate(0, 0, -1)
	created, err := s.CreateCustomFieldDefinition(ctx, scope, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Archived() {
		t.Fatal("create honoured a caller-supplied archive stamp")
	}
}

// A returned definition must not share memory with the stored one.
func TestCustomFieldDefinitionsAreClonedOut(t *testing.T) {
	s, ctx, scope := NewMemStore(), context.Background(), Scope{TenantID: "t1"}

	created, err := s.CreateCustomFieldDefinition(ctx, scope, &CustomFieldDefinition{
		ObjectType: "account", FieldKey: "region", Label: "Region", DataType: FieldPicklist,
		Config: FieldConfig{Options: []string{"EMEA"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created.Label = "mutated"
	created.Config.Options[0] = "mutated"

	fetched, err := s.GetCustomFieldDefinition(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Label == "mutated" || fetched.Config.Options[0] == "mutated" {
		t.Fatalf("store shares memory with its caller: %+v", fetched)
	}
}

func TestMarshalConfigNeverEmitsNull(t *testing.T) {
	d := textField("region")
	b, err := d.MarshalConfig()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "{}" {
		t.Fatalf("config rendered as %q; the column is NOT NULL with an object CHECK", b)
	}
}

// --- PgStore ---

// The MemStore tests above prove the rules; this proves PgStore implements the
// same ones through real SQL — the uniqueness constraint, the partial index's
// live/archived split, the jsonb config round trip, and RLS.
//
// Skips without a live database, like the rest of the PostgreSQL tier.
func TestPgStoreCustomFieldDefinitions(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	scope := Scope{TenantID: "cf-" + newID()}

	created, err := s.CreateCustomFieldDefinition(ctx, scope, &CustomFieldDefinition{
		ObjectType: "account", FieldKey: "region", Label: "Region", DataType: FieldPicklist,
		Config: FieldConfig{Options: []string{"EMEA", "AMER"}}, Required: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The jsonb config must survive the round trip intact.
	got, err := s.GetCustomFieldDefinition(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Config.Options) != 2 || got.Config.Options[0] != "EMEA" || !got.Required {
		t.Fatalf("config did not round trip: %+v", got)
	}

	// The database's unique constraint is what actually enforces this.
	if _, err := s.CreateCustomFieldDefinition(ctx, scope, textField("region")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate key should conflict, got %v", err)
	}

	updated, err := s.UpdateCustomFieldDefinition(ctx, scope, &CustomFieldDefinition{
		ID: created.ID, Label: "Sales Region", DataType: FieldPicklist,
		Config: FieldConfig{Options: []string{"EMEA", "AMER", "APAC"}},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Label != "Sales Region" || len(updated.Config.Options) != 3 || updated.Required {
		t.Fatalf("update did not apply: %+v", updated)
	}

	live, err := s.ListCustomFieldDefinitions(ctx, scope, "account", false)
	if err != nil || len(live) != 1 {
		t.Fatalf("list live: %v (%d rows)", err, len(live))
	}

	archived, err := s.ArchiveCustomFieldDefinition(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !archived.Archived() {
		t.Fatal("archive did not stamp archived_at")
	}
	again, err := s.ArchiveCustomFieldDefinition(ctx, scope, created.ID)
	if err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	if !again.ArchivedAt.Equal(archived.ArchivedAt) {
		t.Fatal("re-archiving moved the stamp; COALESCE should keep the original")
	}

	live, err = s.ListCustomFieldDefinitions(ctx, scope, "account", false)
	if err != nil || len(live) != 0 {
		t.Fatalf("archived field still live: %v (%d rows)", err, len(live))
	}
	all, err := s.ListCustomFieldDefinitions(ctx, scope, "account", true)
	if err != nil || len(all) != 1 {
		t.Fatalf("list with archived: %v (%d rows)", err, len(all))
	}

	if _, err := s.GetCustomFieldDefinition(ctx, scope, "no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing definition should be ErrNotFound, got %v", err)
	}
}

// RLS, not a Go WHERE clause, is what must keep these apart.
func TestPgStoreCustomFieldDefinitionsAreTenantIsolated(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	a := Scope{TenantID: "cf-a-" + newID()}
	b := Scope{TenantID: "cf-b-" + newID()}

	mine, err := s.CreateCustomFieldDefinition(ctx, a, textField("region"))
	if err != nil {
		t.Fatalf("create in tenant a: %v", err)
	}

	if _, err := s.GetCustomFieldDefinition(ctx, b, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b read tenant a's definition, got %v", err)
	}
	listed, err := s.ListCustomFieldDefinitions(ctx, b, "account", true)
	if err != nil {
		t.Fatalf("list in tenant b: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("tenant b listed tenant a's definitions: %+v", listed)
	}
	if _, err := s.ArchiveCustomFieldDefinition(ctx, b, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant b archived tenant a's definition, got %v", err)
	}
	// The same key in another tenant is a different field, not a collision.
	if _, err := s.CreateCustomFieldDefinition(ctx, b, textField("region")); err != nil {
		t.Fatalf("tenant b should be able to use the same key: %v", err)
	}
}
