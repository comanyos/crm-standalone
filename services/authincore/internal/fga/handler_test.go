package fga_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/authincore/authincore/internal/fga"
	"github.com/google/uuid"
)

// passthrough is a no-op middleware standing in for requireAuth.
func passthrough(h http.HandlerFunc) http.HandlerFunc { return h }

func newHandlerMux(org fga.OrgResolver) (*http.ServeMux, *memStore) {
	store := newMemStore()
	svc := fga.NewService(store, fga.DefaultModel())
	mux := http.NewServeMux()
	fga.NewHandler(svc).Routes(mux, "/authz", passthrough, org)
	return mux, store
}

// When the org resolver returns false, the handler must reject and write nothing.
func TestHandler_RejectsUnresolvedOrg(t *testing.T) {
	mux, store := newHandlerMux(func(*http.Request) (uuid.UUID, bool) { return uuid.Nil, false })

	body := `{"object_type":"lead","object_id":"123","relation":"editor","subject_type":"user","subject_id":"alice"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/authz/tuples", strings.NewReader(body)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(store.org) != 0 {
		t.Error("no tuple should be written when org is unresolved")
	}
}

// The org must come from the resolver, never from an org_id in the body.
func TestHandler_IgnoresBodyOrgID(t *testing.T) {
	trusted := uuid.New()
	spoofed := uuid.New()
	mux, store := newHandlerMux(func(*http.Request) (uuid.UUID, bool) { return trusted, true })

	// Body carries a spoofed org_id that must be ignored.
	body := `{"org_id":"` + spoofed.String() + `","object_type":"lead","object_id":"123","relation":"editor","subject_type":"user","subject_id":"alice"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/authz/tuples", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := len(store.org[trusted]); got != 1 {
		t.Errorf("tuple written under trusted org = %d, want 1", got)
	}
	if got := len(store.org[spoofed]); got != 0 {
		t.Errorf("tuple written under spoofed org = %d, want 0", got)
	}

	// And the check path resolves against the trusted org too.
	allowed, err := fga.NewService(store, fga.DefaultModel()).Check(context.Background(), fga.CheckRequest{
		OrgID: trusted, ObjectType: "lead", ObjectID: "123",
		Relation: "editor", SubjectType: "user", SubjectID: "alice",
	})
	if err != nil || !allowed {
		t.Errorf("Check under trusted org = %v (err %v), want true", allowed, err)
	}
}
