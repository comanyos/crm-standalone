package domain

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestPgStoreAccountHierarchy exercises B3's rules against real Postgres:
// owner_id required, the parent acyclicity invariant, dedup flagging, and
// ownership history (plus its tenant isolation) — the same behaviors
// account_hierarchy_test.go proves against MemStore, run here through
// PgStore/scopedDB so the SQL paths are not just compiled, but verified.
func TestPgStoreAccountHierarchy(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-crm-hierarchy-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-crm-hierarchy-b-%d", stamp), OrgID: "org-b"}

	// owner_id required on Create and Update.
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "No Owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create without owner_id: got %v, want ErrInvalid", err)
	}

	root, err := s.CreateAccount(ctx, tenantA, &Account{Name: "root", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	root.OwnerID = ""
	if _, err := s.UpdateAccount(ctx, tenantA, root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update clearing owner_id: got %v, want ErrInvalid", err)
	}
	root.OwnerID = "owner-a" // restore for the rest of the test

	// Dangling / cross-tenant parent_id rejected.
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "eng", OwnerID: "owner-a", ParentID: "does-not-exist"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with dangling parent: got %v, want ErrInvalid", err)
	}
	if _, err := s.CreateAccount(ctx, tenantB, &Account{Name: "eng", OwnerID: "owner-b", ParentID: root.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with cross-tenant parent: got %v, want ErrInvalid", err)
	}

	// Cycle rejection: self-parent and a deeper cycle.
	child, err := s.CreateAccount(ctx, tenantA, &Account{Name: "child", OwnerID: "owner-a", ParentID: root.ID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	grandchild, err := s.CreateAccount(ctx, tenantA, &Account{Name: "grandchild", OwnerID: "owner-a", ParentID: child.ID})
	if err != nil {
		t.Fatalf("create grandchild: %v", err)
	}
	root.ParentID = grandchild.ID
	if _, err := s.UpdateAccount(ctx, tenantA, root); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cycle through descendant: got %v, want ErrInvalidTransition", err)
	}
	// Legal reparenting still works.
	grandchild.ParentID = root.ID
	if _, err := s.UpdateAccount(ctx, tenantA, grandchild); err != nil {
		t.Fatalf("valid reparent: %v", err)
	}

	// Dedup flagging on domain, tenant-scoped.
	first, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Corp", OwnerID: "owner-a", Website: "acme-pg.example"})
	if err != nil {
		t.Fatalf("create first domain account: %v", err)
	}
	dup, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Inc", OwnerID: "owner-a", Website: "www.acme-pg.example"})
	if err != nil {
		t.Fatalf("create duplicate domain account: %v", err)
	}
	if dup.DuplicateOfAccountID != first.ID {
		t.Fatalf("DuplicateOfAccountID = %q, want %q", dup.DuplicateOfAccountID, first.ID)
	}
	if _, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Acme B", OwnerID: "owner-b", Website: "acme-pg.example"}); err != nil {
		t.Fatalf("create tenant B account with same domain: %v", err)
	}
	// Re-fetch to prove the flag round-trips through Postgres.
	gotDup, err := s.GetAccount(ctx, tenantA, dup.ID)
	if err != nil {
		t.Fatalf("get duplicate account: %v", err)
	}
	if gotDup.DuplicateOfAccountID != first.ID {
		t.Fatalf("persisted DuplicateOfAccountID = %q, want %q", gotDup.DuplicateOfAccountID, first.ID)
	}

	// Ownership history: an owner change appends a row; a no-op update does
	// not; tenant B cannot read tenant A's history.
	dup.Industry = "software"
	dup, err = s.UpdateAccount(ctx, tenantA, dup)
	if err != nil {
		t.Fatalf("non-owner update: %v", err)
	}
	histAfterNoop, err := s.ListOwnershipHistory(ctx, tenantA, dup.ID)
	if err != nil {
		t.Fatalf("list ownership history: %v", err)
	}
	if len(histAfterNoop) != 0 {
		t.Fatalf("ownership history has %d rows after a non-owner update, want 0", len(histAfterNoop))
	}

	dup.OwnerID = "owner-new"
	updated, err := s.UpdateAccount(ctx, tenantA, dup)
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
	if histA[0].FromOwnerID != "owner-a" || histA[0].ToOwnerID != "owner-new" {
		t.Fatalf("ownership history row = %+v, want owner-a -> owner-new", histA[0])
	}
}

// TestPgStoreAccountDuplicateFlagUnderConcurrentWrites pins down the
// question a flaky browser test raised: whether the dedup scan can miss a
// domain that is already committed, because applyAccountDuplicateFlag's
// FindAccountByDomain read and the INSERT/UPDATE that maintains
// accounts.website_domain (migrations/0010) run in separate transactions.
//
// It cannot. The read is READ COMMITTED and the writer's transaction has
// already committed by the time the next request starts, so a
// create-then-update-then-create sequence sees the domain every time — even
// with the whole tenant being written to concurrently, which is what the
// browser suite does under parallel workers. What the separate transactions
// do leave open is the narrower window where two accounts claiming the same
// domain are created at literally the same instant and neither can see the
// other's uncommitted row; that is the documented "flag, don't block"
// behaviour degrading to "don't flag", not a lost write, and it is
// deliberately not asserted here because its outcome is a scheduling
// coin-flip rather than a contract.
func TestPgStoreAccountDuplicateFlagUnderConcurrentWrites(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	tenant := Scope{TenantID: fmt.Sprintf("tenant-crm-dedup-concurrent-%d", time.Now().UnixNano()), OrgID: "org-a"}

	// Background churn on one shared domain, so the sequence below is never
	// the only writer in the tenant.
	stop := make(chan struct{})
	var noise sync.WaitGroup
	for n := 0; n < 4; n++ {
		noise.Add(1)
		go func(n int) {
			defer noise.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				acct, err := s.CreateAccount(ctx, tenant, &Account{
					Name: fmt.Sprintf("noise-%d", n), OwnerID: "owner-a", Website: "https://shared-noise.example",
				})
				if err != nil {
					return
				}
				if err := s.DeleteAccount(ctx, tenant, acct.ID); err != nil {
					return
				}
			}
		}(n)
	}

	// The browser suite's sequence, per goroutine on its own domain:
	// create an account without a website, give it one, then create a
	// second account with that same website and expect it flagged.
	var seq sync.WaitGroup
	for i := 0; i < 16; i++ {
		seq.Add(1)
		go func(i int) {
			defer seq.Done()
			website := fmt.Sprintf("https://dedup-%d-%d.example", time.Now().UnixNano(), i)

			first, err := s.CreateAccount(ctx, tenant, &Account{Name: fmt.Sprintf("first-%d", i), OwnerID: "owner-a"})
			if err != nil {
				t.Errorf("create first: %v", err)
				return
			}
			first.Website = website
			if _, err := s.UpdateAccount(ctx, tenant, first); err != nil {
				t.Errorf("set first's website: %v", err)
				return
			}

			second, err := s.CreateAccount(ctx, tenant, &Account{
				Name: fmt.Sprintf("second-%d", i), OwnerID: "owner-a", Website: website,
			})
			if err != nil {
				t.Errorf("create second: %v", err)
				return
			}
			if second.DuplicateOfAccountID != first.ID {
				t.Errorf("second account for %s: duplicate_of_account_id = %q, want %q",
					website, second.DuplicateOfAccountID, first.ID)
			}
		}(i)
	}
	seq.Wait()
	close(stop)
	noise.Wait()
}
