package domain

import (
	"context"
	"errors"
	"testing"
)

func TestCustomObjectDefinitionsAndRecordsAreTenantScoped(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	a := Scope{TenantID: "tenant-a"}
	b := Scope{TenantID: "tenant-b"}
	object, err := s.CreateCustomObjectDefinition(ctx, a, &CustomObjectDefinition{APIName: "asset", Label: "Asset", PluralLabel: "Assets"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCustomFieldDefinition(ctx, a, &CustomFieldDefinition{ObjectType: object.APIName, FieldKey: "serial", Label: "Serial", DataType: FieldText}); err != nil {
		t.Fatalf("custom object field: %v", err)
	}
	record, err := s.CreateCustomRecord(ctx, a, &CustomRecord{ObjectType: "asset", Fields: CustomValues{"serial": "A-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCustomRecord(ctx, b, "asset", record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant read=%v, want not found", err)
	}
	if _, err := s.GetCustomObjectDefinition(ctx, b, "asset"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant definition read=%v, want not found", err)
	}
	if _, err := s.CreateCustomRecord(ctx, b, &CustomRecord{ObjectType: "asset", Fields: CustomValues{"serial": "B-1"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant write=%v, want not found", err)
	}
	if got, _, err := s.ListCustomRecords(ctx, b, "asset", Page{}); err != nil || len(got) != 0 {
		t.Fatalf("other tenant list=%v,%v", got, err)
	}
}

func TestCustomObjectRecordRelationsStayInTenant(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	a := Scope{TenantID: "tenant-a"}
	b := Scope{TenantID: "tenant-b"}
	if _, err := s.CreateCustomObjectDefinition(ctx, a, &CustomObjectDefinition{APIName: "asset", Label: "Asset", PluralLabel: "Assets"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCustomFieldDefinition(ctx, a, &CustomFieldDefinition{ObjectType: "asset", FieldKey: "serial", Label: "Serial", DataType: FieldText}); err != nil {
		t.Fatal(err)
	}
	account, err := s.CreateAccount(ctx, a, &Account{Name: "A", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.CreateAccount(ctx, b, &Account{Name: "B", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCustomRecord(ctx, a, &CustomRecord{ObjectType: "asset", Fields: CustomValues{"serial": "A"}, Relations: map[string]string{"account": account.ID}}); err != nil {
		t.Fatalf("same-tenant relation: %v", err)
	}
	if _, err := s.CreateCustomRecord(ctx, a, &CustomRecord{ObjectType: "asset", Fields: CustomValues{"serial": "B"}, Relations: map[string]string{"account": foreign.ID}}); err == nil {
		t.Fatal("cross-tenant relation was accepted")
	}
}
