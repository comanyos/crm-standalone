package domain

import (
	"errors"
	"testing"
)

// --- Hierarchy: acyclicity ---------------------------------------------

// TestAccountParentMustExist covers the referential-integrity half of B3's
// parent invariant: a parent_id naming no Account in the tenant is
// ErrInvalid, not ErrInvalidTransition — the request names something that
// does not exist, the same split hcm's OrgUnit invariant uses.
func TestAccountParentMustExist(t *testing.T) {
	s := NewMemStore()
	_, err := s.CreateAccount(ctx, tenantA, &Account{Name: "eng", OwnerID: "owner-a", ParentID: "does-not-exist"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with dangling parent: got %v, want ErrInvalid", err)
	}
}

// TestAccountParentMustBeInTenant covers the cross-tenant half of the same
// rule: naming another tenant's real Account id as parent_id is
// indistinguishable, from this tenant's point of view, from naming an id
// that does not exist at all.
func TestAccountParentMustBeInTenant(t *testing.T) {
	s := NewMemStore()
	parent, err := s.CreateAccount(ctx, tenantA, &Account{Name: "root", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = s.CreateAccount(ctx, tenantB, &Account{Name: "eng", OwnerID: "owner-b", ParentID: parent.ID})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with cross-tenant parent: got %v, want ErrInvalid", err)
	}
}

func TestAccountCreateWithoutParentIsFine(t *testing.T) {
	s := NewMemStore()
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "root", OwnerID: "owner-a"}); err != nil {
		t.Fatalf("create root account: %v", err)
	}
}

// TestAccountCycleRejection is the table-driven cycle test the task
// specifies: self-parent, a direct cycle (A -> B -> A), a deeper cycle
// (A -> B -> C -> A), and legal reparenting that must still succeed.
func TestAccountCycleRejection(t *testing.T) {
	t.Run("self-parent", func(t *testing.T) {
		s := NewMemStore()
		root, err := s.CreateAccount(ctx, tenantA, &Account{Name: "root", OwnerID: "owner-a"})
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		root.ParentID = root.ID
		_, err = s.UpdateAccount(ctx, tenantA, root)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("self-parent: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("direct cycle A -> B -> A", func(t *testing.T) {
		s := NewMemStore()
		a, err := s.CreateAccount(ctx, tenantA, &Account{Name: "A", OwnerID: "owner-a"})
		if err != nil {
			t.Fatalf("create A: %v", err)
		}
		b, err := s.CreateAccount(ctx, tenantA, &Account{Name: "B", OwnerID: "owner-a", ParentID: a.ID})
		if err != nil {
			t.Fatalf("create B: %v", err)
		}
		// A -> B would close the loop: A's parent becomes its own child.
		a.ParentID = b.ID
		_, err = s.UpdateAccount(ctx, tenantA, a)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("direct cycle: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("deeper cycle A -> B -> C -> A", func(t *testing.T) {
		s := NewMemStore()
		a, err := s.CreateAccount(ctx, tenantA, &Account{Name: "A", OwnerID: "owner-a"})
		if err != nil {
			t.Fatalf("create A: %v", err)
		}
		b, err := s.CreateAccount(ctx, tenantA, &Account{Name: "B", OwnerID: "owner-a", ParentID: a.ID})
		if err != nil {
			t.Fatalf("create B: %v", err)
		}
		c, err := s.CreateAccount(ctx, tenantA, &Account{Name: "C", OwnerID: "owner-a", ParentID: b.ID})
		if err != nil {
			t.Fatalf("create C: %v", err)
		}
		a.ParentID = c.ID
		_, err = s.UpdateAccount(ctx, tenantA, a)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("deeper cycle: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("legal reparenting", func(t *testing.T) {
		s := NewMemStore()
		root, err := s.CreateAccount(ctx, tenantA, &Account{Name: "root", OwnerID: "owner-a"})
		if err != nil {
			t.Fatalf("create root: %v", err)
		}
		child, err := s.CreateAccount(ctx, tenantA, &Account{Name: "child", OwnerID: "owner-a", ParentID: root.ID})
		if err != nil {
			t.Fatalf("create child: %v", err)
		}
		grandchild, err := s.CreateAccount(ctx, tenantA, &Account{Name: "grandchild", OwnerID: "owner-a", ParentID: child.ID})
		if err != nil {
			t.Fatalf("create grandchild: %v", err)
		}
		// Reparenting the grandchild directly onto root is a legal tree edit.
		grandchild.ParentID = root.ID
		if _, err := s.UpdateAccount(ctx, tenantA, grandchild); err != nil {
			t.Fatalf("valid reparent: %v", err)
		}
	})
}

// --- Ownership: required, and audited on change -------------------------

func TestAccountOwnerRequiredOnCreate(t *testing.T) {
	s := NewMemStore()
	_, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without owner_id: got %v, want ErrInvalid", err)
	}
}

func TestAccountOwnerRequiredOnUpdate(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	acct.OwnerID = ""
	_, err = s.UpdateAccount(ctx, tenantA, acct)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("update clearing owner_id: got %v, want ErrInvalid", err)
	}
}

// TestAccountOwnershipHistoryRecordedOnChange covers: an owner_id change
// appends a history row with the correct from/to, and a no-op update
// (owner_id unchanged) appends nothing.
func TestAccountOwnershipHistoryRecordedOnChange(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A non-op update (owner_id unchanged) must append no history row.
	acct.Industry = "software"
	acct, err = s.UpdateAccount(ctx, tenantA, acct)
	if err != nil {
		t.Fatalf("non-owner update: %v", err)
	}
	hist, err := s.ListOwnershipHistory(ctx, tenantA, acct.ID)
	if err != nil {
		t.Fatalf("list ownership history: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("ownership history has %d rows after a non-owner update, want 0", len(hist))
	}

	// Changing owner_id appends exactly one row recording from -> to.
	acct.OwnerID = "owner-2"
	updated, err := s.UpdateAccount(ctx, tenantA, acct)
	if err != nil {
		t.Fatalf("owner-changing update: %v", err)
	}
	hist, err = s.ListOwnershipHistory(ctx, tenantA, updated.ID)
	if err != nil {
		t.Fatalf("list ownership history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("ownership history has %d rows, want exactly 1", len(hist))
	}
	if hist[0].AccountID != updated.ID || hist[0].FromOwnerID != "owner-1" || hist[0].ToOwnerID != "owner-2" {
		t.Fatalf("ownership history row = %+v, want account %q owner-1 -> owner-2", hist[0], updated.ID)
	}
	if hist[0].ChangedAt.IsZero() {
		t.Fatal("ownership history row has a zero ChangedAt")
	}

	// A second owner change appends a second row without disturbing the first.
	updated.OwnerID = "owner-3"
	if _, err := s.UpdateAccount(ctx, tenantA, updated); err != nil {
		t.Fatalf("second owner-changing update: %v", err)
	}
	hist, err = s.ListOwnershipHistory(ctx, tenantA, updated.ID)
	if err != nil {
		t.Fatalf("list ownership history: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("ownership history has %d rows after a second change, want 2", len(hist))
	}
	if hist[1].FromOwnerID != "owner-2" || hist[1].ToOwnerID != "owner-3" {
		t.Fatalf("second ownership history row = %+v, want owner-2 -> owner-3", hist[1])
	}
}

// TestAccountOwnershipHistoryTenantIsolation is the mandatory tenancy test
// (.claude/rules/tenancy-and-scopes.md) for the new store method.
func TestAccountOwnershipHistoryTenantIsolation(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	acct.OwnerID = "owner-2"
	updated, err := s.UpdateAccount(ctx, tenantA, acct)
	if err != nil {
		t.Fatalf("owner-changing update: %v", err)
	}

	histB, err := s.ListOwnershipHistory(ctx, tenantB, updated.ID)
	if err != nil {
		t.Fatalf("tenant B list ownership history: %v", err)
	}
	if len(histB) != 0 {
		t.Fatalf("tenant B read %d of tenant A's ownership history rows; want 0", len(histB))
	}

	histA, err := s.ListOwnershipHistory(ctx, tenantA, updated.ID)
	if err != nil {
		t.Fatalf("tenant A list ownership history: %v", err)
	}
	if len(histA) != 1 {
		t.Fatalf("tenant A's own ownership history has %d rows, want 1", len(histA))
	}

	if _, err := s.ListOwnershipHistory(ctx, noScope, updated.ID); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("list ownership history with no tenant: got %v, want ErrNoTenant", err)
	}
}

// --- Exact-key dedup on domain -------------------------------------------

// TestAccountDuplicateFlaggingOnCreate covers B3's dedup rule: a matching
// Website domain is flagged (never blocked — the record is still created),
// and a non-matching domain is not flagged.
func TestAccountDuplicateFlaggingOnCreate(t *testing.T) {
	s := NewMemStore()
	first, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Corp", OwnerID: "owner-a", Website: "https://acme.com"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if first.DuplicateOfAccountID != "" {
		t.Fatalf("first account with a unique domain was flagged: %q", first.DuplicateOfAccountID)
	}

	t.Run("matching domain is flagged but still created", func(t *testing.T) {
		dup, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Inc", OwnerID: "owner-b", Website: "www.acme.com"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if dup.DuplicateOfAccountID != first.ID {
			t.Fatalf("DuplicateOfAccountID = %q, want %q", dup.DuplicateOfAccountID, first.ID)
		}
		// Still readable as a normal account — flagging never blocks creation.
		got, err := s.GetAccount(ctx, tenantA, dup.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ID != dup.ID {
			t.Fatal("account was not stored")
		}
	})

	t.Run("non-matching domain is not flagged", func(t *testing.T) {
		other, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Other Co", OwnerID: "owner-c", Website: "other.example"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if other.DuplicateOfAccountID != "" {
			t.Fatalf("DuplicateOfAccountID = %q, want empty", other.DuplicateOfAccountID)
		}
	})

	t.Run("no website is not flagged", func(t *testing.T) {
		noSite, err := s.CreateAccount(ctx, tenantA, &Account{Name: "No Site Co", OwnerID: "owner-d"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if noSite.DuplicateOfAccountID != "" {
			t.Fatalf("DuplicateOfAccountID = %q, want empty", noSite.DuplicateOfAccountID)
		}
	})
}

// TestAccountDuplicateFlagRecomputedOnUpdate covers that the flag is not
// last-write-wins stale: it is recomputed on every Update the same way
// Lead.DuplicateOfContactID is (B1).
func TestAccountDuplicateFlagRecomputedOnUpdate(t *testing.T) {
	s := NewMemStore()
	first, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Corp", OwnerID: "owner-a", Website: "acme.com"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	second, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Other Co", OwnerID: "owner-b", Website: "other.example"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if second.DuplicateOfAccountID != "" {
		t.Fatalf("DuplicateOfAccountID = %q, want empty at create", second.DuplicateOfAccountID)
	}

	second.Website = "acme.com"
	updated, err := s.UpdateAccount(ctx, tenantA, second)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.DuplicateOfAccountID != first.ID {
		t.Fatalf("DuplicateOfAccountID after update = %q, want %q", updated.DuplicateOfAccountID, first.ID)
	}

	// Changing the domain away clears the flag.
	updated.Website = "unique-again.example"
	cleared, err := s.UpdateAccount(ctx, tenantA, updated)
	if err != nil {
		t.Fatalf("update clearing domain: %v", err)
	}
	if cleared.DuplicateOfAccountID != "" {
		t.Fatalf("DuplicateOfAccountID after clearing domain = %q, want empty", cleared.DuplicateOfAccountID)
	}
}

// TestAccountDuplicateFlagIsTenantScoped covers the mandatory tenancy rule:
// a cross-tenant Account sharing the same domain must never flag another
// tenant's Account (.claude/rules/tenancy-and-scopes.md — dedup is not a
// global lookup).
func TestAccountDuplicateFlagIsTenantScoped(t *testing.T) {
	s := NewMemStore()
	if _, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Acme B", OwnerID: "owner-b", Website: "shared.example"}); err != nil {
		t.Fatalf("seed tenant B account: %v", err)
	}
	acctA, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme A", OwnerID: "owner-a", Website: "shared.example"})
	if err != nil {
		t.Fatalf("create tenant A account: %v", err)
	}
	if acctA.DuplicateOfAccountID != "" {
		t.Fatalf("tenant A account flagged against tenant B's account: %q", acctA.DuplicateOfAccountID)
	}
}
