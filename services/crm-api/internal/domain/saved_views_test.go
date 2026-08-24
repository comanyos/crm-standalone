package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Saved views have two Store implementations, and the same contract must
// hold for both — see revenue_store_parity_test.go's doc comment.
func eachSavedViewStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

// savedViewScope returns a scope nothing else in the suite shares — see
// tagScope's doc comment for why a fixed tenant id would collide with an
// earlier run's rows in the reused test database.
func savedViewScope(label string) Scope {
	return Scope{TenantID: fmt.Sprintf("tenant-views-%s-%d", label, time.Now().UnixNano())}
}

func TestSavedViewValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *SavedView
		want string
	}{
		{"minimal", &SavedView{OwnerID: "u1", ObjectType: "account", Name: "My accounts"}, ""},
		{"with filters and columns", &SavedView{
			OwnerID: "u1", ObjectType: "account", Name: "Gold tier",
			Filters: map[string]string{"tier": "gold"}, Columns: []string{"name", "tier"},
		}, ""},
		{"no owner", &SavedView{ObjectType: "account", Name: "X"}, "needs an owner"},
		{"unknown object type", &SavedView{OwnerID: "u1", ObjectType: "invoice", Name: "X"}, "not a type of list"},
		{"blank name", &SavedView{OwnerID: "u1", ObjectType: "account", Name: "  "}, "needs a name"},
		{"blank filter key", &SavedView{
			OwnerID: "u1", ObjectType: "account", Name: "X", Filters: map[string]string{"": "v"},
		}, "cannot be blank"},
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

// TestSavedViewLifecycleIsTheSameInBothStores exercises create, duplicate
// rejection, private listing/reading, update and delete.
func TestSavedViewLifecycleIsTheSameInBothStores(t *testing.T) {
	eachSavedViewStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := savedViewScope(name)

		view, err := s.CreateSavedView(ctx, scope, &SavedView{
			OwnerID: "alice", ObjectType: "account", Name: "Gold tier",
			Filters: map[string]string{"tier": "gold"}, Sort: "-created_at",
			Columns: []string{"name", "tier"},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if view.ID == "" {
			t.Fatal("expected an assigned id")
		}
		if view.Filters["tier"] != "gold" {
			t.Fatalf("filters = %+v, want tier=gold", view.Filters)
		}

		if _, err := s.CreateSavedView(ctx, scope, &SavedView{
			OwnerID: "alice", ObjectType: "account", Name: "gold tier",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate name (case-insensitive) = %v, want ErrConflict", err)
		}

		// The same name is fine for a different owner or a different list.
		if _, err := s.CreateSavedView(ctx, scope, &SavedView{
			OwnerID: "bob", ObjectType: "account", Name: "Gold tier",
		}); err != nil {
			t.Fatalf("create for a different owner: %v", err)
		}
		if _, err := s.CreateSavedView(ctx, scope, &SavedView{
			OwnerID: "alice", ObjectType: "lead", Name: "Gold tier",
		}); err != nil {
			t.Fatalf("create for a different object type: %v", err)
		}

		got, err := s.GetSavedView(ctx, scope, "alice", view.ID)
		if err != nil {
			t.Fatalf("get own view: %v", err)
		}
		if got.Name != "Gold tier" {
			t.Fatalf("name = %q, want %q", got.Name, "Gold tier")
		}

		// Private by construction: bob cannot read alice's view by id.
		if _, err := s.GetSavedView(ctx, scope, "bob", view.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get someone else's view = %v, want ErrNotFound", err)
		}

		aliceAccountViews, err := s.ListSavedViews(ctx, scope, "alice", "account")
		if err != nil {
			t.Fatalf("list alice's account views: %v", err)
		}
		if len(aliceAccountViews) != 1 {
			t.Fatalf("alice's account views = %d, want 1 (bob's and alice's lead view must not appear)", len(aliceAccountViews))
		}

		aliceAllViews, err := s.ListSavedViews(ctx, scope, "alice", "")
		if err != nil {
			t.Fatalf("list alice's views: %v", err)
		}
		if len(aliceAllViews) != 2 {
			t.Fatalf("alice's views (all types) = %d, want 2", len(aliceAllViews))
		}

		updated, err := s.UpdateSavedView(ctx, scope, "alice", &SavedView{
			ID: view.ID, ObjectType: "account", Name: "Gold tier (renamed)",
			Filters: map[string]string{"tier": "gold", "owner_id": "me"}, Sort: "",
		})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.Name != "Gold tier (renamed)" || len(updated.Filters) != 2 {
			t.Fatalf("update did not apply: %+v", updated)
		}

		// Bob cannot update or delete alice's view.
		if _, err := s.UpdateSavedView(ctx, scope, "bob", &SavedView{
			ID: view.ID, ObjectType: "account", Name: "Hijacked",
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("bob update alice's view = %v, want ErrNotFound", err)
		}
		if err := s.DeleteSavedView(ctx, scope, "bob", view.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("bob delete alice's view = %v, want ErrNotFound", err)
		}

		if err := s.DeleteSavedView(ctx, scope, "alice", view.ID); err != nil {
			t.Fatalf("alice delete own view: %v", err)
		}
		if _, err := s.GetSavedView(ctx, scope, "alice", view.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get deleted view = %v, want ErrNotFound", err)
		}
		if err := s.DeleteSavedView(ctx, scope, "alice", view.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("re-delete = %v, want ErrNotFound", err)
		}
	})
}

func TestListSavedViewsRequiresAnOwner(t *testing.T) {
	eachSavedViewStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := savedViewScope(name)
		if _, err := s.ListSavedViews(ctx, scope, "", "account"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("list with no owner = %v, want ErrInvalid", err)
		}
	})
}
