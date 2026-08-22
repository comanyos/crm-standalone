package domain

import (
	"errors"
	"fmt"
	"testing"
)

// --- Referential integrity: account_id ------------------------------------

// TestContactAccountMustExist covers the referential-integrity half of B4's
// account_id invariant: an account_id naming no Account in the tenant is
// ErrInvalid — the same split requireAccount already gives Opportunity.
func TestContactAccountMustExist(t *testing.T) {
	s := NewMemStore()
	_, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: "does-not-exist"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with dangling account_id: got %v, want ErrInvalid", err)
	}
}

// TestContactAccountMustBeInTenant covers the cross-tenant half of the same
// rule: naming another tenant's real Account id as account_id is
// indistinguishable, from this tenant's point of view, from naming an id
// that does not exist at all.
func TestContactAccountMustBeInTenant(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Tenant Co", OwnerID: "owner-b"})
	if err != nil {
		t.Fatalf("seed tenant B account: %v", err)
	}
	_, err = s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with cross-tenant account_id: got %v, want ErrInvalid", err)
	}
}

// TestContactAccountCheckedOnUpdateToo covers that account_id is re-checked
// on every Update, not only at Create — a dangling reference introduced
// after this Contact was created (the account it named was deleted) must be
// caught here too, the same convention UpdateOpportunity uses.
func TestContactAccountCheckedOnUpdateToo(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	contact.AccountID = "does-not-exist"
	_, err = s.UpdateContact(ctx, tenantA, contact)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("update with dangling account_id: got %v, want ErrInvalid", err)
	}

	otherAcct, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Tenant Co", OwnerID: "owner-b"})
	if err != nil {
		t.Fatalf("seed tenant B account: %v", err)
	}
	contact.AccountID = otherAcct.ID
	_, err = s.UpdateContact(ctx, tenantA, contact)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("update with cross-tenant account_id: got %v, want ErrInvalid", err)
	}
}

func TestContactCreateWithValidAccountIsFine(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID}); err != nil {
		t.Fatalf("create with valid account_id: %v", err)
	}
}

// --- Exact-key dedup on email ----------------------------------------------

// TestContactDuplicateFlaggingOnCreate covers B4's dedup rule: a matching
// email is flagged (never blocked — the record is still created), and a
// non-matching email is not flagged.
func TestContactDuplicateFlaggingOnCreate(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	first, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if first.DuplicateOfContactID != "" {
		t.Fatalf("first contact with a unique email was flagged: %q", first.DuplicateOfContactID)
	}

	t.Run("matching email is flagged but still created", func(t *testing.T) {
		dup, err := s.CreateContact(ctx, tenantA, &Contact{Email: "Alice@Acme.com", AccountID: acct.ID})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if dup.DuplicateOfContactID != first.ID {
			t.Fatalf("DuplicateOfContactID = %q, want %q", dup.DuplicateOfContactID, first.ID)
		}
		// Still readable as a normal contact — flagging never blocks creation.
		got, err := s.GetContact(ctx, tenantA, dup.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ID != dup.ID {
			t.Fatal("contact was not stored")
		}
	})

	t.Run("non-matching email is not flagged", func(t *testing.T) {
		other, err := s.CreateContact(ctx, tenantA, &Contact{Email: "bob@acme.com", AccountID: acct.ID})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if other.DuplicateOfContactID != "" {
			t.Fatalf("DuplicateOfContactID = %q, want empty", other.DuplicateOfContactID)
		}
	})
}

// TestContactDuplicateFlagRecomputedOnUpdate covers that the flag is not
// last-write-wins stale: it is recomputed on every Update (setting and
// clearing), the same convention Lead.DuplicateOfContactID (B1) and
// Account.DuplicateOfAccountID (B3) use.
func TestContactDuplicateFlagRecomputedOnUpdate(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	first, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	second, err := s.CreateContact(ctx, tenantA, &Contact{Email: "bob@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if second.DuplicateOfContactID != "" {
		t.Fatalf("DuplicateOfContactID = %q, want empty at create", second.DuplicateOfContactID)
	}

	// Setting: an update whose email now matches another Contact flags it.
	second.Email = "alice@acme.com"
	updated, err := s.UpdateContact(ctx, tenantA, second)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.DuplicateOfContactID != first.ID {
		t.Fatalf("DuplicateOfContactID after update = %q, want %q", updated.DuplicateOfContactID, first.ID)
	}

	// Clearing: changing the email away clears the flag.
	updated.Email = "unique-again@acme.com"
	cleared, err := s.UpdateContact(ctx, tenantA, updated)
	if err != nil {
		t.Fatalf("update clearing email: %v", err)
	}
	if cleared.DuplicateOfContactID != "" {
		t.Fatalf("DuplicateOfContactID after clearing email = %q, want empty", cleared.DuplicateOfContactID)
	}
}

// TestContactNeverFlagsItself covers that updating a Contact without
// actually changing its email (or changing other fields only) never flags
// it against itself.
func TestContactNeverFlagsItself(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	contact.Title = "VP Eng"
	updated, err := s.UpdateContact(ctx, tenantA, contact)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.DuplicateOfContactID != "" {
		t.Fatalf("contact flagged against itself: %q", updated.DuplicateOfContactID)
	}
}

// TestContactDuplicateFlagIsTenantScoped covers the mandatory tenancy rule:
// a cross-tenant Contact sharing the same email must never flag another
// tenant's Contact (.claude/rules/tenancy-and-scopes.md — dedup is not a
// global lookup).
func TestContactDuplicateFlagIsTenantScoped(t *testing.T) {
	s := NewMemStore()
	acctB, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Tenant Co", OwnerID: "owner-b"})
	if err != nil {
		t.Fatalf("seed tenant B account: %v", err)
	}
	if _, err := s.CreateContact(ctx, tenantB, &Contact{Email: "shared@example.com", AccountID: acctB.ID}); err != nil {
		t.Fatalf("seed tenant B contact: %v", err)
	}

	acctA, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed tenant A account: %v", err)
	}
	contactA, err := s.CreateContact(ctx, tenantA, &Contact{Email: "shared@example.com", AccountID: acctA.ID})
	if err != nil {
		t.Fatalf("create tenant A contact: %v", err)
	}
	if contactA.DuplicateOfContactID != "" {
		t.Fatalf("tenant A contact flagged against tenant B's contact: %q", contactA.DuplicateOfContactID)
	}
}

// --- Role -------------------------------------------------------------

// TestContactRoleAcceptsKnownValues covers every value in contactRoles, plus
// empty (role not yet known).
func TestContactRoleAcceptsKnownValues(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	roles := []string{"", ContactRoleBilling, ContactRoleTechnical, ContactRoleDecisionMaker, ContactRoleOther}
	for i, role := range roles {
		t.Run(role, func(t *testing.T) {
			c, err := s.CreateContact(ctx, tenantA, &Contact{
				Email:     emailForRoleCase(i),
				AccountID: acct.ID,
				Role:      role,
			})
			if err != nil {
				t.Fatalf("create with role %q: %v", role, err)
			}
			if c.Role != role {
				t.Fatalf("Role = %q, want %q", c.Role, role)
			}
		})
	}
}

func emailForRoleCase(i int) string {
	return fmt.Sprintf("role-case-%d@acme.com", i)
}

// TestContactRoleRejectsUnknownValue covers that an out-of-set role is
// ErrInvalid on both Create and Update — a plain validated field, not a
// workflow.
func TestContactRoleRejectsUnknownValue(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	t.Run("create", func(t *testing.T) {
		_, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID, Role: "champion"})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("create with unknown role: got %v, want ErrInvalid", err)
		}
	})

	t.Run("update", func(t *testing.T) {
		contact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "bob@acme.com", AccountID: acct.ID})
		if err != nil {
			t.Fatalf("seed contact: %v", err)
		}
		contact.Role = "champion"
		_, err = s.UpdateContact(ctx, tenantA, contact)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("update with unknown role: got %v, want ErrInvalid", err)
		}
	})
}
