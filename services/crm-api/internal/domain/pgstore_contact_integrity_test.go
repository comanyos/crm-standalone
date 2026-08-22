package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestPgStoreContactIntegrity exercises B4's rules against real Postgres:
// account_id referential integrity (dangling, cross-tenant, re-checked on
// update), exact-email dedup flagging (set, cleared, tenant-scoped, never
// self-flagging), and role validation — the same behaviors
// contact_integrity_test.go proves against MemStore, run here through
// PgStore/scopedDB so the SQL paths are not just compiled, but verified.
func TestPgStoreContactIntegrity(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-crm-contact-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-crm-contact-b-%d", stamp), OrgID: "org-b"}

	// account_id required, and dangling / cross-tenant is rejected.
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "no-account@acme.com"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without account_id: got %v, want ErrInvalid", err)
	}
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: "does-not-exist"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with dangling account_id: got %v, want ErrInvalid", err)
	}

	acctA, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create tenant A account: %v", err)
	}
	acctB, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Tenant Co", OwnerID: "owner-b"})
	if err != nil {
		t.Fatalf("create tenant B account: %v", err)
	}
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acctB.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with cross-tenant account_id: got %v, want ErrInvalid", err)
	}

	// account_id re-checked on Update too.
	contact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acctA.ID})
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	contact.AccountID = "does-not-exist"
	if _, err := s.UpdateContact(ctx, tenantA, contact); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update with dangling account_id: got %v, want ErrInvalid", err)
	}
	contact.AccountID = acctA.ID // restore for the rest of the test

	// Role: known values accepted, unknown rejected on both create and update.
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "champion@acme.com", AccountID: acctA.ID, Role: "champion"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with unknown role: got %v, want ErrInvalid", err)
	}
	withRole, err := s.CreateContact(ctx, tenantA, &Contact{Email: "billing@acme.com", AccountID: acctA.ID, Role: ContactRoleBilling})
	if err != nil {
		t.Fatalf("create with known role: %v", err)
	}
	if withRole.Role != ContactRoleBilling {
		t.Fatalf("Role = %q, want %q", withRole.Role, ContactRoleBilling)
	}
	withRole.Role = "champion"
	if _, err := s.UpdateContact(ctx, tenantA, withRole); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update with unknown role: got %v, want ErrInvalid", err)
	}

	// Dedup flagging on email, tenant-scoped, cleared on change, never
	// self-flagging.
	dup, err := s.CreateContact(ctx, tenantA, &Contact{Email: "Alice@Acme.com", AccountID: acctA.ID})
	if err != nil {
		t.Fatalf("create duplicate-email contact: %v", err)
	}
	if dup.DuplicateOfContactID != contact.ID {
		t.Fatalf("DuplicateOfContactID = %q, want %q", dup.DuplicateOfContactID, contact.ID)
	}
	if _, err := s.CreateContact(ctx, tenantB, &Contact{Email: "alice@acme.com", AccountID: acctB.ID}); err != nil {
		t.Fatalf("create tenant B contact with same email: %v", err)
	}

	// Re-fetch to prove the flag round-trips through Postgres.
	gotDup, err := s.GetContact(ctx, tenantA, dup.ID)
	if err != nil {
		t.Fatalf("get duplicate contact: %v", err)
	}
	if gotDup.DuplicateOfContactID != contact.ID {
		t.Fatalf("persisted DuplicateOfContactID = %q, want %q", gotDup.DuplicateOfContactID, contact.ID)
	}

	// Changing the email away clears the flag. (A no-op update leaving two
	// contacts sharing a case-insensitive email is deliberately not
	// asserted here: which of the two the exact-key lookup returns as "the
	// match" is not specified — the same ambiguity account_hierarchy_test.go
	// avoids for Account's own dedup flag. A contact never flagging *itself*
	// is covered deterministically in contact_integrity_test.go's
	// TestContactNeverFlagsItself, where only one contact shares the key.)
	dup.Email = "unique-pg@acme.com"
	cleared, err := s.UpdateContact(ctx, tenantA, dup)
	if err != nil {
		t.Fatalf("update clearing email: %v", err)
	}
	if cleared.DuplicateOfContactID != "" {
		t.Fatalf("DuplicateOfContactID after clearing email = %q, want empty", cleared.DuplicateOfContactID)
	}

	// Tenant B cannot read tenant A's contact at all.
	if _, err := s.GetContact(ctx, tenantB, contact.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B GetContact err = %v; want ErrNotFound", err)
	}
}
