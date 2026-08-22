package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestPgStoreListAccounts_FilterSortAndKeysetPagination proves the dynamic
// WHERE-clause building and SQL row-value keyset comparison in
// pgstore.go's ListAccounts behave the same way against real Postgres as
// MemStore's equivalent (query_test.go's TestListAccounts_* tests).
func TestPgStoreListAccounts_FilterSortAndKeysetPagination(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	tenant := Scope{TenantID: fmt.Sprintf("tenant-crm-pg-acct-%d", time.Now().UnixNano()), OrgID: "org-a"}

	names := []string{"Zebra Corp", "Acme Corp", "Middle Inc", "Acme Subsidiary"}
	for _, n := range names {
		if _, err := s.CreateAccount(ctx, tenant, &Account{Name: n, OwnerID: "owner-1"}); err != nil {
			t.Fatalf("seed %q: %v", n, err)
		}
	}

	rows, next, err := s.ListAccounts(ctx, tenant, Page{}, AccountFilter{NameContains: "acme"}, SortAccountName)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if next != "" {
		t.Fatalf("expected a single page, got next=%q", next)
	}
	if len(rows) != 2 || rows[0].Name != "Acme Corp" || rows[1].Name != "Acme Subsidiary" {
		t.Fatalf("filter+sort: got %+v", rows)
	}

	// Tier/owner filters and the indexed website_domain lookup.
	acct, err := s.CreateAccount(ctx, tenant, &Account{Name: "Golden Co", Tier: "gold", OwnerID: "owner-2", Website: "https://www.golden-co.example/about"})
	if err != nil {
		t.Fatalf("create golden co: %v", err)
	}
	byTier, _, err := s.ListAccounts(ctx, tenant, Page{}, AccountFilter{Tier: "gold"}, SortAccountName)
	if err != nil {
		t.Fatalf("list by tier: %v", err)
	}
	if len(byTier) != 1 || byTier[0].ID != acct.ID {
		t.Fatalf("tier filter: got %+v, want just %s", byTier, acct.ID)
	}
	byOwner, _, err := s.ListAccounts(ctx, tenant, Page{}, AccountFilter{OwnerID: "owner-2"}, SortAccountName)
	if err != nil {
		t.Fatalf("list by owner: %v", err)
	}
	if len(byOwner) != 1 || byOwner[0].ID != acct.ID {
		t.Fatalf("owner filter: got %+v, want just %s", byOwner, acct.ID)
	}

	found, err := s.FindAccountByDomain(ctx, tenant, "golden-co.example")
	if err != nil {
		t.Fatalf("find by domain: %v", err)
	}
	if found == nil || found.ID != acct.ID {
		t.Fatalf("FindAccountByDomain = %+v, want %s", found, acct.ID)
	}

	// ParentID filter (the account detail page's "child accounts" view).
	child, err := s.CreateAccount(ctx, tenant, &Account{Name: "Child Co", OwnerID: "owner-1", ParentID: acct.ID})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	byParent, _, err := s.ListAccounts(ctx, tenant, Page{}, AccountFilter{ParentID: acct.ID}, SortAccountName)
	if err != nil {
		t.Fatalf("list by parent: %v", err)
	}
	if len(byParent) != 1 || byParent[0].ID != child.ID {
		t.Fatalf("parent filter: got %+v, want just %s", byParent, child.ID)
	}

	// Multi-page keyset pagination over -created_at, no duplicates or gaps.
	const extra = 15
	created := map[string]bool{}
	for i := range extra {
		out, err := s.CreateAccount(ctx, tenant, &Account{Name: fmt.Sprintf("page-acct-%02d", i), OwnerID: "owner-3"})
		if err != nil {
			t.Fatalf("seed page account %d: %v", i, err)
		}
		created[out.ID] = true
	}
	seen := map[string]bool{}
	token := ""
	for pages := 0; ; pages++ {
		if pages > extra+len(names)+1 {
			t.Fatal("pagination did not terminate")
		}
		rows, next, err := s.ListAccounts(ctx, tenant, Page{Size: 4, Token: token}, AccountFilter{}, SortAccountCreatedDesc)
		if err != nil {
			t.Fatalf("paginated list: %v", err)
		}
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("record %s returned twice", r.ID)
			}
			seen[r.ID] = true
		}
		if next == "" {
			break
		}
		token = next
	}
	for id := range created {
		if !seen[id] {
			t.Fatalf("account %s never appeared in any page", id)
		}
	}
}

// TestPgStoreListContacts_FilterAndSort proves ListContacts' account_id and
// email_contains filters, and the "email" sort, against real Postgres.
func TestPgStoreListContacts_FilterAndSort(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	tenant := Scope{TenantID: fmt.Sprintf("tenant-crm-pg-contact-%d", time.Now().UnixNano()), OrgID: "org-a"}

	acctA, err := s.CreateAccount(ctx, tenant, &Account{Name: "Acct A", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	acctB, err := s.CreateAccount(ctx, tenant, &Account{Name: "Acct B", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, tenant, &Contact{Email: "zed@acme.com", AccountID: acctA.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, tenant, &Contact{Email: "amy@acme.com", AccountID: acctA.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, tenant, &Contact{Email: "bob@other.com", AccountID: acctB.ID}); err != nil {
		t.Fatal(err)
	}

	byAccount, _, err := s.ListContacts(ctx, tenant, Page{}, ContactFilter{AccountID: acctA.ID}, SortContactEmail)
	if err != nil {
		t.Fatalf("list by account: %v", err)
	}
	if len(byAccount) != 2 || byAccount[0].Email != "amy@acme.com" || byAccount[1].Email != "zed@acme.com" {
		t.Fatalf("account filter + email sort: got %+v", byAccount)
	}

	byEmail, _, err := s.ListContacts(ctx, tenant, Page{}, ContactFilter{EmailContains: "other"}, SortContactEmail)
	if err != nil {
		t.Fatalf("list by email: %v", err)
	}
	if len(byEmail) != 1 || byEmail[0].Email != "bob@other.com" {
		t.Fatalf("email filter: got %+v, want just bob", byEmail)
	}
}

// TestPgStoreUpdateAccount_OptimisticConcurrency proves the UpdatedAt
// mismatch check in pgstore.go's UpdateAccount rejects a write built from a
// stale read, using a real UPDATE ... WHERE against Postgres rather than
// MemStore's in-memory comparison.
func TestPgStoreUpdateAccount_OptimisticConcurrency(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	tenant := Scope{TenantID: fmt.Sprintf("tenant-crm-pg-optcon-%d", time.Now().UnixNano()), OrgID: "org-a"}

	acct, err := s.CreateAccount(ctx, tenant, &Account{Name: "Original", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}

	// Zero UpdatedAt (a caller that never round-trips it) is not checked.
	acct.Name = "First edit"
	updated, err := s.UpdateAccount(ctx, tenant, acct)
	if err != nil {
		t.Fatalf("update with zero UpdatedAt: %v", err)
	}

	stale := cloneAccount(updated)
	stale.Name = "Stale edit"
	stale.UpdatedAt = updated.UpdatedAt.Add(-time.Hour)
	if _, err := s.UpdateAccount(ctx, tenant, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}

	fresh, err := s.GetAccount(ctx, tenant, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Name = "Correct edit"
	if _, err := s.UpdateAccount(ctx, tenant, fresh); err != nil {
		t.Fatalf("update with matching UpdatedAt: %v", err)
	}
}

// TestPgStoreUpdateContact_OptimisticConcurrency is UpdateAccount's sibling
// test for UpdateContact.
func TestPgStoreUpdateContact_OptimisticConcurrency(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	tenant := Scope{TenantID: fmt.Sprintf("tenant-crm-pg-optcon-contact-%d", time.Now().UnixNano()), OrgID: "org-a"}

	acct, err := s.CreateAccount(ctx, tenant, &Account{Name: "Acct", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateContact(ctx, tenant, &Contact{Email: "a@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatal(err)
	}

	stale := cloneContact(c)
	stale.FirstName = "Stale"
	stale.UpdatedAt = c.UpdatedAt.Add(-time.Hour)
	if _, err := s.UpdateContact(ctx, tenant, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}

	fresh, err := s.GetContact(ctx, tenant, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.FirstName = "Fresh"
	if _, err := s.UpdateContact(ctx, tenant, fresh); err != nil {
		t.Fatalf("update with matching UpdatedAt: %v", err)
	}
}

// TestPgStoreListAccounts_InvalidPageTokenIsRejected proves a malformed
// cursor token fails fast with ErrInvalid rather than as a SQL error.
func TestPgStoreListAccounts_InvalidPageTokenIsRejected(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	tenant := Scope{TenantID: fmt.Sprintf("tenant-crm-pg-badtoken-%d", time.Now().UnixNano()), OrgID: "org-a"}

	if _, _, err := s.ListAccounts(ctx, tenant, Page{Token: "not-a-valid-cursor"}, AccountFilter{}, SortAccountName); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}
