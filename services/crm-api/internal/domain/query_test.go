package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseAccountSort(t *testing.T) {
	if got, err := ParseAccountSort(""); err != nil || got != SortAccountName {
		t.Fatalf("empty sort: got (%q, %v), want (%q, nil)", got, err, SortAccountName)
	}
	if got, err := ParseAccountSort("-created_at"); err != nil || got != SortAccountCreatedDesc {
		t.Fatalf("-created_at: got (%q, %v), want (%q, nil)", got, err, SortAccountCreatedDesc)
	}
	if _, err := ParseAccountSort("owner_id"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown sort: got %v, want ErrInvalid", err)
	}
}

func TestParseContactSort(t *testing.T) {
	if got, err := ParseContactSort(""); err != nil || got != SortContactCreatedDesc {
		t.Fatalf("empty sort: got (%q, %v), want (%q, nil)", got, err, SortContactCreatedDesc)
	}
	if got, err := ParseContactSort("email"); err != nil || got != SortContactEmail {
		t.Fatalf("email: got (%q, %v), want (%q, nil)", got, err, SortContactEmail)
	}
	if _, err := ParseContactSort("last_name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown sort: got %v, want ErrInvalid", err)
	}
}

func TestListAccounts_NameFilterAndSort(t *testing.T) {
	s := NewMemStore()
	names := []string{"Zebra Corp", "Acme Corp", "Middle Inc", "Acme Subsidiary"}
	for _, n := range names {
		if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: n, OwnerID: "owner-1"}); err != nil {
			t.Fatalf("seed %q: %v", n, err)
		}
	}

	rows, next, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{NameContains: "acme"}, SortAccountName)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if next != "" {
		t.Fatalf("expected a single page, got next=%q", next)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 accounts matching %q, got %d: %+v", "acme", len(rows), rows)
	}
	if rows[0].Name != "Acme Corp" || rows[1].Name != "Acme Subsidiary" {
		t.Fatalf("expected alphabetical order, got [%s, %s]", rows[0].Name, rows[1].Name)
	}
}

func TestListAccounts_TierAndOwnerFilter(t *testing.T) {
	s := NewMemStore()
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "A", Tier: "gold", OwnerID: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "B", Tier: "silver", OwnerID: "owner-2"}); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{Tier: "gold"}, SortAccountName)
	if err != nil {
		t.Fatalf("list by tier: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "A" {
		t.Fatalf("tier filter: got %+v, want just A", rows)
	}

	rows, _, err = s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{OwnerID: "owner-2"}, SortAccountName)
	if err != nil {
		t.Fatalf("list by owner: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "B" {
		t.Fatalf("owner filter: got %+v, want just B", rows)
	}
}

func TestListAccounts_ParentIDFilter(t *testing.T) {
	s := NewMemStore()
	parent, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Parent Co", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	otherParent, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Other Parent", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Child A", OwnerID: "o", ParentID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Child B", OwnerID: "o", ParentID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Unrelated", OwnerID: "o", ParentID: otherParent.ID}); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{ParentID: parent.ID}, SortAccountName)
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(rows) != 2 || rows[0].Name != "Child A" || rows[1].Name != "Child B" {
		t.Fatalf("parent filter: got %+v, want just Child A and Child B", rows)
	}
}

func TestListAccounts_CreatedDescSortPaginatesWithoutDuplicatesOrGaps(t *testing.T) {
	s := NewMemStore()
	const total = 23
	created := make(map[string]bool, total)
	for i := range total {
		out, err := s.CreateAccount(ctx, tenantA, &Account{Name: fmt.Sprintf("acct-%02d", i), OwnerID: "owner-1"})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		created[out.ID] = true
	}

	seen := map[string]bool{}
	token := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		rows, next, err := s.ListAccounts(ctx, tenantA, Page{Size: 7, Token: token}, AccountFilter{}, SortAccountCreatedDesc)
		if err != nil {
			t.Fatalf("list: %v", err)
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
	if len(seen) != total {
		t.Fatalf("paged through %d records, want %d", len(seen), total)
	}
	for id := range created {
		if !seen[id] {
			t.Fatalf("account %s never appeared in any page", id)
		}
	}
}

func TestListAccounts_InvalidPageTokenIsRejected(t *testing.T) {
	s := NewMemStore()
	if _, _, err := s.ListAccounts(ctx, tenantA, Page{Token: "not-a-valid-cursor"}, AccountFilter{}, SortAccountName); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestListContacts_AccountAndEmailFilter(t *testing.T) {
	s := NewMemStore()
	acctA, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acct A", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	acctB, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acct B", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acctA.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: "bob@other.com", AccountID: acctB.ID}); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.ListContacts(ctx, tenantA, Page{}, ContactFilter{AccountID: acctA.ID}, SortContactEmail)
	if err != nil {
		t.Fatalf("list by account: %v", err)
	}
	if len(rows) != 1 || rows[0].Email != "alice@acme.com" {
		t.Fatalf("account filter: got %+v, want just alice", rows)
	}

	rows, _, err = s.ListContacts(ctx, tenantA, Page{}, ContactFilter{EmailContains: "acme"}, SortContactEmail)
	if err != nil {
		t.Fatalf("list by email: %v", err)
	}
	if len(rows) != 1 || rows[0].Email != "alice@acme.com" {
		t.Fatalf("email filter: got %+v, want just alice", rows)
	}
}

func TestListContacts_EmailSortAscending(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acct", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"zed@acme.com", "amy@acme.com", "mona@acme.com"} {
		if _, err := s.CreateContact(ctx, tenantA, &Contact{Email: e, AccountID: acct.ID}); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := s.ListContacts(ctx, tenantA, Page{}, ContactFilter{}, SortContactEmail)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 || rows[0].Email != "amy@acme.com" || rows[1].Email != "mona@acme.com" || rows[2].Email != "zed@acme.com" {
		t.Fatalf("expected ascending email order, got %+v", rows)
	}
}

func TestUpdateAccount_OptimisticConcurrency(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Original", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}

	// A zero UpdatedAt (a caller that never round-trips it) is not checked.
	acct.Name = "First edit"
	updated, err := s.UpdateAccount(ctx, tenantA, acct)
	if err != nil {
		t.Fatalf("update with zero UpdatedAt: %v", err)
	}

	// A stale UpdatedAt (the record changed since it was read) is rejected.
	// The offset is unambiguous regardless of clock resolution — unlike
	// reusing acct.CreatedAt, which could (rarely, under heavy scheduling
	// contention) equal updated.UpdatedAt if two back-to-back time.Now()
	// calls landed in the same tick, making the "stale" value indistinguishable
	// from current and the test flaky.
	stalerCopy := cloneAccount(updated)
	stalerCopy.Name = "Stale edit"
	stalerCopy.UpdatedAt = updated.UpdatedAt.Add(-time.Hour)
	if _, err := s.UpdateAccount(ctx, tenantA, stalerCopy); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}

	// The matching UpdatedAt succeeds.
	fresh, err := s.GetAccount(ctx, tenantA, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Name = "Correct edit"
	if _, err := s.UpdateAccount(ctx, tenantA, fresh); err != nil {
		t.Fatalf("update with matching UpdatedAt: %v", err)
	}
}

func TestUpdateContact_OptimisticConcurrency(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acct", OwnerID: "o"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateContact(ctx, tenantA, &Contact{Email: "a@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatal(err)
	}

	stale := cloneContact(c)
	stale.FirstName = "Stale"
	stale.UpdatedAt = c.UpdatedAt.Add(-time.Hour)
	if _, err := s.UpdateContact(ctx, tenantA, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}

	fresh, err := s.GetContact(ctx, tenantA, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.FirstName = "Fresh"
	if _, err := s.UpdateContact(ctx, tenantA, fresh); err != nil {
		t.Fatalf("update with matching UpdatedAt: %v", err)
	}
}

func TestAccountValidate_FieldLimits(t *testing.T) {
	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'x'
		}
		return string(b)
	}
	cases := []struct {
		name string
		acct Account
	}{
		{"name too long", Account{Name: long(maxNameLen + 1)}},
		{"industry too long", Account{Name: "ok", Industry: long(maxIndustryLen + 1)}},
		{"tier too long", Account{Name: "ok", Tier: long(maxTierLen + 1)}},
		{"website too long", Account{Name: "ok", Website: long(maxWebsiteLen + 1)}},
		{"negative revenue", Account{Name: "ok", AnnualRevenue: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.acct.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}
}

func TestContactValidate_FieldLimitsAndEmailFormat(t *testing.T) {
	cases := []struct {
		name    string
		contact Contact
	}{
		{"missing @", Contact{Email: "not-an-email"}},
		{"trailing @", Contact{Email: "user@"}},
		{"leading @", Contact{Email: "@domain.com"}},
		{"no dot in domain", Contact{Email: "user@localhost"}},
		{"whitespace in local part", Contact{Email: "us er@domain.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.contact.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}

	if err := (&Contact{Email: "valid@example.com"}).Validate(); err != nil {
		t.Fatalf("expected a valid email to pass, got %v", err)
	}
}
