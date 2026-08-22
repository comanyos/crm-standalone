package fga_test

import (
	"context"
	"testing"

	"github.com/authincore/authincore/internal/fga"
	"github.com/google/uuid"
)

// memStore is an in-memory fga.Store for unit tests.
type memStore struct {
	tuples []fga.Tuple
	org    map[uuid.UUID][]fga.Tuple
}

func newMemStore() *memStore { return &memStore{org: make(map[uuid.UUID][]fga.Tuple)} }

func (m *memStore) Write(_ context.Context, orgID uuid.UUID, t fga.Tuple) error {
	for _, existing := range m.org[orgID] {
		if existing == t {
			return nil // idempotent
		}
	}
	m.org[orgID] = append(m.org[orgID], t)
	return nil
}

func (m *memStore) Delete(_ context.Context, orgID uuid.UUID, t fga.Tuple) error {
	ts := m.org[orgID]
	out := ts[:0]
	for _, existing := range ts {
		if existing != t {
			out = append(out, existing)
		}
	}
	m.org[orgID] = out
	return nil
}

func (m *memStore) DirectTuples(_ context.Context, orgID uuid.UUID, objectType, objectID, relation, subjectType, subjectID string) ([]fga.Tuple, error) {
	match := func(filter, value string) bool { return filter == "" || filter == value }
	var result []fga.Tuple
	for _, t := range m.org[orgID] {
		if match(objectType, t.ObjectType) &&
			match(objectID, t.ObjectID) &&
			match(relation, t.Relation) &&
			match(subjectType, t.SubjectType) &&
			match(subjectID, t.SubjectID) {
			result = append(result, t)
		}
	}
	return result, nil
}

func (m *memStore) DeleteObject(_ context.Context, orgID uuid.UUID, objectType, objectID string) error {
	ts := m.org[orgID]
	out := ts[:0]
	for _, t := range ts {
		if !(t.ObjectType == objectType && t.ObjectID == objectID) {
			out = append(out, t)
		}
	}
	m.org[orgID] = out
	return nil
}

func (m *memStore) DeleteSubject(_ context.Context, orgID uuid.UUID, subjectType, subjectID string) error {
	ts := m.org[orgID]
	out := ts[:0]
	for _, t := range ts {
		if !(t.SubjectType == subjectType && t.SubjectID == subjectID) {
			out = append(out, t)
		}
	}
	m.org[orgID] = out
	return nil
}

func newSvc() (*fga.Service, *memStore, uuid.UUID) {
	store := newMemStore()
	svc := fga.NewService(store, fga.DefaultModel())
	orgID := uuid.New()
	return svc, store, orgID
}

func write(t *testing.T, svc *fga.Service, orgID uuid.UUID, tuple fga.Tuple) {
	t.Helper()
	if err := svc.Write(context.Background(), orgID, tuple); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func checkExpect(t *testing.T, svc *fga.Service, orgID uuid.UUID, req fga.CheckRequest, want bool) {
	t.Helper()
	got, err := svc.Check(context.Background(), req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got != want {
		t.Errorf("Check(%v:%v %v %v:%v) = %v, want %v",
			req.ObjectType, req.ObjectID, req.Relation, req.SubjectType, req.SubjectID, got, want)
	}
}

func TestCheck_DirectTuple(t *testing.T) {
	svc, _, orgID := newSvc()
	alice := uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "lead:123",
		Relation:    "editor",
		SubjectType: "user", SubjectID: alice,
	})

	checkExpect(t, svc, orgID, fga.CheckRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "lead:123",
		Relation: "editor", SubjectType: "user", SubjectID: alice,
	}, true)

	checkExpect(t, svc, orgID, fga.CheckRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "lead:123",
		Relation: "owner", SubjectType: "user", SubjectID: alice,
	}, false)
}

func TestCheck_InheritedRelation(t *testing.T) {
	// DefaultModel: document owner → editor → viewer
	svc, _, orgID := newSvc()
	alice := uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "lead:123",
		Relation: "owner", SubjectType: "user", SubjectID: alice,
	})

	tests := []struct {
		relation string
		want     bool
	}{
		{"owner", true},
		{"editor", true},    // implied by owner
		{"viewer", true},    // implied by owner→editor→viewer
		{"approver", true},  // implied by owner
		{"commenter", true}, // implied by owner→editor
	}

	for _, tc := range tests {
		checkExpect(t, svc, orgID, fga.CheckRequest{
			OrgID: orgID, ObjectType: "document", ObjectID: "lead:123",
			Relation: tc.relation, SubjectType: "user", SubjectID: alice,
		}, tc.want)
	}
}

func TestCheck_InheritedChain(t *testing.T) {
	svc, _, orgID := newSvc()
	bob := uuid.NewString()

	// bob is editor — should be viewer but not owner
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "doc:42",
		Relation: "editor", SubjectType: "user", SubjectID: bob,
	})

	checkExpect(t, svc, orgID, fga.CheckRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "doc:42",
		Relation: "viewer", SubjectType: "user", SubjectID: bob,
	}, true)

	checkExpect(t, svc, orgID, fga.CheckRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "doc:42",
		Relation: "owner", SubjectType: "user", SubjectID: bob,
	}, false)
}

func TestCheck_GroupExpansion(t *testing.T) {
	svc, _, orgID := newSvc()
	charlie := uuid.NewString()
	salesGroup := uuid.NewString()

	// sales group can view document
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "deal:99",
		Relation: "viewer", SubjectType: "group", SubjectID: salesGroup,
		SubjectRelation: "member",
	})
	// charlie is a member of sales
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "group", ObjectID: salesGroup,
		Relation: "member", SubjectType: "user", SubjectID: charlie,
	})

	checkExpect(t, svc, orgID, fga.CheckRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "deal:99",
		Relation: "viewer", SubjectType: "user", SubjectID: charlie,
	}, true)

	// user not in group should be denied
	dave := uuid.NewString()
	checkExpect(t, svc, orgID, fga.CheckRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "deal:99",
		Relation: "viewer", SubjectType: "user", SubjectID: dave,
	}, false)
}

func TestListObjects(t *testing.T) {
	svc, _, orgID := newSvc()
	alice := uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "lead:1",
		Relation: "editor", SubjectType: "user", SubjectID: alice,
	})
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "lead:2",
		Relation: "owner", SubjectType: "user", SubjectID: alice,
	})
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "lead:3",
		Relation: "viewer", SubjectType: "user", SubjectID: uuid.NewString(),
	})

	ids, err := svc.ListObjects(context.Background(), fga.ListObjectsRequest{
		OrgID: orgID, ObjectType: "document",
		Relation: "editor", SubjectType: "user", SubjectID: alice,
	})
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}

	// alice is direct editor of lead:1, implied editor of lead:2 (owner→editor)
	found := make(map[string]bool)
	for _, id := range ids {
		found[id] = true
	}
	if !found["lead:1"] {
		t.Error("expected lead:1 in ListObjects result")
	}
	if !found["lead:2"] {
		t.Error("expected lead:2 in ListObjects result (via owner→editor)")
	}
	if found["lead:3"] {
		t.Error("lead:3 should not appear — alice is not viewer/editor/owner of it")
	}
}

func TestListSubjects(t *testing.T) {
	svc, _, orgID := newSvc()
	alice, bob := uuid.NewString(), uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "task:7",
		Relation: "editor", SubjectType: "user", SubjectID: alice,
	})
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "task:7",
		Relation: "editor", SubjectType: "user", SubjectID: bob,
	})

	tuples, err := svc.ListSubjects(context.Background(), fga.ListSubjectsRequest{
		OrgID: orgID, ObjectType: "document", ObjectID: "task:7",
		Relation: "editor", SubjectType: "user",
	})
	if err != nil {
		t.Fatalf("ListSubjects: %v", err)
	}
	if len(tuples) != 2 {
		t.Errorf("expected 2 subjects, got %d", len(tuples))
	}
}

func TestWrite_Idempotent(t *testing.T) {
	svc, store, orgID := newSvc()
	tuple := fga.Tuple{
		ObjectType: "document", ObjectID: "x",
		Relation: "viewer", SubjectType: "user", SubjectID: "u",
	}
	for i := 0; i < 3; i++ {
		write(t, svc, orgID, tuple)
	}
	tuples := store.org[orgID]
	if len(tuples) != 1 {
		t.Errorf("expected 1 tuple after idempotent writes, got %d", len(tuples))
	}
}

func TestWrite_InvalidTuple(t *testing.T) {
	svc, _, orgID := newSvc()
	ctx := context.Background()

	cases := []fga.Tuple{
		{ObjectID: "x", Relation: "r", SubjectType: "user", SubjectID: "u"},     // missing ObjectType
		{ObjectType: "doc", Relation: "r", SubjectType: "user", SubjectID: "u"}, // missing ObjectID
		{ObjectType: "doc", ObjectID: "x", SubjectType: "user", SubjectID: "u"}, // missing Relation
		{ObjectType: "doc", ObjectID: "x", Relation: "r", SubjectID: "u"},       // missing SubjectType
		{ObjectType: "doc", ObjectID: "x", Relation: "r", SubjectType: "user"},  // missing SubjectID
	}

	for _, tc := range cases {
		if err := svc.Write(ctx, orgID, tc); err == nil {
			t.Errorf("expected error for invalid tuple %+v, got nil", tc)
		}
	}
}

func TestCrossOrgIsolation(t *testing.T) {
	svc, _, _ := newSvc()
	org1, org2 := uuid.New(), uuid.New()
	alice := uuid.NewString()

	write(t, svc, org1, fga.Tuple{
		ObjectType: "document", ObjectID: "secret",
		Relation: "viewer", SubjectType: "user", SubjectID: alice,
	})

	// alice should NOT see the doc when queried under org2
	checkExpect(t, svc, org2, fga.CheckRequest{
		OrgID: org2, ObjectType: "document", ObjectID: "secret",
		Relation: "viewer", SubjectType: "user", SubjectID: alice,
	}, false)
}

func TestDeleteObject(t *testing.T) {
	svc, store, orgID := newSvc()
	alice := uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "todelete",
		Relation: "owner", SubjectType: "user", SubjectID: alice,
	})

	if err := svc.DeleteObject(context.Background(), orgID, "document", "todelete"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if len(store.org[orgID]) != 0 {
		t.Error("expected tuples to be removed after DeleteObject")
	}
}

func TestDeleteSubject(t *testing.T) {
	svc, store, orgID := newSvc()
	alice := uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "d1",
		Relation: "editor", SubjectType: "user", SubjectID: alice,
	})
	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "d2",
		Relation: "viewer", SubjectType: "user", SubjectID: alice,
	})

	if err := svc.DeleteSubject(context.Background(), orgID, "user", alice); err != nil {
		t.Fatalf("DeleteSubject: %v", err)
	}
	if len(store.org[orgID]) != 0 {
		t.Error("expected all tuples removed after DeleteSubject")
	}
}

func TestListTuples(t *testing.T) {
	svc, _, orgID := newSvc()
	alice, bob := uuid.NewString(), uuid.NewString()

	seed := []fga.Tuple{
		{ObjectType: "document", ObjectID: "d1", Relation: "editor", SubjectType: "user", SubjectID: alice},
		{ObjectType: "document", ObjectID: "d1", Relation: "viewer", SubjectType: "user", SubjectID: bob},
		{ObjectType: "document", ObjectID: "d2", Relation: "owner", SubjectType: "user", SubjectID: alice},
		{ObjectType: "project", ObjectID: "p1", Relation: "viewer", SubjectType: "user", SubjectID: bob},
	}
	for _, tup := range seed {
		write(t, svc, orgID, tup)
	}

	tests := []struct {
		name   string
		filter fga.TupleFilter
		want   int
	}{
		{"no filter returns all", fga.TupleFilter{}, 4},
		{"by object type", fga.TupleFilter{ObjectType: "document"}, 3},
		{"by object id", fga.TupleFilter{ObjectType: "document", ObjectID: "d1"}, 2},
		{"by relation", fga.TupleFilter{Relation: "viewer"}, 2},
		{"by subject", fga.TupleFilter{SubjectType: "user", SubjectID: alice}, 2},
		{"compound filter", fga.TupleFilter{ObjectType: "document", Relation: "editor", SubjectID: alice}, 1},
		{"no match", fga.TupleFilter{ObjectType: "lead"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.ListTuples(context.Background(), orgID, tt.filter)
			if err != nil {
				t.Fatalf("ListTuples: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("ListTuples(%+v) = %d tuples, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestListTuples_OrgIsolation(t *testing.T) {
	svc, _, orgID := newSvc()
	other := uuid.New()
	alice := uuid.NewString()

	write(t, svc, orgID, fga.Tuple{
		ObjectType: "document", ObjectID: "d1",
		Relation: "editor", SubjectType: "user", SubjectID: alice,
	})

	got, err := svc.ListTuples(context.Background(), other, fga.TupleFilter{})
	if err != nil {
		t.Fatalf("ListTuples: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no tuples for unrelated org, got %d", len(got))
	}
}

func TestModel(t *testing.T) {
	svc, _, _ := newSvc()
	m := svc.Model()
	if _, ok := m.Types["document"]; !ok {
		t.Fatal("expected default model to define the document type")
	}
	if got := m.Types["document"].Implies["owner"]; len(got) == 0 {
		t.Error("expected owner to imply relations on document")
	}
}
