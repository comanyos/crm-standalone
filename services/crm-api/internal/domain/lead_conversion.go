package domain

import (
	"fmt"
	"strings"
)

// ConvertLead — B1's load-bearing operation (docs/customer-os/PLAN.md).
//
// Converting a lead creates, in one transaction:
//   - a Contact from the lead's email and/or phone (reusing an exact match
//     instead of creating a duplicate — the same keys applyLeadDuplicateFlag
//     checks);
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
// duplicate them on retry, because the retry's identity lookups find
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

// requireLeadContactIdentity validates the address ConvertLead will put on
// the Contact. Person-first leads may be email-only or phone-only.
func requireLeadContactIdentity(lead *Lead) (email, phone string, err error) {
	if lead == nil {
		return "", "", fmt.Errorf("%w: no Lead", ErrInvalid)
	}
	email = strings.ToLower(strings.TrimSpace(lead.ContactEmail))
	phone = strings.TrimSpace(lead.Phone)
	if email == "" && phone == "" {
		return "", "", fmt.Errorf("%w: Lead.contact_email or Lead.phone is required to convert", ErrInvalid)
	}
	if phone != "" {
		phone, err = NormalizeE164Phone(phone)
		if err != nil {
			return "", "", fmt.Errorf("%w: Lead.phone %v", ErrInvalid, err)
		}
	}
	return email, phone, nil
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
func newConversionAccount(company, domain, fallback string) *Account {
	name := strings.TrimSpace(company)
	if name == "" {
		name = strings.TrimSpace(fallback)
	}
	if name == "" {
		name = domain
	}
	if name == "" {
		name = "Converted lead"
	}
	return &Account{Name: name, Website: domain}
}

// leadDisplayName is the human label used when a person-first lead has no
// company. It never invents data; the final generic fallback is only for the
// Account record ConvertLead must attach the Contact to.
func leadDisplayName(lead *Lead) string {
	if lead == nil {
		return ""
	}
	if name := strings.TrimSpace(strings.TrimSpace(lead.FirstName) + " " + strings.TrimSpace(lead.LastName)); name != "" {
		return name
	}
	if email := strings.TrimSpace(lead.ContactEmail); email != "" {
		return email
	}
	return strings.TrimSpace(lead.Phone)
}
