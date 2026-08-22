package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/auth"
)

func fieldSecurityContext(roles, scopes []string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Subject: "field-security-test", TenantID: "tenant-a", OrgID: "org-a", Roles: roles, Scopes: scopes,
	})
}

// TestCustomFieldSecurityIsEnforcedBeforeSerialization proves the requirement
// that a lower-privileged caller receives no restricted field at all, and
// cannot write one by calling the API directly instead of using the UI.
func TestCustomFieldSecurityIsEnforcedBeforeSerialization(t *testing.T) {
	s := newTestService()
	admin := fieldSecurityContext([]string{"crm_admin"}, []string{"crm:*"})
	member := fieldSecurityContext([]string{"crm_member"}, []string{"crm:account:read", "crm:account:write"})

	_, err := s.CreateCustomFieldDefinition(admin, connect.NewRequest(&crmv1.CreateCustomFieldDefinitionRequest{
		Definition: &crmv1.CustomFieldDefinition{
			ObjectType: "account", FieldKey: "internal_score", Label: "Internal score", DataType: "number",
			Config: &crmv1.CustomFieldConfig{VisibleToRoles: []string{"crm_admin"}, EditableToRoles: []string{"crm_admin"}},
		},
	}))
	if err != nil {
		t.Fatalf("create restricted field: %v", err)
	}
	created, err := s.CreateAccount(admin, connect.NewRequest(&crmv1.CreateAccountRequest{Account: &crmv1.Account{
		Name: "Acme", OwnerId: "owner", CustomFields: map[string]*crmv1.CustomValue{
			"internal_score": {Value: &crmv1.CustomValue_Number{Number: 91}},
		},
	}}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if _, ok := created.Msg.GetAccount().GetCustomFields()["internal_score"]; !ok {
		t.Fatal("administrator response omitted the field it can read")
	}

	read, err := s.GetAccount(member, connect.NewRequest(&crmv1.GetAccountRequest{Id: created.Msg.GetAccount().GetId()}))
	if err != nil {
		t.Fatalf("member read: %v", err)
	}
	if _, leaked := read.Msg.GetAccount().GetCustomFields()["internal_score"]; leaked {
		t.Fatal("restricted custom field was present in lower-privileged API response")
	}

	_, err = s.UpdateAccount(member, connect.NewRequest(&crmv1.UpdateAccountRequest{Account: &crmv1.Account{
		Id: created.Msg.GetAccount().GetId(), Name: "Acme", OwnerId: "owner", CustomFields: map[string]*crmv1.CustomValue{
			"internal_score": {Value: &crmv1.CustomValue_Number{Number: 1}},
		},
	}}))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Fatalf("member restricted-field write code = %v, want PermissionDenied (err=%v)", got, err)
	}

	adminRead, err := s.GetAccount(admin, connect.NewRequest(&crmv1.GetAccountRequest{Id: created.Msg.GetAccount().GetId()}))
	if err != nil {
		t.Fatalf("administrator re-read: %v", err)
	}
	if got := adminRead.Msg.GetAccount().GetCustomFields()["internal_score"].GetNumber(); got != 91 {
		t.Fatalf("unauthorized write changed stored value to %v, want 91", got)
	}
}

func TestFormulaFieldsAreComputedOnReadAndNeverAcceptedFromWriters(t *testing.T) {
	s := newTestService()
	admin := fieldSecurityContext([]string{"crm_admin"}, []string{"crm:*"})
	for _, definition := range []*crmv1.CustomFieldDefinition{
		{ObjectType: "account", FieldKey: "subtotal", Label: "Subtotal", DataType: "number"},
		{ObjectType: "account", FieldKey: "discounted", Label: "Discounted", DataType: "formula", Config: &crmv1.CustomFieldConfig{Expression: "subtotal * 0.9"}},
	} {
		if _, err := s.CreateCustomFieldDefinition(admin, connect.NewRequest(&crmv1.CreateCustomFieldDefinitionRequest{Definition: definition})); err != nil {
			t.Fatalf("create %s: %v", definition.FieldKey, err)
		}
	}
	created, err := s.CreateAccount(admin, connect.NewRequest(&crmv1.CreateAccountRequest{Account: &crmv1.Account{Name: "Formula", OwnerId: "owner", CustomFields: map[string]*crmv1.CustomValue{
		"subtotal": {Value: &crmv1.CustomValue_Number{Number: 20}},
	}}}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if got := created.Msg.GetAccount().GetCustomFields()["discounted"].GetNumber(); got != 18 {
		t.Fatalf("computed value = %v, want 18", got)
	}
	_, err = s.UpdateAccount(admin, connect.NewRequest(&crmv1.UpdateAccountRequest{Account: &crmv1.Account{Id: created.Msg.GetAccount().GetId(), Name: "Formula", OwnerId: "owner", CustomFields: map[string]*crmv1.CustomValue{
		"subtotal": {Value: &crmv1.CustomValue_Number{Number: 20}}, "discounted": {Value: &crmv1.CustomValue_Number{Number: 1}},
	}}}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("formula write code = %v, want InvalidArgument (err=%v)", got, err)
	}
}
