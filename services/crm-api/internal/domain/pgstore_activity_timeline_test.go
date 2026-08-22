package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestPgStoreActivityTimeline exercises B5's rules against real Postgres:
// polymorphic subject referential integrity (dangling, cross-tenant,
// unrecognized subject_type), the chronological timeline query, and
// immutability on Update — the same behaviors activity_timeline_test.go
// proves against MemStore, run here through PgStore/scopedDB so the SQL
// paths are not just compiled, but verified.
func TestPgStoreActivityTimeline(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-crm-activity-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-crm-activity-b-%d", stamp), OrgID: "org-b"}

	acctA, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Acme", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create tenant A account: %v", err)
	}
	acctB, err := s.CreateAccount(ctx, tenantB, &Account{Name: "Other Tenant Co", OwnerID: "owner-b"})
	if err != nil {
		t.Fatalf("create tenant B account: %v", err)
	}

	// Referential integrity: dangling, cross-tenant, unrecognized type.
	if _, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: ActivitySubjectAccount, SubjectID: "does-not-exist",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with dangling subject: got %v, want ErrInvalid", err)
	}
	if _, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: ActivitySubjectAccount, SubjectID: acctB.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with cross-tenant subject: got %v, want ErrInvalid", err)
	}
	if _, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "note", SubjectType: "campaign", SubjectID: acctA.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create with unknown subject_type: got %v, want ErrInvalid", err)
	}

	// Chronological timeline: newest first by occurred_at.
	base := time.Now().UTC().Truncate(time.Second)
	oldest, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "call", SubjectType: ActivitySubjectAccount, SubjectID: acctA.ID, OccurredAt: base,
	})
	if err != nil {
		t.Fatalf("create oldest activity: %v", err)
	}
	newest, err := s.CreateActivity(ctx, tenantA, &Activity{
		Kind: "meeting", SubjectType: ActivitySubjectAccount, SubjectID: acctA.ID, OccurredAt: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create newest activity: %v", err)
	}

	timeline, err := s.ListActivitiesForSubject(ctx, tenantA, ActivitySubjectAccount, acctA.ID)
	if err != nil {
		t.Fatalf("list activities for subject: %v", err)
	}
	if len(timeline) != 2 {
		t.Fatalf("got %d activities, want 2", len(timeline))
	}
	if timeline[0].ID != newest.ID || timeline[1].ID != oldest.ID {
		t.Fatalf("timeline order = [%s, %s], want [%s, %s]",
			timeline[0].ID, timeline[1].ID, newest.ID, oldest.ID)
	}

	// Tenant isolation on the timeline query.
	tenantBTimeline, err := s.ListActivitiesForSubject(ctx, tenantB, ActivitySubjectAccount, acctA.ID)
	if err != nil {
		t.Fatalf("list under tenant B: %v", err)
	}
	if len(tenantBTimeline) != 0 {
		t.Fatalf("tenant B saw %d of tenant A's activities, want 0", len(tenantBTimeline))
	}

	// Immutability: a real content change is rejected; resubmitting the
	// same content is a no-op.
	oldest.Notes = "corrected"
	if _, err := s.UpdateActivity(ctx, tenantA, oldest); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("update changing notes: got %v, want ErrInvalidTransition", err)
	}
	oldest.Notes = ""
	if _, err := s.UpdateActivity(ctx, tenantA, oldest); err != nil {
		t.Fatalf("no-op update: %v", err)
	}

	// Tenant B cannot read tenant A's activity at all.
	if _, err := s.GetActivity(ctx, tenantB, oldest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B GetActivity err = %v; want ErrNotFound", err)
	}
}
