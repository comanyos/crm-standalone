package domain

import (
	"errors"
	"testing"
	"time"
)

// --- Polymorphic referential integrity -------------------------------------

// seedActivitySubjects creates one Account, Contact, Lead and Opportunity
// under scope and returns their ids keyed by the ActivitySubject* constant
// that names them.
func seedActivitySubjects(t *testing.T, s *MemStore, scope Scope) map[string]string {
	t.Helper()
	acct, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	contact, err := s.CreateContact(ctx, scope, &Contact{Email: "alice@acme.com", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	lead, err := s.CreateLead(ctx, scope, &Lead{Company: "Acme"})
	if err != nil {
		t.Fatalf("seed lead: %v", err)
	}
	opp, err := s.CreateOpportunity(ctx, scope, &Opportunity{Name: "Big Deal", AccountID: acct.ID})
	if err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	return map[string]string{
		ActivitySubjectAccount:     acct.ID,
		ActivitySubjectContact:     contact.ID,
		ActivitySubjectLead:        lead.ID,
		ActivitySubjectOpportunity: opp.ID,
	}
}

// TestActivitySubjectAcceptsValidReference covers every known subject_type:
// a subject_id naming a real, tenant-scoped record of that type is accepted.
func TestActivitySubjectAcceptsValidReference(t *testing.T) {
	s := NewMemStore()
	ids := seedActivitySubjects(t, s, tenantA)

	for _, subjectType := range []string{
		ActivitySubjectAccount, ActivitySubjectContact, ActivitySubjectLead, ActivitySubjectOpportunity,
	} {
		t.Run(subjectType, func(t *testing.T) {
			act, err := s.CreateActivity(ctx, tenantA, &Activity{
				Kind: "note", SubjectType: subjectType, SubjectID: ids[subjectType],
			})
			if err != nil {
				t.Fatalf("create with valid %s subject: %v", subjectType, err)
			}
			if act.SubjectID != ids[subjectType] {
				t.Fatalf("SubjectID = %q, want %q", act.SubjectID, ids[subjectType])
			}
		})
	}
}

// TestActivitySubjectRejectsDanglingReference covers every known
// subject_type: a subject_id naming no record of that type in this tenant is
// ErrInvalid.
func TestActivitySubjectRejectsDanglingReference(t *testing.T) {
	s := NewMemStore()

	for _, subjectType := range []string{
		ActivitySubjectAccount, ActivitySubjectContact, ActivitySubjectLead, ActivitySubjectOpportunity,
	} {
		t.Run(subjectType, func(t *testing.T) {
			_, err := s.CreateActivity(ctx, tenantA, &Activity{
				Kind: "note", SubjectType: subjectType, SubjectID: "does-not-exist",
			})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("create with dangling %s subject: got %v, want ErrInvalid", subjectType, err)
			}
		})
	}
}

// TestActivitySubjectRejectsCrossTenantReference covers every known
// subject_type: naming another tenant's real record as subject_id is
// indistinguishable, from this tenant's point of view, from naming an id
// that does not exist at all (.claude/rules/tenancy-and-scopes.md).
func TestActivitySubjectRejectsCrossTenantReference(t *testing.T) {
	s := NewMemStore()
	idsB := seedActivitySubjects(t, s, tenantB)

	for _, subjectType := range []string{
		ActivitySubjectAccount, ActivitySubjectContact, ActivitySubjectLead, ActivitySubjectOpportunity,
	} {
		t.Run(subjectType, func(t *testing.T) {
			_, err := s.CreateActivity(ctx, tenantA, &Activity{
				Kind: "note", SubjectType: subjectType, SubjectID: idsB[subjectType],
			})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("create with cross-tenant %s subject: got %v, want ErrInvalid", subjectType, err)
			}
		})
	}
}

// TestActivitySubjectRejectsUnknownType covers that an unrecognized
// subject_type is ErrInvalid even when subject_id happens to name a real
// record — the dispatch itself must reject it, not just the id lookup.
func TestActivitySubjectRejectsUnknownType(t *testing.T) {
	s := NewMemStore()
	ids := seedActivitySubjects(t, s, tenantA)

	_, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: "campaign", SubjectID: ids[ActivitySubjectAccount],
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with unknown subject_type: got %v, want ErrInvalid", err)
	}
}

// --- Chronological timeline query -------------------------------------------

// TestListActivitiesForSubjectOrdersNewestFirst covers B5's ordering
// guarantee: multiple activities for one subject come back newest first by
// occurred_at, regardless of creation order.
func TestListActivitiesForSubjectOrdersNewestFirst(t *testing.T) {
	s := NewMemStore()
	ids := seedActivitySubjects(t, s, tenantA)
	acctID := ids[ActivitySubjectAccount]

	base := time.Now().UTC().Truncate(time.Second)
	oldest, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "call", SubjectType: ActivitySubjectAccount, SubjectID: acctID, OccurredAt: base,
	})
	if err != nil {
		t.Fatalf("seed oldest: %v", err)
	}
	middle, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "email", SubjectType: ActivitySubjectAccount, SubjectID: acctID, OccurredAt: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("seed middle: %v", err)
	}
	newest, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "meeting", SubjectType: ActivitySubjectAccount, SubjectID: acctID, OccurredAt: base.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed newest: %v", err)
	}

	got, err := s.ListActivitiesForSubject(ctx, tenantA, ActivitySubjectAccount, acctID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d activities, want 3", len(got))
	}
	wantOrder := []string{newest.ID, middle.ID, oldest.ID}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Fatalf("position %d = %q, want %q (order: %v)", i, got[i].ID, w, ids)
		}
	}
}

// TestListActivitiesForSubjectOnlyReturnsThatSubject covers that the query
// filters correctly: an activity attached to a different subject (even of
// the same type) never leaks into another subject's timeline.
func TestListActivitiesForSubjectOnlyReturnsThatSubject(t *testing.T) {
	s := NewMemStore()
	acct1, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-1"})
	if err != nil {
		t.Fatalf("seed account 1: %v", err)
	}
	acct2, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Globex", OwnerID: "owner-2"})
	if err != nil {
		t.Fatalf("seed account 2: %v", err)
	}
	if _, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: ActivitySubjectAccount, SubjectID: acct1.ID,
	}); err != nil {
		t.Fatalf("seed activity 1: %v", err)
	}
	if _, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: ActivitySubjectAccount, SubjectID: acct2.ID,
	}); err != nil {
		t.Fatalf("seed activity 2: %v", err)
	}

	got, err := s.ListActivitiesForSubject(ctx, tenantA, ActivitySubjectAccount, acct1.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d activities, want 1", len(got))
	}
	if got[0].SubjectID != acct1.ID {
		t.Fatalf("SubjectID = %q, want %q", got[0].SubjectID, acct1.ID)
	}
}

// TestListActivitiesForSubjectIsTenantScoped covers the mandatory tenancy
// rule (.claude/rules/tenancy-and-scopes.md): a subject in tenant A must
// never surface tenant B's activities, even asking under the same subject
// id (which cannot collide across tenants here, since ids are generated
// per-tenant, but the query itself must still be scoped defensively).
func TestListActivitiesForSubjectIsTenantScoped(t *testing.T) {
	s := NewMemStore()
	idsA := seedActivitySubjects(t, s, tenantA)
	acctA := idsA[ActivitySubjectAccount]

	if _, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: ActivitySubjectAccount, SubjectID: acctA,
	}); err != nil {
		t.Fatalf("seed tenant A activity: %v", err)
	}

	got, err := s.ListActivitiesForSubject(ctx, tenantB, ActivitySubjectAccount, acctA)
	if err != nil {
		t.Fatalf("list under tenant B: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("tenant B saw %d of tenant A's activities, want 0", len(got))
	}
}

// TestListActivitiesForSubjectRejectsEmptyScope covers the store-method
// non-negotiable: an empty tenant is always ErrNoTenant, never "all
// tenants".
func TestListActivitiesForSubjectRejectsEmptyScope(t *testing.T) {
	s := NewMemStore()
	_, err := s.ListActivitiesForSubject(ctx, noScope, ActivitySubjectAccount, "any")
	if !errors.Is(err, ErrNoTenant) {
		t.Fatalf("list with no tenant: got %v, want ErrNoTenant", err)
	}
}

// --- Immutability ------------------------------------------------------------

// TestActivityUpdateRejectsContentChange covers B5's immutability rule: once
// logged, an Activity's core content fields cannot change through Update.
// Each subtest changes exactly one field and expects ErrInvalidTransition.
func TestActivityUpdateRejectsContentChange(t *testing.T) {
	s := NewMemStore()
	ids := seedActivitySubjects(t, s, tenantA)
	occurredAt := time.Now().UTC().Truncate(time.Second)

	seed := func(t *testing.T) *Activity {
		t.Helper()
		act, err := s.CreateActivity(ctx, tenantA, &Activity{
			Kind: "call", SubjectType: ActivitySubjectAccount, SubjectID: ids[ActivitySubjectAccount],
			Notes: "original", ActorID: "user-1", OccurredAt: occurredAt,
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		return act
	}

	t.Run("kind", func(t *testing.T) {
		act := seed(t)
		act.Kind = "email"
		if _, err := s.UpdateActivity(ctx, tenantA, act); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("update kind: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("subject", func(t *testing.T) {
		act := seed(t)
		act.SubjectType = ActivitySubjectLead
		act.SubjectID = ids[ActivitySubjectLead]
		if _, err := s.UpdateActivity(ctx, tenantA, act); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("update subject: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("notes", func(t *testing.T) {
		act := seed(t)
		act.Notes = "corrected"
		if _, err := s.UpdateActivity(ctx, tenantA, act); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("update notes: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("actor_id", func(t *testing.T) {
		act := seed(t)
		act.ActorID = "user-2"
		if _, err := s.UpdateActivity(ctx, tenantA, act); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("update actor_id: got %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("occurred_at", func(t *testing.T) {
		act := seed(t)
		act.OccurredAt = occurredAt.Add(time.Hour)
		if _, err := s.UpdateActivity(ctx, tenantA, act); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("update occurred_at: got %v, want ErrInvalidTransition", err)
		}
	})
}

// TestActivityUpdateAllowsTrueNoOp covers the other half of B5's
// immutability rule: resubmitting a record's current values unchanged is
// accepted, not rejected — a retry of an already-applied Update (or a client
// that always sends the full object back) must be idempotent.
func TestActivityUpdateAllowsTrueNoOp(t *testing.T) {
	s := NewMemStore()
	ids := seedActivitySubjects(t, s, tenantA)
	occurredAt := time.Now().UTC().Truncate(time.Second)

	act, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "call", SubjectType: ActivitySubjectAccount, SubjectID: ids[ActivitySubjectAccount],
		Notes: "original", ActorID: "user-1", OccurredAt: occurredAt,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	updated, err := s.UpdateActivity(ctx, tenantA, act)
	if err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if updated.Notes != "original" || updated.Kind != "call" {
		t.Fatalf("no-op update changed content: %+v", updated)
	}
}

// TestActivityUpdateUnknownIDIsNotFound covers that Update on an id that was
// never created is ErrNotFound, not ErrInvalidTransition — the immutability
// check only applies once there is an existing record to compare against.
func TestActivityUpdateUnknownIDIsNotFound(t *testing.T) {
	s := NewMemStore()
	_, err := s.UpdateActivity(ctx, tenantA, &Activity{ID: "missing", Kind: "call"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown id: got %v, want ErrNotFound", err)
	}
}
