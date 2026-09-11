package domain

import (
	"errors"
	"testing"
)

// TestLeadCreateDefaultsStatusToNew covers B1's default: a Lead created with
// no explicit status starts at "new".
func TestLeadCreateDefaultsStatusToNew(t *testing.T) {
	s := NewMemStore()
	out, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.Status != LeadStatusNew {
		t.Fatalf("Status = %q, want %q", out.Status, LeadStatusNew)
	}
}

func TestLeadCreateRejectsUnknownStatus(t *testing.T) {
	s := NewMemStore()
	_, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", Status: "made-up-status"})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("create with unknown status: got %v, want ErrInvalidTransition", err)
	}
}

// TestLeadStatusTransitions is the transition table test: legal transitions,
// illegal transitions (skip-ahead and backward), and terminal-state
// rejection (both converted and disqualified, including between the two).
func TestLeadStatusTransitions(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{"new_to_working", LeadStatusNew, LeadStatusWorking, false},
		{"working_to_qualified", LeadStatusWorking, LeadStatusQualified, false},
		{"qualified_to_disqualified", LeadStatusQualified, LeadStatusDisqualified, false},
		{"unchanged_status_is_always_legal", LeadStatusWorking, LeadStatusWorking, false},

		{"skip_ahead_new_to_qualified", LeadStatusNew, LeadStatusQualified, true},
		{"skip_ahead_new_to_disqualified", LeadStatusNew, LeadStatusDisqualified, true},
		{"backward_qualified_to_new", LeadStatusQualified, LeadStatusNew, true},
		{"backward_working_to_new", LeadStatusWorking, LeadStatusNew, true},
		{"disqualified_is_terminal", LeadStatusDisqualified, LeadStatusWorking, true},
		{"reopen_disqualified_to_new", LeadStatusDisqualified, LeadStatusNew, true},
		{"disqualified_to_qualified_illegal", LeadStatusDisqualified, LeadStatusQualified, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemStore()
			lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", Status: tc.from})
			if err != nil {
				t.Fatalf("seed lead at status %q: %v", tc.from, err)
			}
			lead.Status = tc.to
			_, err = s.UpdateLead(ctx, tenantA, lead)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("%s -> %s: got %v, want ErrInvalidTransition", tc.from, tc.to, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s -> %s: got %v, want no error", tc.from, tc.to, err)
			}
		})
	}
}

// TestLeadConvertedIsTerminalAndGated covers that converted, specifically,
// can only ever be reached through ConvertLead — never by naming it directly
// on Create or Update — even though it is a legal edge in the abstract
// transition table (qualified -> converted).
func TestLeadConvertedIsTerminalAndGated(t *testing.T) {
	t.Run("create cannot set status to converted directly", func(t *testing.T) {
		s := NewMemStore()
		_, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", Status: LeadStatusConverted})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("create with status=converted: got %v, want ErrInvalid", err)
		}
	})

	t.Run("update cannot set status to converted directly", func(t *testing.T) {
		s := NewMemStore()
		lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", Status: LeadStatusQualified})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		lead.Status = LeadStatusConverted
		_, err = s.UpdateLead(ctx, tenantA, lead)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("update with status=converted: got %v, want ErrInvalid", err)
		}
	})

	t.Run("re-saving an already-converted lead with status=converted is allowed", func(t *testing.T) {
		s := NewMemStore()
		lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "lead@acme.com")
		result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		result.Lead.Company = "Acme Renamed"
		if _, err := s.UpdateLead(ctx, tenantA, result.Lead); err != nil {
			t.Fatalf("re-save of converted lead: %v", err)
		}
	})

	t.Run("converted cannot transition anywhere else", func(t *testing.T) {
		s := NewMemStore()
		lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "lead@acme.com")
		converted, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		converted.Lead.Status = LeadStatusWorking
		if _, err := s.UpdateLead(ctx, tenantA, converted.Lead); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("move converted lead backward: got %v, want ErrInvalidTransition", err)
		}
	})
}

// TestLeadDuplicateFlagging covers B1's exact-key dedup: a lead whose
// contact_email already belongs to an existing Contact is flagged, not
// silently duplicated (it is still created), and a non-matching email is not
// flagged. The flag is recomputed on Update too.
func TestLeadDuplicateFlagging(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	t.Run("matching email is flagged but still created", func(t *testing.T) {
		lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", ContactEmail: "Alice@Acme.com"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if lead.DuplicateOfContactID != contact.ID {
			t.Fatalf("DuplicateOfContactID = %q, want %q", lead.DuplicateOfContactID, contact.ID)
		}
		// Still readable as a normal lead — flagging never blocks creation.
		got, err := s.GetLead(ctx, tenantA, lead.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ID != lead.ID {
			t.Fatalf("lead was not stored")
		}
	})

	t.Run("non-matching email is not flagged", func(t *testing.T) {
		lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Other Co", ContactEmail: "bob@other.com"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if lead.DuplicateOfContactID != "" {
			t.Fatalf("DuplicateOfContactID = %q, want empty", lead.DuplicateOfContactID)
		}
	})

	t.Run("flag is recomputed on update", func(t *testing.T) {
		lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Other Co", ContactEmail: "carol@other.com"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if lead.DuplicateOfContactID != "" {
			t.Fatalf("DuplicateOfContactID = %q, want empty at create", lead.DuplicateOfContactID)
		}
		lead.ContactEmail = "alice@acme.com"
		updated, err := s.UpdateLead(ctx, tenantA, lead)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.DuplicateOfContactID != contact.ID {
			t.Fatalf("DuplicateOfContactID after update = %q, want %q", updated.DuplicateOfContactID, contact.ID)
		}
	})

	t.Run("a tenant B contact never flags a tenant A lead", func(t *testing.T) {
		otherAcct, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Tenant Co", OwnerID: "owner-b"})
		if err != nil {
			t.Fatalf("seed tenant B account: %v", err)
		}
		if _, err := s.CreateContact(ctx, tenantB, &Contact{Email: "shared@example.com", AccountID: otherAcct.ID}); err != nil {
			t.Fatalf("seed tenant B contact: %v", err)
		}
		lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Tenant A Co", ContactEmail: "shared@example.com"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if lead.DuplicateOfContactID != "" {
			t.Fatalf("tenant A lead flagged against tenant B's contact: %q", lead.DuplicateOfContactID)
		}
	})
}

func TestPersonFirstLeadNormalizesAndDeduplicatesByPhone(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Example", OwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	contact, err := s.CreateContact(ctx, tenantA, &Contact{
		FirstName: "Existing", Phone: "+15550100000", AccountID: acct.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lead, err := s.CreateLead(ctx, tenantA, &Lead{
		FirstName: " Alex ", LastName: " Rivera ", Phone: "+1 (555) 010-0000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lead.FirstName != "Alex" || lead.LastName != "Rivera" || lead.Phone != "+15550100000" {
		t.Fatalf("normalized lead = %+v", lead)
	}
	if lead.Company != "" || lead.ContactEmail != "" {
		t.Fatalf("optional company/email were invented: %+v", lead)
	}
	if lead.DuplicateOfContactID != contact.ID {
		t.Fatalf("phone duplicate = %q, want %q", lead.DuplicateOfContactID, contact.ID)
	}
	if _, err := s.CreateLead(ctx, tenantA, &Lead{FirstName: "Local", Phone: "9876543210"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("local phone without country code: %v", err)
	}
}

// TestFindContactByEmailTenantIsolation is the mandatory tenancy test
// (.claude/rules/tenancy-and-scopes.md) for this new lookup path.
func TestFindContactByEmailTenantIsolation(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}

	if _, err := s.FindContactByEmail(ctx, tenantB, "alice@acme.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B found tenant A's contact by email: got %v, want ErrNotFound", err)
	}

	got, err := s.FindContactByEmail(ctx, tenantA, "alice@acme.com")
	if err != nil {
		t.Fatalf("tenant A find by email: %v", err)
	}
	if got.ID != contact.ID {
		t.Fatalf("found contact %q, want %q", got.ID, contact.ID)
	}

	if _, err := s.FindContactByEmail(ctx, noScope, "alice@acme.com"); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("find with no tenant: got %v, want ErrNoTenant", err)
	}
}

// TestFindAccountByDomainTenantIsolation is the mandatory tenancy test for
// this new lookup path.
func TestFindAccountByDomainTenantIsolation(t *testing.T) {
	s := NewMemStore()
	acct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a", Website: "https://www.acme.com/about"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	if _, err := s.FindAccountByDomain(ctx, tenantB, "acme.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B found tenant A's account by domain: got %v, want ErrNotFound", err)
	}

	got, err := s.FindAccountByDomain(ctx, tenantA, "acme.com")
	if err != nil {
		t.Fatalf("tenant A find by domain: %v", err)
	}
	if got.ID != acct.ID {
		t.Fatalf("found account %q, want %q", got.ID, acct.ID)
	}

	if _, err := s.FindAccountByDomain(ctx, noScope, "acme.com"); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("find with no tenant: got %v, want ErrNoTenant", err)
	}
}

// mustProgressLeadToQualified seeds a Lead and walks it new -> working ->
// qualified through UpdateLead, the only legal path to a state ConvertLead
// will accept.
func mustProgressLeadToQualified(t *testing.T, s *MemStore, sc Scope, company, email string) *Lead {
	t.Helper()
	lead, err := s.CreateLead(ctx, sc, &Lead{Company: company, ContactEmail: email})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	lead.Status = LeadStatusWorking
	lead, err = s.UpdateLead(ctx, sc, lead)
	if err != nil {
		t.Fatalf("advance to working: %v", err)
	}
	lead.Status = LeadStatusQualified
	lead, err = s.UpdateLead(ctx, sc, lead)
	if err != nil {
		t.Fatalf("advance to qualified: %v", err)
	}
	return lead
}
