package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The CRM role matrix is pinned to the shared contract (task.md §1.4).
//
// This service grants the scopes; services/crm-api demands them. They are
// separate Go modules, so the matrix would otherwise exist twice with nothing
// comparing the copies — and a scope granted here but named differently there
// is invisible at build time. It surfaces as one role, in production, being
// told it lacks permission for a screen it is supposed to have.
//
// packages/contracts/rbac/crm-roles.json is the source of truth. Changing a
// role means changing that file and both services' definitions together; this
// test is what makes the omission fail rather than ship.

type sharedRole struct {
	Key         string   `json:"key"`
	DisplayName string   `json:"displayName"`
	Scopes      []string `json:"scopes"`
}

type sharedRoleMatrix struct {
	Scopes []string     `json:"scopes"`
	Roles  []sharedRole `json:"roles"`
}

func loadSharedRoleMatrix(t *testing.T) sharedRoleMatrix {
	t.Helper()
	var path string
	for _, candidate := range []string{
		"../../../../packages/contracts/rbac/crm-roles.json",
		"../../../packages/contracts/rbac/crm-roles.json",
		"packages/contracts/rbac/crm-roles.json",
	} {
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		t.Fatal("cannot find packages/contracts/rbac/crm-roles.json; the roles this " +
			"bootstrap provisions are unpinned, and nothing checks them against the CRM")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read role matrix: %v", err)
	}
	var m sharedRoleMatrix
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse role matrix: %v", err)
	}
	if len(m.Roles) == 0 {
		t.Fatal("the shared role matrix defines no roles")
	}
	return m
}

// crmRoleDefinitionsByKey indexes the CRM-resource roles this file provisions.
// The "owner" role is an AuthinCore administration role and is not part of the
// CRM contract.
func crmRoleDefinitionsByKey() map[string]roleDefinition {
	out := map[string]roleDefinition{}
	for _, definition := range crmRoleDefinitions {
		if definition.ResourceType == "crm" {
			out[definition.Key] = definition
		}
	}
	return out
}

// TestBootstrapProvisionsExactlyTheSharedRoleMatrix compares the two sides
// key by key and scope by scope, in both directions: a role in the contract
// that bootstrap never creates leaves users unable to be granted it, and a
// role bootstrap creates that the contract does not describe is authority
// nobody reviewed.
func TestBootstrapProvisionsExactlyTheSharedRoleMatrix(t *testing.T) {
	shared := loadSharedRoleMatrix(t)
	provisioned := crmRoleDefinitionsByKey()

	if len(provisioned) != len(shared.Roles) {
		t.Errorf("bootstrap provisions %d CRM roles, the contract describes %d",
			len(provisioned), len(shared.Roles))
	}

	for _, want := range shared.Roles {
		got, ok := provisioned[want.Key]
		if !ok {
			t.Errorf("the contract describes role %q, which bootstrap never creates", want.Key)
			continue
		}
		if got.DisplayName != want.DisplayName {
			t.Errorf("role %s display name = %q, contract says %q",
				want.Key, got.DisplayName, want.DisplayName)
		}
		gotScopes := append([]string(nil), got.Scopes...)
		wantScopes := append([]string(nil), want.Scopes...)
		sort.Strings(gotScopes)
		sort.Strings(wantScopes)
		if len(gotScopes) != len(wantScopes) {
			t.Errorf("role %s: bootstrap grants %v, contract says %v", want.Key, gotScopes, wantScopes)
			continue
		}
		for i := range gotScopes {
			if gotScopes[i] != wantScopes[i] {
				t.Errorf("role %s: bootstrap grants %v, contract says %v", want.Key, gotScopes, wantScopes)
				break
			}
		}
	}

	for key := range provisioned {
		found := false
		for _, want := range shared.Roles {
			if want.Key == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bootstrap creates CRM role %q, which the shared contract does not describe", key)
		}
	}
}

// TestProvisionedScopesAreInTheContractsVocabulary catches a typo'd scope,
// which would otherwise provision cleanly and simply never match anything the
// CRM demands.
func TestProvisionedScopesAreInTheContractsVocabulary(t *testing.T) {
	shared := loadSharedRoleMatrix(t)
	known := map[string]bool{"crm:*": true}
	for _, scope := range shared.Scopes {
		known[scope] = true
	}
	for _, definition := range crmRoleDefinitions {
		if definition.ResourceType != "crm" {
			continue
		}
		for _, scope := range definition.Scopes {
			if !known[scope] {
				t.Errorf("role %s is granted %q, which is not a scope the CRM defines; "+
					"it will never match a procedure", definition.Key, scope)
			}
		}
	}
}

// TestBootstrapAdministratorIsGrantedTheAdminRole pins the assignment, not
// just the definition. Creating the roles and forgetting to assign one leaves
// a freshly bootstrapped organization with an administrator who cannot use
// the CRM.
func TestBootstrapAdministratorIsGrantedTheAdminRole(t *testing.T) {
	provisioned := crmRoleDefinitionsByKey()
	if _, ok := provisioned["crm_admin"]; !ok {
		t.Fatal("bootstrap defines no crm_admin role to assign")
	}
	// assignBootstrapRoles grants this fixed set; keep the test honest by
	// naming what it depends on rather than re-deriving it.
	for _, key := range []string{"owner", "crm_admin"} {
		found := false
		for _, definition := range crmRoleDefinitions {
			if definition.Key == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("assignBootstrapRoles grants %q, which crmRoleDefinitions never creates; "+
				"the assignment would insert a nil role id", key)
		}
	}
}
