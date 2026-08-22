package domain

import (
	"context"
	"errors"
	"fmt"
)

// Activity polymorphic referential integrity, chronological timeline query,
// and immutability (B5 — docs/customer-os/PLAN.md).
//
// requireActivitySubject follows requireAccount's idiom (transitions.go) but
// dispatches on subject_type: an Activity attaches to exactly one of
// Account, Contact, Lead or Opportunity, and that record must exist in this
// tenant. An unrecognized subject_type is ErrInvalid the same way a
// dangling/cross-tenant subject_id is — both are "this Activity does not
// name a real subject".
//
// checkActivityImmutable is B5's other rule: once logged, an Activity's
// content cannot change. A correction is a new Activity, the same argument
// account_hierarchy.go and transitions.go make for a reopened Account
// hierarchy edge or a reopened Opportunity — see UpdateActivity in
// memstore.go/pgstore.go. Update remains callable (the RPC surface is not
// removed), but it only ever succeeds as a true no-op: repeating the
// record's current values back is accepted (retries are idempotent), and any
// actual change to kind, subject_type, subject_id, notes, actor_id or
// occurred_at is ErrInvalidTransition.

// Activity.SubjectType values.
const (
	ActivitySubjectAccount     = "account"
	ActivitySubjectContact     = "contact"
	ActivitySubjectLead        = "lead"
	ActivitySubjectOpportunity = "opportunity"
)

// activitySubjectReader is what requireActivitySubject needs from a store:
// one tenant-scoped Get per subject type it can dispatch to. Both MemStore
// and PgStore already implement all four.
type activitySubjectReader interface {
	GetAccount(ctx context.Context, scope Scope, id string) (*Account, error)
	GetContact(ctx context.Context, scope Scope, id string) (*Contact, error)
	GetLead(ctx context.Context, scope Scope, id string) (*Lead, error)
	GetOpportunity(ctx context.Context, scope Scope, id string) (*Opportunity, error)
}

// requireActivitySubject fails when subjectType is not one of the four known
// values, or when no record of that type names subjectID in this tenant. The
// lookup is always through the caller's own scope, so a real record
// belonging to another tenant is indistinguishable from one that does not
// exist at all — never a cross-tenant read.
func requireActivitySubject(ctx context.Context, r activitySubjectReader, scope Scope, subjectType, subjectID string) error {
	var err error
	switch subjectType {
	case ActivitySubjectAccount:
		_, err = r.GetAccount(ctx, scope, subjectID)
	case ActivitySubjectContact:
		_, err = r.GetContact(ctx, scope, subjectID)
	case ActivitySubjectLead:
		_, err = r.GetLead(ctx, scope, subjectID)
	case ActivitySubjectOpportunity:
		_, err = r.GetOpportunity(ctx, scope, subjectID)
	default:
		return fmt.Errorf("%w: Activity.subject_type %q is not a known subject type", ErrInvalid, subjectType)
	}
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: Activity.subject_id %q has no %s in this tenant", ErrInvalid, subjectID, subjectType)
	}
	return err
}

// checkActivityImmutable fails when v differs from existing in any core
// content field. A caller that resubmits the record unchanged (retrying an
// Update, or a client that always sends the full object back) is a no-op,
// not an error.
func checkActivityImmutable(existing, v *Activity) error {
	if existing.Kind == v.Kind &&
		existing.SubjectType == v.SubjectType &&
		existing.SubjectID == v.SubjectID &&
		existing.Notes == v.Notes &&
		existing.ActorID == v.ActorID &&
		existing.OccurredAt.Equal(v.OccurredAt) {
		return nil
	}
	return fmt.Errorf(
		"%w: Activity %q is immutable once logged; record a correction as a new Activity instead of editing this one",
		ErrInvalidTransition, existing.ID)
}
