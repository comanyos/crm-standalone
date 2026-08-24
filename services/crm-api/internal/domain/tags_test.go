package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Tags have two Store implementations, and the same contract must hold for
// both — see revenue_store_parity_test.go's doc comment for why this file
// runs identical assertions against MemStore and PgStore rather than
// keeping the behavioural tests in only one of them.
func eachTagStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

// tagScope returns a scope nothing else in the suite shares. The test
// database is reused across runs and never truncated between them, so a
// fixed tenant id would collide with rows an earlier run left behind — see
// revenueScope's doc comment for the same reasoning.
func tagScope(label string) Scope {
	return Scope{TenantID: fmt.Sprintf("tenant-tags-%s-%d", label, time.Now().UnixNano())}
}

func TestTagDefinitionValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *TagDefinition
		want string
	}{
		{"minimal", &TagDefinition{Name: "Hot"}, ""},
		{"with color", &TagDefinition{Name: "Hot", Color: "#ff0000"}, ""},
		{"blank name", &TagDefinition{Name: "   "}, "needs a name"},
		{"bad color", &TagDefinition{Name: "Hot", Color: "red"}, "hex code"},
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

func TestTagAssignmentValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *TagAssignment
		want string
	}{
		{"minimal", &TagAssignment{TagID: "t1", ObjectType: "account", ObjectID: "a1"}, ""},
		{"no tag", &TagAssignment{ObjectType: "account", ObjectID: "a1"}, "needs a tag"},
		{"unknown type", &TagAssignment{TagID: "t1", ObjectType: "invoice", ObjectID: "a1"}, "not a taggable"},
		{"no object id", &TagAssignment{TagID: "t1", ObjectType: "account"}, "needs a record id"},
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

// TestTagLifecycleIsTheSameInBothStores exercises create, duplicate-name
// rejection, assign/remove idempotency, listing by record and by tag, and
// cascade-delete — the whole surface a bulk tag action depends on.
func TestTagLifecycleIsTheSameInBothStores(t *testing.T) {
	eachTagStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := tagScope(name)

		tag, err := s.CreateTagDefinition(ctx, scope, &TagDefinition{Name: "Hot Lead", Color: "#ff0000"})
		if err != nil {
			t.Fatalf("create tag: %v", err)
		}
		if tag.ID == "" {
			t.Fatal("expected an assigned id")
		}

		if _, err := s.CreateTagDefinition(ctx, scope, &TagDefinition{Name: "hot lead"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate name (case-insensitive) = %v, want ErrConflict", err)
		}

		other, err := s.CreateTagDefinition(ctx, scope, &TagDefinition{Name: "Needs Follow-up"})
		if err != nil {
			t.Fatalf("create second tag: %v", err)
		}

		tags, err := s.ListTagDefinitions(ctx, scope)
		if err != nil {
			t.Fatalf("list tags: %v", err)
		}
		if len(tags) != 2 {
			t.Fatalf("list tags = %d, want 2", len(tags))
		}

		acct1, err := s.CreateAccount(ctx, scope, &Account{Name: "Acct One", OwnerID: "owner-1"})
		if err != nil {
			t.Fatalf("create account 1: %v", err)
		}
		acct2, err := s.CreateAccount(ctx, scope, &Account{Name: "Acct Two", OwnerID: "owner-1"})
		if err != nil {
			t.Fatalf("create account 2: %v", err)
		}

		// Assigning is idempotent: the same call twice must not error and
		// must not create a second row — the property a bulk "add tag"
		// retry depends on.
		first, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: tag.ID, ObjectType: "account", ObjectID: acct1.ID})
		if err != nil {
			t.Fatalf("assign tag: %v", err)
		}
		second, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: tag.ID, ObjectType: "account", ObjectID: acct1.ID})
		if err != nil {
			t.Fatalf("re-assign tag: %v", err)
		}
		if first.ID != second.ID {
			t.Fatalf("re-assigning created a second row: %s != %s", first.ID, second.ID)
		}

		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: other.ID, ObjectType: "account", ObjectID: acct1.ID}); err != nil {
			t.Fatalf("assign second tag: %v", err)
		}
		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: tag.ID, ObjectType: "account", ObjectID: acct2.ID}); err != nil {
			t.Fatalf("assign to second record: %v", err)
		}

		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: "missing", ObjectType: "account", ObjectID: acct1.ID}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("assign unknown tag = %v, want ErrInvalid", err)
		}
		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: tag.ID, ObjectType: "account", ObjectID: "does-not-exist"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("assign to nonexistent account = %v, want ErrInvalid", err)
		}

		recordTags, err := s.ListTagsForRecord(ctx, scope, "account", acct1.ID)
		if err != nil {
			t.Fatalf("list tags for record: %v", err)
		}
		if len(recordTags) != 2 {
			t.Fatalf("tags for acct1 = %d, want 2", len(recordTags))
		}

		assignments, next, err := s.ListTaggedRecords(ctx, scope, tag.ID, "", Page{})
		if err != nil {
			t.Fatalf("list tagged records: %v", err)
		}
		if next != "" {
			t.Fatalf("unexpected next token %q", next)
		}
		if len(assignments) != 2 {
			t.Fatalf("records tagged %q = %d, want 2", tag.Name, len(assignments))
		}

		// Removing is idempotent too: removing twice must not error.
		if err := s.RemoveTag(ctx, scope, "account", acct1.ID, tag.ID); err != nil {
			t.Fatalf("remove tag: %v", err)
		}
		if err := s.RemoveTag(ctx, scope, "account", acct1.ID, tag.ID); err != nil {
			t.Fatalf("re-remove tag: %v", err)
		}
		recordTags, err = s.ListTagsForRecord(ctx, scope, "account", acct1.ID)
		if err != nil {
			t.Fatalf("list tags for record after remove: %v", err)
		}
		if len(recordTags) != 1 {
			t.Fatalf("tags for acct-1 after remove = %d, want 1", len(recordTags))
		}

		// Deleting a tag cascades its remaining assignment away.
		if err := s.DeleteTagDefinition(ctx, scope, tag.ID); err != nil {
			t.Fatalf("delete tag: %v", err)
		}
		if _, err := s.GetTagDefinition(ctx, scope, tag.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get deleted tag = %v, want ErrNotFound", err)
		}
		assignments, _, err = s.ListTaggedRecords(ctx, scope, tag.ID, "", Page{})
		if err != nil {
			t.Fatalf("list tagged records after delete: %v", err)
		}
		if len(assignments) != 0 {
			t.Fatalf("assignments survived tag delete: %d", len(assignments))
		}

		if err := s.DeleteTagDefinition(ctx, scope, tag.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("re-delete tag = %v, want ErrNotFound", err)
		}
	})
}

func TestAssignTagRejectsUnknownObjectType(t *testing.T) {
	eachTagStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := tagScope(name)
		tag, err := s.CreateTagDefinition(ctx, scope, &TagDefinition{Name: "Hot"})
		if err != nil {
			t.Fatalf("create tag: %v", err)
		}
		if _, err := s.AssignTag(ctx, scope, &TagAssignment{TagID: tag.ID, ObjectType: "invoice", ObjectID: "x"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("assign to unknown type = %v, want ErrInvalid", err)
		}
	})
}
