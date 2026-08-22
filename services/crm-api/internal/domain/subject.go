package domain

import (
	"context"
	"errors"
	"fmt"
)

// The polymorphic subject edge, shared by Activity, Task and Note.
//
// All three attach to exactly one CRM record of one of four types, and all
// three need the identical guarantee: the record must exist *in the caller's
// own tenant*. Keeping one implementation is what stops the three from
// drifting — a second copy that forgot to look the record up through the
// caller's scope would be a cross-tenant read, and it would look correct.

// Subject type values.
const (
	SubjectAccount     = "account"
	SubjectContact     = "contact"
	SubjectLead        = "lead"
	SubjectOpportunity = "opportunity"
)

// knownSubjectTypes is the allowlist. A subject type outside it is rejected
// rather than stored, because a row naming a type nothing can resolve is
// indistinguishable from corruption later.
var knownSubjectTypes = map[string]bool{
	SubjectAccount:     true,
	SubjectContact:     true,
	SubjectLead:        true,
	SubjectOpportunity: true,
}

// subjectReader is what requireSubject needs from a store: one tenant-scoped
// Get per subject type. Both MemStore and PgStore already implement all four.
type subjectReader interface {
	GetAccount(ctx context.Context, scope Scope, id string) (*Account, error)
	GetContact(ctx context.Context, scope Scope, id string) (*Contact, error)
	GetLead(ctx context.Context, scope Scope, id string) (*Lead, error)
	GetOpportunity(ctx context.Context, scope Scope, id string) (*Opportunity, error)
}

// requireSubject fails when subjectType is not one of the four known values,
// or when no record of that type names subjectID in this tenant.
//
// The lookup always goes through the caller's own scope, so a real record
// belonging to another tenant is indistinguishable from one that does not
// exist at all. That is the property that keeps this from becoming an
// existence oracle across the tenant boundary — see the isolation tests in
// pgstore_tenant_isolation_test.go.
//
// entity names the record being attached, so the error tells the caller what
// they were doing rather than describing the machinery.
func requireSubject(ctx context.Context, r subjectReader, scope Scope, entity, subjectType, subjectID string) error {
	var err error
	switch subjectType {
	case SubjectAccount:
		_, err = r.GetAccount(ctx, scope, subjectID)
	case SubjectContact:
		_, err = r.GetContact(ctx, scope, subjectID)
	case SubjectLead:
		_, err = r.GetLead(ctx, scope, subjectID)
	case SubjectOpportunity:
		_, err = r.GetOpportunity(ctx, scope, subjectID)
	default:
		return fmt.Errorf("%w: %s.subject_type %q is not a known subject type",
			ErrInvalid, entity, subjectType)
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %s.subject_id %q has no %s in this tenant",
			ErrInvalid, entity, subjectID, subjectType)
	}
	return err
}
