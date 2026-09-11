package domain

import (
	"errors"
	"testing"
)

// TestConvertLeadHappyPath covers B1 item 2: converting a qualified lead
// creates exactly one Contact and one Account, attaches the Contact to the
// Account, and marks the Lead converted with the linked ids.
func TestConvertLeadHappyPath(t *testing.T) {
	s := NewMemStore()
	lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "alice@acme.com")

	result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if result.Lead.Status != LeadStatusConverted {
		t.Fatalf("Lead.Status = %q, want %q", result.Lead.Status, LeadStatusConverted)
	}
	if result.Contact == nil || result.Contact.Email != "alice@acme.com" {
		t.Fatalf("Contact = %+v, want email alice@acme.com", result.Contact)
	}
	if result.Account == nil || result.Account.Name != "Acme" {
		t.Fatalf("Account = %+v, want name Acme", result.Account)
	}
	if result.Contact.AccountID != result.Account.ID {
		t.Fatalf("Contact.AccountID = %q, want %q (referential integrity)", result.Contact.AccountID, result.Account.ID)
	}
	if result.Opportunity != nil {
		t.Fatalf("Opportunity = %+v, want nil (create_opportunity was not requested)", result.Opportunity)
	}

	if result.Lead.ConvertedContactID != result.Contact.ID {
		t.Fatalf("Lead.ConvertedContactID = %q, want %q", result.Lead.ConvertedContactID, result.Contact.ID)
	}
	if result.Lead.ConvertedAccountID != result.Account.ID {
		t.Fatalf("Lead.ConvertedAccountID = %q, want %q", result.Lead.ConvertedAccountID, result.Account.ID)
	}
	if result.Lead.ConvertedOpportunityID != "" {
		t.Fatalf("Lead.ConvertedOpportunityID = %q, want empty", result.Lead.ConvertedOpportunityID)
	}

	// Exactly one Account and one Contact exist in the tenant.
	accts, _, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{}, SortAccountName)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accts) != 1 {
		t.Fatalf("tenant has %d accounts, want exactly 1", len(accts))
	}
	contacts, _, err := s.ListContacts(ctx, tenantA, Page{}, ContactFilter{}, SortContactCreatedDesc)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("tenant has %d contacts, want exactly 1", len(contacts))
	}
}

// TestConvertLeadRejectsIllegalSourceStatus covers that ConvertLead enforces
// the same status transition table as UpdateLead: only a qualified lead may
// convert.
func TestConvertLeadRejectsIllegalSourceStatus(t *testing.T) {
	s := NewMemStore()
	lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", ContactEmail: "alice@acme.com"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// lead.Status is "new" — converting straight from "new" is illegal.
	if _, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("convert from new: got %v, want ErrInvalidTransition", err)
	}
}

// TestConvertLeadRequiresAContactAddress covers that a lead with neither
// email nor phone cannot be converted (there is nothing to address the
// Contact by).
func TestConvertLeadRequiresAContactAddress(t *testing.T) {
	s := NewMemStore()
	lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	lead.Status = LeadStatusWorking
	lead, err = s.UpdateLead(ctx, tenantA, lead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	lead.Status = LeadStatusQualified
	lead, err = s.UpdateLead(ctx, tenantA, lead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("convert with no email or phone: got %v, want ErrInvalid", err)
	}
}

func TestConvertPhoneOnlyPersonFirstLead(t *testing.T) {
	s := NewMemStore()
	lead, err := s.CreateLead(ctx, tenantA, &Lead{
		FirstName: " Alex ", LastName: " Rivera ", Phone: "+1 (555) 010-0000",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{LeadStatusWorking, LeadStatusQualified} {
		lead.Status = status
		lead, err = s.UpdateLead(ctx, tenantA, lead)
		if err != nil {
			t.Fatalf("advance to %s: %v", status, err)
		}
	}
	result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Contact.FirstName != "Alex" || result.Contact.LastName != "Rivera" ||
		result.Contact.Phone != "+15550100000" || result.Contact.Email != "" {
		t.Fatalf("converted contact = %+v", result.Contact)
	}
	if result.Account.Name != "Alex Rivera" {
		t.Fatalf("person-first fallback account = %+v", result.Account)
	}
}

// TestConvertLeadReusesExistingAccountByDomain covers B1's account-matching
// rule: an existing Account whose Website names the same domain as the
// lead's email is reused, not duplicated.
func TestConvertLeadReusesExistingAccountByDomain(t *testing.T) {
	s := NewMemStore()
	existing, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Corp", OwnerID: "owner-a", Website: "https://acme.com"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}

	lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "new-contact@acme.com")
	result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if result.Account.ID != existing.ID {
		t.Fatalf("Account.ID = %q, want reused existing account %q", result.Account.ID, existing.ID)
	}
	accts, _, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{}, SortAccountName)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accts) != 1 {
		t.Fatalf("tenant has %d accounts after conversion, want exactly 1 (no duplicate)", len(accts))
	}
}

// TestConvertLeadReusesExistingContactByEmailWithoutReassigningItsAccount
// covers the dedup-flagged case end to end: converting a lead whose email
// already belongs to a Contact reuses that Contact (does not create a
// second one) and does NOT reassign it to a different, domain-matched
// Account — the existing Contact's Account link is left exactly as it was.
func TestConvertLeadReusesExistingContactByEmailWithoutReassigningItsAccount(t *testing.T) {
	s := NewMemStore()
	// The contact already belongs to an account with a *different* domain
	// than the one conversion's domain-match would otherwise pick.
	originalAcct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Original Co", OwnerID: "owner-a", Website: "original.example"})
	if err != nil {
		t.Fatalf("seed original account: %v", err)
	}
	existingContact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "alice@acme.com", AccountID: originalAcct.ID})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	// A domain-matching account also exists, but must NOT be used since the
	// contact is reused as-is.
	if _, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme Corp", OwnerID: "owner-a", Website: "acme.com"}); err != nil {
		t.Fatalf("seed domain-matching account: %v", err)
	}

	lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", ContactEmail: "alice@acme.com"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	if lead.DuplicateOfContactID != existingContact.ID {
		t.Fatalf("lead was not flagged as a duplicate of %q", existingContact.ID)
	}
	lead.Status = LeadStatusWorking
	lead, err = s.UpdateLead(ctx, tenantA, lead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	lead.Status = LeadStatusQualified
	lead, err = s.UpdateLead(ctx, tenantA, lead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}

	result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if result.Contact.ID != existingContact.ID {
		t.Fatalf("Contact.ID = %q, want reused existing contact %q", result.Contact.ID, existingContact.ID)
	}
	if result.Account.ID != originalAcct.ID {
		t.Fatalf("Account.ID = %q, want the contact's original account %q (must not reassign)", result.Account.ID, originalAcct.ID)
	}

	contacts, _, err := s.ListContacts(ctx, tenantA, Page{}, ContactFilter{}, SortContactCreatedDesc)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("tenant has %d contacts after conversion, want exactly 1 (no duplicate)", len(contacts))
	}
	accts, _, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{}, SortAccountName)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accts) != 2 {
		t.Fatalf("tenant has %d accounts after conversion, want exactly 2 (no new account created)", len(accts))
	}
}

// TestConvertLeadWithOpportunity covers B1 item 3's optional Opportunity:
// requested, one is created against the resolved Account; not requested,
// none is.
func TestConvertLeadWithOpportunity(t *testing.T) {
	t.Run("create_opportunity=true creates one", func(t *testing.T) {
		s := NewMemStore()
		lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "alice@acme.com")
		result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{
			LeadID: lead.ID, CreateOpportunity: true, OpportunityAmount: 5000,
		})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		if result.Opportunity == nil {
			t.Fatal("Opportunity is nil, want one created")
		}
		if result.Opportunity.AccountID != result.Account.ID {
			t.Fatalf("Opportunity.AccountID = %q, want %q", result.Opportunity.AccountID, result.Account.ID)
		}
		if result.Opportunity.Amount != 5000 {
			t.Fatalf("Opportunity.Amount = %v, want 5000", result.Opportunity.Amount)
		}
		if result.Opportunity.Stage != StageProspecting {
			t.Fatalf("Opportunity.Stage = %q, want %q", result.Opportunity.Stage, StageProspecting)
		}
		if result.Lead.ConvertedOpportunityID != result.Opportunity.ID {
			t.Fatalf("Lead.ConvertedOpportunityID = %q, want %q", result.Lead.ConvertedOpportunityID, result.Opportunity.ID)
		}
	})

	t.Run("create_opportunity=false creates none", func(t *testing.T) {
		s := NewMemStore()
		lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "alice@acme.com")
		result, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		if result.Opportunity != nil {
			t.Fatalf("Opportunity = %+v, want nil", result.Opportunity)
		}
		opps, _, err := s.ListOpportunities(ctx, tenantA, Page{})
		if err != nil {
			t.Fatalf("list opportunities: %v", err)
		}
		if len(opps) != 0 {
			t.Fatalf("tenant has %d opportunities, want 0", len(opps))
		}
	})
}

// TestConvertLeadIsIdempotent covers B1's idempotency requirement: retrying
// conversion on the same lead id must not create duplicate
// Account/Contact/Opportunity records and must return the same linked ids
// both times.
func TestConvertLeadIsIdempotent(t *testing.T) {
	s := NewMemStore()
	lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "alice@acme.com")

	first, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID, CreateOpportunity: true})
	if err != nil {
		t.Fatalf("first convert: %v", err)
	}

	second, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID, CreateOpportunity: true})
	if err != nil {
		t.Fatalf("second convert (retry): %v", err)
	}

	if second.Contact.ID != first.Contact.ID {
		t.Fatalf("retry Contact.ID = %q, want %q", second.Contact.ID, first.Contact.ID)
	}
	if second.Account.ID != first.Account.ID {
		t.Fatalf("retry Account.ID = %q, want %q", second.Account.ID, first.Account.ID)
	}
	if second.Opportunity == nil || first.Opportunity == nil || second.Opportunity.ID != first.Opportunity.ID {
		t.Fatalf("retry Opportunity = %+v, want same id as first %+v", second.Opportunity, first.Opportunity)
	}

	accts, _, err := s.ListAccounts(ctx, tenantA, Page{}, AccountFilter{}, SortAccountName)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accts) != 1 {
		t.Fatalf("tenant has %d accounts after retry, want exactly 1", len(accts))
	}
	contacts, _, err := s.ListContacts(ctx, tenantA, Page{}, ContactFilter{}, SortContactCreatedDesc)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("tenant has %d contacts after retry, want exactly 1", len(contacts))
	}
	opps, _, err := s.ListOpportunities(ctx, tenantA, Page{})
	if err != nil {
		t.Fatalf("list opportunities: %v", err)
	}
	if len(opps) != 1 {
		t.Fatalf("tenant has %d opportunities after retry, want exactly 1", len(opps))
	}

	// A retry that changes its mind about create_opportunity still returns
	// the first call's outcome — it is a no-op, not a fresh decision.
	third, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID, CreateOpportunity: false})
	if err != nil {
		t.Fatalf("third convert (retry, different input): %v", err)
	}
	if third.Opportunity == nil || third.Opportunity.ID != first.Opportunity.ID {
		t.Fatalf("retry with create_opportunity=false still must return the original opportunity; got %+v", third.Opportunity)
	}
}

// TestConvertLeadTenantIsolation: a lead created under tenant A cannot be
// converted by naming its id under tenant B's scope.
func TestConvertLeadTenantIsolation(t *testing.T) {
	s := NewMemStore()
	lead := mustProgressLeadToQualified(t, s, tenantA, "Acme", "alice@acme.com")

	if _, err := s.ConvertLead(ctx, tenantB, ConvertLeadInput{LeadID: lead.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B converted tenant A's lead: got %v, want ErrNotFound", err)
	}

	if _, err := s.ConvertLead(ctx, noScope, ConvertLeadInput{LeadID: lead.ID}); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("convert with no tenant: got %v, want ErrNoTenant", err)
	}
}
