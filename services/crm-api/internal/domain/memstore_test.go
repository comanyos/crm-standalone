package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var ctx = context.Background()

// tenantA and tenantB exist to prove records never cross between them.
var (
	tenantA = Scope{TenantID: "tenant-a", OrgID: "org-a"}
	tenantB = Scope{TenantID: "tenant-b", OrgID: "org-b"}
	noScope = Scope{}
)

// entityCase exercises one entity through the parts of the Store contract that are
// identical for every entity.
type entityCase struct {
	name          string
	create        func(*MemStore, Scope, string) (string, error)
	get           func(*MemStore, Scope, string) error
	createInvalid func(*MemStore, Scope) error
	del           func(*MemStore, Scope, string) error
}

func entityCases() []entityCase {
	return []entityCase{
		{
			name: "Account",
			create: func(s *MemStore, sc Scope, v string) (string, error) {
				out, err := s.CreateAccount(ctx, sc, &Account{Name: v, OwnerID: "owner-" + v})
				if err != nil {
					return "", err
				}
				return out.ID, nil
			},
			get: func(s *MemStore, sc Scope, id string) error {
				_, err := s.GetAccount(ctx, sc, id)
				return err
			},
			createInvalid: func(s *MemStore, sc Scope) error {
				_, err := s.CreateAccount(ctx, sc, &Account{})
				return err
			},
			del: func(s *MemStore, sc Scope, id string) error {
				return s.DeleteAccount(ctx, sc, id)
			},
		},
		{
			name: "Contact",
			// Referential integrity (B4) means a Contact now needs an
			// existing account_id: seed one under the same scope before
			// creating the Contact itself — the same adjustment B2 made
			// for Opportunity above.
			create: func(s *MemStore, sc Scope, v string) (string, error) {
				acct, err := s.CreateAccount(ctx, sc, &Account{Name: "seed-account-" + v, OwnerID: "owner-" + v})
				if err != nil {
					return "", err
				}
				out, err := s.CreateContact(ctx, sc, &Contact{Email: v + "@example.com", AccountID: acct.ID})
				if err != nil {
					return "", err
				}
				return out.ID, nil
			},
			get: func(s *MemStore, sc Scope, id string) error {
				_, err := s.GetContact(ctx, sc, id)
				return err
			},
			createInvalid: func(s *MemStore, sc Scope) error {
				_, err := s.CreateContact(ctx, sc, &Contact{})
				return err
			},
			del: func(s *MemStore, sc Scope, id string) error {
				return s.DeleteContact(ctx, sc, id)
			},
		},
		{
			name: "Lead",
			create: func(s *MemStore, sc Scope, v string) (string, error) {
				out, err := s.CreateLead(ctx, sc, &Lead{Company: v})
				if err != nil {
					return "", err
				}
				return out.ID, nil
			},
			get: func(s *MemStore, sc Scope, id string) error {
				_, err := s.GetLead(ctx, sc, id)
				return err
			},
			createInvalid: func(s *MemStore, sc Scope) error {
				_, err := s.CreateLead(ctx, sc, &Lead{})
				return err
			},
			del: func(s *MemStore, sc Scope, id string) error {
				return s.DeleteLead(ctx, sc, id)
			},
		},
		{
			name: "Opportunity",
			// Referential integrity (B2) means an Opportunity now needs an
			// existing account_id: seed one under the same scope before
			// creating the Opportunity itself.
			create: func(s *MemStore, sc Scope, v string) (string, error) {
				acct, err := s.CreateAccount(ctx, sc, &Account{Name: "seed-account-" + v, OwnerID: "owner-" + v})
				if err != nil {
					return "", err
				}
				out, err := s.CreateOpportunity(ctx, sc, &Opportunity{Name: v, AccountID: acct.ID})
				if err != nil {
					return "", err
				}
				return out.ID, nil
			},
			get: func(s *MemStore, sc Scope, id string) error {
				_, err := s.GetOpportunity(ctx, sc, id)
				return err
			},
			createInvalid: func(s *MemStore, sc Scope) error {
				_, err := s.CreateOpportunity(ctx, sc, &Opportunity{})
				return err
			},
			del: func(s *MemStore, sc Scope, id string) error {
				return s.DeleteOpportunity(ctx, sc, id)
			},
		},
		{
			name: "Activity",
			// Referential integrity (B5) means an Activity now needs a real
			// polymorphic subject: seed an Account under the same scope and
			// point subject_type/subject_id at it, the same adjustment B2
			// and B4 made for Opportunity and Contact above.
			create: func(s *MemStore, sc Scope, v string) (string, error) {
				acct, err := s.CreateAccount(ctx, sc, &Account{Name: "seed-account-" + v, OwnerID: "owner-" + v})
				if err != nil {
					return "", err
				}
				out, err := s.CreateActivity(ctx, sc, &Activity{Kind: v, SubjectType: ActivitySubjectAccount, SubjectID: acct.ID})
				if err != nil {
					return "", err
				}
				return out.ID, nil
			},
			get: func(s *MemStore, sc Scope, id string) error {
				_, err := s.GetActivity(ctx, sc, id)
				return err
			},
			createInvalid: func(s *MemStore, sc Scope) error {
				_, err := s.CreateActivity(ctx, sc, &Activity{})
				return err
			},
			del: func(s *MemStore, sc Scope, id string) error {
				return s.DeleteActivity(ctx, sc, id)
			},
		},
	}
}

func TestCreateThenGet(t *testing.T) {
	for _, tc := range entityCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			id, err := tc.create(s, tenantA, "value-1")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if id == "" {
				t.Fatal("create returned an empty id")
			}
			if err := tc.get(s, tenantA, id); err != nil {
				t.Fatalf("get after create: %v", err)
			}
		})
	}
}

// TestTenantIsolation is the test that matters: a record written by one tenant must
// be invisible to another even when the exact ID is known.
func TestTenantIsolation(t *testing.T) {
	for _, tc := range entityCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			id, err := tc.create(s, tenantA, "tenant-a-secret")
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			err = tc.get(s, tenantB, id)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("tenant B read tenant A's record: got %v, want ErrNotFound", err)
			}

			if err := tc.del(s, tenantB, id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("tenant B deleted tenant A's record: got %v, want ErrNotFound", err)
			}
			if err := tc.get(s, tenantA, id); err != nil {
				t.Fatalf("tenant A's record was affected by tenant B: %v", err)
			}
		})
	}
}

func TestEmptyScopeRejected(t *testing.T) {
	for _, tc := range entityCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			if _, err := tc.create(s, noScope, "x"); !errors.Is(err, ErrNoTenant) {
				t.Fatalf("create with no tenant: got %v, want ErrNoTenant", err)
			}
			if err := tc.get(s, noScope, "any"); !errors.Is(err, ErrNoTenant) {
				t.Fatalf("get with no tenant: got %v, want ErrNoTenant", err)
			}
		})
	}
}

func TestValidationRejectsEmptyRequiredField(t *testing.T) {
	for _, tc := range entityCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			if err := tc.createInvalid(s, tenantA); !errors.Is(err, ErrInvalid) {
				t.Fatalf("create with empty required field: got %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDeleteIsIdempotentlyNotFound(t *testing.T) {
	for _, tc := range entityCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			id, err := tc.create(s, tenantA, "v")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := tc.del(s, tenantA, id); err != nil {
				t.Fatalf("first delete: %v", err)
			}
			if err := tc.del(s, tenantA, id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("second delete: got %v, want ErrNotFound", err)
			}
		})
	}
}

// TestListPaginates walks the whole set one small page at a time and checks the walk
// terminates and returns every record exactly once.
func TestListPaginates(t *testing.T) {
	s := NewMemStore()
	const total = 25
	for i := range total {
		if _, err := s.CreateAccount(ctx, tenantA,
			&Account{Name: fmt.Sprintf("row-%02d", i), OwnerID: "owner-x"}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	token := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		rows, next, err := s.ListAccounts(ctx, tenantA, Page{Size: 10, Token: token}, AccountFilter{}, SortAccountName)
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
}

func TestListIsTenantScoped(t *testing.T) {
	s := NewMemStore()
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "a", OwnerID: "owner-a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, _, err := s.ListAccounts(ctx, tenantB, Page{}, AccountFilter{}, SortAccountName)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("tenant B listed %d of tenant A's records", len(rows))
	}
}

func TestSummaryCountsOnlyOwnTenant(t *testing.T) {
	s := NewMemStore()
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "a", OwnerID: "owner-a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sum, err := s.Summary(ctx, tenantB)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Total != 0 {
		t.Fatalf("tenant B summary counted %d of tenant A's records", sum.Total)
	}
}

func TestUpdatePreservesCreatedAt(t *testing.T) {
	s := NewMemStore()
	created, err := s.CreateAccount(ctx, tenantA, &Account{Name: "before", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	created.Name = "after"
	updated, err := s.UpdateAccount(ctx, tenantA, created)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("update changed CreatedAt: %v -> %v", created.CreatedAt, updated.CreatedAt)
	}
	if updated.Name != "after" {
		t.Fatalf("update did not persist the change: %q", updated.Name)
	}
}

// TestNewFieldsRoundTrip covers the A0 schema additions: Account.ParentID,
// Opportunity.CloseReason, Lead.Source, and Activity.SubjectType/OccurredAt
// all persist and read back unchanged. No business rule is asserted here —
// that is Track B.
func TestNewFieldsRoundTrip(t *testing.T) {
	s := NewMemStore()

	parent, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Parent Co", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create parent account: %v", err)
	}
	child, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Child Co", OwnerID: "owner-a", ParentID: parent.ID})
	if err != nil {
		t.Fatalf("create child account: %v", err)
	}
	got, err := s.GetAccount(ctx, tenantA, child.ID)
	if err != nil {
		t.Fatalf("get child account: %v", err)
	}
	if got.ParentID != parent.ID {
		t.Fatalf("Account.ParentID = %q, want %q", got.ParentID, parent.ID)
	}

	lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", Source: "webinar"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	gotLead, err := s.GetLead(ctx, tenantA, lead.ID)
	if err != nil {
		t.Fatalf("get lead: %v", err)
	}
	if gotLead.Source != "webinar" {
		t.Fatalf("Lead.Source = %q, want %q", gotLead.Source, "webinar")
	}

	oppAccount, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Big Deal Co", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create opportunity's account: %v", err)
	}
	opp, err := s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Big Deal", AccountID: oppAccount.ID})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	opp.CloseReason = "won-competitive"
	updatedOpp, err := s.UpdateOpportunity(ctx, tenantA, opp)
	if err != nil {
		t.Fatalf("update opportunity: %v", err)
	}
	if updatedOpp.CloseReason != "won-competitive" {
		t.Fatalf("Opportunity.CloseReason = %q, want %q", updatedOpp.CloseReason, "won-competitive")
	}
	gotOpp, err := s.GetOpportunity(ctx, tenantA, opp.ID)
	if err != nil {
		t.Fatalf("get opportunity: %v", err)
	}
	if gotOpp.CloseReason != "won-competitive" {
		t.Fatalf("persisted Opportunity.CloseReason = %q, want %q", gotOpp.CloseReason, "won-competitive")
	}

	now := time.Now().UTC().Truncate(time.Second)
	act, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "call", SubjectType: "opportunity", SubjectID: opp.ID, OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("create activity: %v", err)
	}
	gotAct, err := s.GetActivity(ctx, tenantA, act.ID)
	if err != nil {
		t.Fatalf("get activity: %v", err)
	}
	if gotAct.SubjectType != "opportunity" {
		t.Fatalf("Activity.SubjectType = %q, want %q", gotAct.SubjectType, "opportunity")
	}
	if !gotAct.OccurredAt.Equal(now) {
		t.Fatalf("Activity.OccurredAt = %v, want %v", gotAct.OccurredAt, now)
	}
}

func TestUpdateUnknownIDIsNotFound(t *testing.T) {
	s := NewMemStore()
	_, err := s.UpdateAccount(ctx, tenantA, &Account{ID: "missing", Name: "x"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown id: got %v, want ErrNotFound", err)
	}
}
