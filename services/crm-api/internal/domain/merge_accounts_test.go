package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Account merges have two Store implementations, and the same contract must
// hold for both — see revenue_store_parity_test.go's doc comment.
func eachMergeStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

func mergeScope(label string) Scope {
	return Scope{TenantID: fmt.Sprintf("tenant-merge-%s-%d", label, time.Now().UnixNano())}
}

func TestMergeAccountsInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   MergeAccountsInput
		want string
	}{
		{"minimal", MergeAccountsInput{SurvivorID: "a", DuplicateID: "b"}, ""},
		{"with resolutions", MergeAccountsInput{
			SurvivorID: "a", DuplicateID: "b",
			FieldResolutions: map[string]string{"name": ResolutionDuplicate, "tier": ResolutionSurvivor},
		}, ""},
		{"no survivor", MergeAccountsInput{DuplicateID: "b"}, "needs both"},
		{"self merge", MergeAccountsInput{SurvivorID: "a", DuplicateID: "a"}, "cannot be merged into itself"},
		{"unknown field", MergeAccountsInput{
			SurvivorID: "a", DuplicateID: "b", FieldResolutions: map[string]string{"id": ResolutionDuplicate},
		}, "not a field a merge can resolve"},
		{"bad resolution", MergeAccountsInput{
			SurvivorID: "a", DuplicateID: "b", FieldResolutions: map[string]string{"name": "attacker"},
		}, "not survivor or duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
		})
	}
}

// TestMergeAccountsIsTheSameInBothStores builds a survivor and a duplicate
// each with a full set of relationships — a contact, an opportunity, a
// child account, an activity, a task, a note, an ownership-history entry (via
// a real owner change), and two tags, one of which the survivor already
// carries — and proves the merge migrates every one of them, resolves
// fields per the caller's choice, deletes the duplicate, and writes an
// audit record both accounts can find.
func TestMergeAccountsIsTheSameInBothStores(t *testing.T) {
	eachMergeStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := mergeScope(name)

		survivor, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme Inc", Tier: "silver", OwnerID: "alice"})
		if err != nil {
			t.Fatalf("create survivor: %v", err)
		}
		duplicate, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme Incorporated", Tier: "gold", OwnerID: "bob", Industry: "Manufacturing"})
		if err != nil {
			t.Fatalf("create duplicate: %v", err)
		}
		// A real owner change on the duplicate, so it has an
		// account_ownership_history row to migrate.
		duplicate.OwnerID = "carol"
		duplicate, err = s.UpdateAccount(ctx, scope, duplicate)
		if err != nil {
			t.Fatalf("change duplicate owner: %v", err)
		}

		contact, err := s.CreateContact(ctx, scope, &Contact{Email: "jane@acme.example", AccountID: duplicate.ID})
		if err != nil {
			t.Fatalf("create contact: %v", err)
		}
		opp, err := s.CreateOpportunity(ctx, scope, &Opportunity{Name: "Acme Renewal", AccountID: duplicate.ID})
		if err != nil {
			t.Fatalf("create opportunity: %v", err)
		}
		child, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme Subsidiary", OwnerID: "alice", ParentID: duplicate.ID})
		if err != nil {
			t.Fatalf("create child account: %v", err)
		}
		activity, err := s.CreateActivity(ctx, scope, &Activity{Kind: "call", SubjectType: SubjectAccount, SubjectID: duplicate.ID})
		if err != nil {
			t.Fatalf("create activity: %v", err)
		}
		task, err := s.CreateTask(ctx, scope, &Task{Title: "Follow up", SubjectType: SubjectAccount, SubjectID: duplicate.ID})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		note, err := s.CreateNote(ctx, scope, &Note{Body: "Called about renewal", SubjectType: SubjectAccount, SubjectID: duplicate.ID})
		if err != nil {
			t.Fatalf("create note: %v", err)
		}

		sharedTag, err := s.CreateTagDefinition(ctx, scope, &TagDefinition{Name: "Enterprise"})
		if err != nil {
			t.Fatalf("create shared tag: %v", err)
		}
		onlyOnDuplicateTag, err := s.CreateTagDefinition(ctx, scope, &TagDefinition{Name: "Renewal risk"})
		if err != nil {
			t.Fatalf("create duplicate-only tag: %v", err)
		}
		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: sharedTag.ID, ObjectType: "account", ObjectID: survivor.ID}); err != nil {
			t.Fatalf("assign shared tag to survivor: %v", err)
		}
		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: sharedTag.ID, ObjectType: "account", ObjectID: duplicate.ID}); err != nil {
			t.Fatalf("assign shared tag to duplicate: %v", err)
		}
		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: onlyOnDuplicateTag.ID, ObjectType: "account", ObjectID: duplicate.ID}); err != nil {
			t.Fatalf("assign duplicate-only tag: %v", err)
		}

		mergedAccount, record, err := s.MergeAccounts(ctx, scope, MergeAccountsInput{
			SurvivorID: survivor.ID, DuplicateID: duplicate.ID,
			FieldResolutions: map[string]string{"tier": ResolutionDuplicate, "industry": ResolutionDuplicate},
			MergedBy:         "admin-1",
		})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}

		// Field resolution: tier and industry came from the duplicate; name
		// (not named in FieldResolutions) stayed the survivor's.
		if mergedAccount.Tier != "gold" {
			t.Errorf("tier = %q, want gold (from duplicate)", mergedAccount.Tier)
		}
		if mergedAccount.Industry != "Manufacturing" {
			t.Errorf("industry = %q, want Manufacturing (from duplicate)", mergedAccount.Industry)
		}
		if mergedAccount.Name != "Acme Inc" {
			t.Errorf("name = %q, want Acme Inc (unresolved field keeps survivor's)", mergedAccount.Name)
		}

		// The duplicate is gone.
		if _, err := s.GetAccount(ctx, scope, duplicate.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get duplicate after merge = %v, want ErrNotFound", err)
		}

		// Every relationship followed the duplicate to the survivor.
		gotContact, err := s.GetContact(ctx, scope, contact.ID)
		if err != nil || gotContact.AccountID != survivor.ID {
			t.Errorf("contact.account_id = %+v, %v, want %s", gotContact, err, survivor.ID)
		}
		gotOpp, err := s.GetOpportunity(ctx, scope, opp.ID)
		if err != nil || gotOpp.AccountID != survivor.ID {
			t.Errorf("opportunity.account_id = %+v, %v, want %s", gotOpp, err, survivor.ID)
		}
		gotChild, err := s.GetAccount(ctx, scope, child.ID)
		if err != nil || gotChild.ParentID != survivor.ID {
			t.Errorf("child.parent_id = %+v, %v, want %s", gotChild, err, survivor.ID)
		}
		gotActivity, err := s.GetActivity(ctx, scope, activity.ID)
		if err != nil || gotActivity.SubjectID != survivor.ID {
			t.Errorf("activity.subject_id = %+v, %v, want %s", gotActivity, err, survivor.ID)
		}
		gotTask, err := s.GetTask(ctx, scope, task.ID)
		if err != nil || gotTask.SubjectID != survivor.ID {
			t.Errorf("task.subject_id = %+v, %v, want %s", gotTask, err, survivor.ID)
		}
		gotNote, err := s.GetNote(ctx, scope, note.ID)
		if err != nil || gotNote.SubjectID != survivor.ID {
			t.Errorf("note.subject_id = %+v, %v, want %s", gotNote, err, survivor.ID)
		}

		history, err := s.ListOwnershipHistory(ctx, scope, survivor.ID)
		if err != nil {
			t.Fatalf("list ownership history: %v", err)
		}
		if len(history) != 1 || history[0].ToOwnerID != "carol" {
			t.Fatalf("survivor ownership history = %+v, want the duplicate's bob->carol change migrated", history)
		}

		// Tags: the shared tag was not duplicated, and the duplicate-only
		// tag followed the record.
		survivorTags, err := s.ListTagsForRecord(ctx, scope, "account", survivor.ID)
		if err != nil {
			t.Fatalf("list survivor tags: %v", err)
		}
		if len(survivorTags) != 2 {
			t.Fatalf("survivor tags = %d, want 2 (Enterprise once, Renewal risk once): %+v", len(survivorTags), survivorTags)
		}

		// The audit record is immutable and visible from both ids.
		if record.SurvivorAccountID != survivor.ID || record.DuplicateAccountID != duplicate.ID {
			t.Fatalf("merge record = %+v", record)
		}
		if record.MergedBy != "admin-1" {
			t.Errorf("merged_by = %q, want admin-1", record.MergedBy)
		}
		if record.RelationshipsMigrated["contacts"] != 1 || record.RelationshipsMigrated["opportunities"] != 1 ||
			record.RelationshipsMigrated["child_accounts"] != 1 || record.RelationshipsMigrated["activities"] != 1 ||
			record.RelationshipsMigrated["tasks"] != 1 || record.RelationshipsMigrated["notes"] != 1 ||
			record.RelationshipsMigrated["ownership_history"] != 1 || record.RelationshipsMigrated["tags"] != 1 {
			t.Fatalf("relationships migrated = %+v, want exactly one of each kind", record.RelationshipsMigrated)
		}

		fromSurvivor, err := s.ListAccountMerges(ctx, scope, survivor.ID)
		if err != nil || len(fromSurvivor) != 1 {
			t.Fatalf("list merges from survivor = %+v, %v", fromSurvivor, err)
		}
		fromDuplicate, err := s.ListAccountMerges(ctx, scope, duplicate.ID)
		if err != nil || len(fromDuplicate) != 1 {
			t.Fatalf("list merges from duplicate id = %+v, %v", fromDuplicate, err)
		}
		if fromSurvivor[0].ID != fromDuplicate[0].ID {
			t.Fatalf("survivor and duplicate see different merge records")
		}
	})
}

func TestMergeAccountsRejectsUnknownAccounts(t *testing.T) {
	eachMergeStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := mergeScope(name)
		survivor, err := s.CreateAccount(ctx, scope, &Account{Name: "Acme", OwnerID: "alice"})
		if err != nil {
			t.Fatalf("create survivor: %v", err)
		}
		if _, _, err := s.MergeAccounts(ctx, scope, MergeAccountsInput{
			SurvivorID: survivor.ID, DuplicateID: "does-not-exist",
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("merge with unknown duplicate = %v, want ErrNotFound", err)
		}
		if _, _, err := s.MergeAccounts(ctx, scope, MergeAccountsInput{
			SurvivorID: "does-not-exist", DuplicateID: survivor.ID,
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("merge with unknown survivor = %v, want ErrNotFound", err)
		}
	})
}
