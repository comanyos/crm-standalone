package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestPgStoreLeadConversion exercises B1's lifecycle, dedup flag and
// ConvertLead against real Postgres — the same behaviors
// lead_lifecycle_test.go / lead_conversion_test.go prove against MemStore,
// run here through PgStore/scopedDB and the single-transaction ConvertLead
// so the SQL paths (including the tx-scoped helpers in
// pgstore_lead_conversion.go) are verified, not just compiled.
func TestPgStoreLeadConversion(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-crm-lead-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-crm-lead-b-%d", stamp), OrgID: "org-b"}

	// --- Status machine -----------------------------------------------
	lead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Acme", ContactEmail: "alice@acme.com"})
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	if lead.Status != LeadStatusNew {
		t.Fatalf("default Status = %q, want %q", lead.Status, LeadStatusNew)
	}

	skip := *lead
	skip.Status = LeadStatusQualified
	if _, err := s.UpdateLead(ctx, tenantA, &skip); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skip-ahead transition: got %v, want ErrInvalidTransition", err)
	}

	direct := *lead
	direct.Status = LeadStatusConverted
	if _, err := s.UpdateLead(ctx, tenantA, &direct); !errors.Is(err, ErrInvalid) {
		t.Fatalf("set converted directly via UpdateLead: got %v, want ErrInvalid", err)
	}

	// --- Exact-key dedup flagging --------------------------------------
	dupAcct, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Dup Co", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create dup account: %v", err)
	}
	dupContact, err := s.CreateContact(ctx, tenantA, &Contact{Email: "dup@example.com", AccountID: dupAcct.ID})
	if err != nil {
		t.Fatalf("create dup contact: %v", err)
	}
	dupLead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Dup Lead Co", ContactEmail: "Dup@Example.com"})
	if err != nil {
		t.Fatalf("create lead with duplicate email: %v", err)
	}
	if dupLead.DuplicateOfContactID != dupContact.ID {
		t.Fatalf("DuplicateOfContactID = %q, want %q", dupLead.DuplicateOfContactID, dupContact.ID)
	}

	// FindContactByEmail / FindAccountByDomain tenant isolation.
	if _, err := s.FindContactByEmail(ctx, tenantB, "dup@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B found tenant A's contact by email: got %v, want ErrNotFound", err)
	}
	if got, err := s.FindContactByEmail(ctx, tenantA, "dup@example.com"); err != nil || got.ID != dupContact.ID {
		t.Fatalf("tenant A find by email: got %+v, %v", got, err)
	}

	acctWithSite, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Domain Co", OwnerID: "owner-a", Website: "https://www.domainco.example/pricing"})
	if err != nil {
		t.Fatalf("create account with website: %v", err)
	}
	if _, err := s.FindAccountByDomain(ctx, tenantB, "domainco.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B found tenant A's account by domain: got %v, want ErrNotFound", err)
	}
	if got, err := s.FindAccountByDomain(ctx, tenantA, "domainco.example"); err != nil || got.ID != acctWithSite.ID {
		t.Fatalf("tenant A find by domain: got %+v, %v", got, err)
	}

	// --- ConvertLead: rejects an unqualified lead -----------------------
	if _, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: lead.ID}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("convert from new: got %v, want ErrInvalidTransition", err)
	}

	// Advance to qualified the only legal way.
	lead.Status = LeadStatusWorking
	lead, err = s.UpdateLead(ctx, tenantA, lead)
	if err != nil {
		t.Fatalf("advance to working: %v", err)
	}
	lead.Status = LeadStatusQualified
	lead, err = s.UpdateLead(ctx, tenantA, lead)
	if err != nil {
		t.Fatalf("advance to qualified: %v", err)
	}

	// --- ConvertLead: happy path, with an Opportunity -------------------
	first, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{
		LeadID: lead.ID, CreateOpportunity: true, OpportunityAmount: 999,
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if first.Lead.Status != LeadStatusConverted {
		t.Fatalf("Lead.Status = %q, want %q", first.Lead.Status, LeadStatusConverted)
	}
	if first.Contact == nil || first.Contact.Email != "alice@acme.com" {
		t.Fatalf("Contact = %+v", first.Contact)
	}
	if first.Account == nil || first.Contact.AccountID != first.Account.ID {
		t.Fatalf("Contact.AccountID = %q, Account.ID = %q — referential integrity broken",
			first.Contact.AccountID, first.Account.ID)
	}
	if first.Opportunity == nil || first.Opportunity.AccountID != first.Account.ID {
		t.Fatalf("Opportunity = %+v, want AccountID %q", first.Opportunity, first.Account.ID)
	}
	if first.Lead.ConvertedContactID != first.Contact.ID ||
		first.Lead.ConvertedAccountID != first.Account.ID ||
		first.Lead.ConvertedOpportunityID != first.Opportunity.ID {
		t.Fatalf("Lead linked ids = %+v, want to match %+v/%+v/%+v",
			first.Lead, first.Contact.ID, first.Account.ID, first.Opportunity.ID)
	}

	// --- Idempotent retry: no duplicates, same ids ----------------------
	second, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{
		LeadID: lead.ID, CreateOpportunity: true, OpportunityAmount: 12345, // different input, must not matter
	})
	if err != nil {
		t.Fatalf("retry convert: %v", err)
	}
	if second.Contact.ID != first.Contact.ID || second.Account.ID != first.Account.ID ||
		second.Opportunity.ID != first.Opportunity.ID {
		t.Fatalf("retry returned different ids: %+v, want same as %+v", second, first)
	}

	contacts, _, err := s.ListContacts(ctx, tenantA, Page{}, ContactFilter{}, SortContactCreatedDesc)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	wantContacts := 2 // dupContact + the converted lead's contact
	if len(contacts) != wantContacts {
		t.Fatalf("tenant has %d contacts after retry, want %d (no duplicate created)", len(contacts), wantContacts)
	}
	opps, _, err := s.ListOpportunities(ctx, tenantA, Page{})
	if err != nil {
		t.Fatalf("list opportunities: %v", err)
	}
	if len(opps) != 1 {
		t.Fatalf("tenant has %d opportunities after retry, want 1 (no duplicate created)", len(opps))
	}

	// --- Converted is terminal: cannot move anywhere else ---------------
	moveOut := *first.Lead
	moveOut.Status = LeadStatusWorking
	if _, err := s.UpdateLead(ctx, tenantA, &moveOut); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("move converted lead backward: got %v, want ErrInvalidTransition", err)
	}

	// --- ConvertLead reuses an existing Account by domain match ---------
	domainLead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Domain Co", ContactEmail: "new@domainco.example"})
	if err != nil {
		t.Fatalf("create domain-match lead: %v", err)
	}
	domainLead.Status = LeadStatusWorking
	domainLead, err = s.UpdateLead(ctx, tenantA, domainLead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	domainLead.Status = LeadStatusQualified
	domainLead, err = s.UpdateLead(ctx, tenantA, domainLead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	domainResult, err := s.ConvertLead(ctx, tenantA, ConvertLeadInput{LeadID: domainLead.ID})
	if err != nil {
		t.Fatalf("convert domain-match lead: %v", err)
	}
	if domainResult.Account.ID != acctWithSite.ID {
		t.Fatalf("Account.ID = %q, want reused existing account %q", domainResult.Account.ID, acctWithSite.ID)
	}

	// --- ConvertLead is tenant-scoped -----------------------------------
	otherLead, err := s.CreateLead(ctx, tenantA, &Lead{Company: "Other", ContactEmail: "other@acme.com"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	otherLead.Status = LeadStatusWorking
	otherLead, err = s.UpdateLead(ctx, tenantA, otherLead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	otherLead.Status = LeadStatusQualified
	otherLead, err = s.UpdateLead(ctx, tenantA, otherLead)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, err := s.ConvertLead(ctx, tenantB, ConvertLeadInput{LeadID: otherLead.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B converted tenant A's lead: got %v, want ErrNotFound", err)
	}
}
