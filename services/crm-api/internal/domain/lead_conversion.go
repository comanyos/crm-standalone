package domain

import (
	"fmt"
	"strings"
)

// ConvertLead — B1's load-bearing operation (docs/customer-os/PLAN.md).
//
// Converting a lead creates, in one transaction:
//   - a Contact from the lead's email (reusing an exact-email match instead
//     of creating a duplicate — the same key applyLeadDuplicateFlag checks);
//   - an Account it is attached to (an existing match by email domain, or a
//     new one — only when a new Contact is being created; a reused Contact
//     keeps whatever Account it already had, conversion never reassigns an
//     existing contact's account);
//   - optionally, an Opportunity against that Account.
//
// It then marks the Lead converted and links the created/reused ids.
//
// Idempotency: calling ConvertLead again with the same lead id is a no-op.
// The primary guard is Lead.Status itself — once it is converted, both
// MemStore.ConvertLead and PgStore.ConvertLead short-circuit and return the
// ids already on the record without creating anything. The Contact-reuse
// and Account-reuse lookups are a second line of defense (a crash between
// creating the Contact/Account and marking the lead converted still will not
// duplicate them on retry, because the retry's email/domain lookups find
// what the first attempt already created) but the Status check is what makes
// a stray extra Opportunity impossible, since Opportunity has no natural key
// to dedup against.

// ConvertLeadInput is what the caller of ConvertLead controls.
type ConvertLeadInput struct {
	LeadID string

	// CreateOpportunity is a flag, not a default: an Opportunity is only
	// created when this is true.
	CreateOpportunity bool
	// OpportunityName defaults to "<lead.company> opportunity" when blank.
	OpportunityName   string
	OpportunityAmount float64
}

// ConvertLeadResult is everything ConvertLead touched or reused.
// Opportunity is nil when the input did not ask for one.
type ConvertLeadResult struct {
	Lead        *Lead
	Contact     *Contact
	Account     *Account
	Opportunity *Opportunity
}

// requireLeadContactEmail validates the one field ConvertLead cannot proceed
// without.
func requireLeadContactEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", fmt.Errorf("%w: Lead.contact_email is required to convert", ErrInvalid)
	}
	return email, nil
}

// conversionOpportunityName applies ConvertLeadInput's documented default.
func conversionOpportunityName(name, company string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	company = strings.TrimSpace(company)
	if company == "" {
		return "converted opportunity"
	}
	return company + " opportunity"
}

// newConversionAccount builds the Account ConvertLead creates when no
// existing one matches by domain. Name falls back to the domain itself when
// the lead carries no company name.
func newConversionAccount(company, domain string) *Account {
	name := strings.TrimSpace(company)
	if name == "" {
		name = domain
	}
	return &Account{Name: name, Website: domain}
}
