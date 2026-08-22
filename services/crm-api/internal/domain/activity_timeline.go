package domain

import (
	"context"
	"fmt"
	"reflect"
)

// Activity chronological timeline query and immutability (B5 —
// docs/customer-os/PLAN.md).
//
// The polymorphic subject edge itself moved to subject.go when Task and Note
// arrived: all three attach to exactly one Account, Contact, Lead or
// Opportunity, and all three need the identical guarantee that the record
// exists in the caller's own tenant. One implementation is what stops a
// second copy from quietly omitting the scope and becoming a cross-tenant
// read.
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

// Activity.SubjectType values. Aliases of the shared subject vocabulary in
// subject.go, kept because callers and tests across the package already name
// them this way and the indirection costs nothing.
const (
	ActivitySubjectAccount     = SubjectAccount
	ActivitySubjectContact     = SubjectContact
	ActivitySubjectLead        = SubjectLead
	ActivitySubjectOpportunity = SubjectOpportunity
)

// requireActivitySubject requires an Activity's subject to resolve inside the
// caller's tenant. Activity's subject is mandatory, unlike Task's — an
// activity that happened to nothing in particular is not a thing this domain
// records. See subject.go for the shared implementation and why there is only
// one of it.
func requireActivitySubject(ctx context.Context, r subjectReader, scope Scope, subjectType, subjectID string) error {
	return requireSubject(ctx, r, scope, "Activity", subjectType, subjectID)
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
		existing.OccurredAt.Equal(v.OccurredAt) &&
		reflect.DeepEqual(existing.CustomFields, v.CustomFields) {
		return nil
	}
	return fmt.Errorf(
		"%w: Activity %q is immutable once logged; record a correction as a new Activity instead of editing this one",
		ErrInvalidTransition, existing.ID)
}
