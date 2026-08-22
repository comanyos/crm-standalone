package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeRepo struct {
	flows map[uuid.UUID]*domflow.Flow
}

func newFakeRepo() *fakeRepo { return &fakeRepo{flows: make(map[uuid.UUID]*domflow.Flow)} }

func (r *fakeRepo) Insert(_ context.Context, f *domflow.Flow) error {
	r.flows[f.ID] = f
	return nil
}
func (r *fakeRepo) ByID(_ context.Context, id uuid.UUID) (*domflow.Flow, error) {
	f, ok := r.flows[id]
	if !ok {
		return nil, domainerr.New(domainerr.CodeNotFound, "flow not found")
	}
	return f, nil
}
func (r *fakeRepo) Update(_ context.Context, f *domflow.Flow) error {
	r.flows[f.ID] = f
	return nil
}
func (r *fakeRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.flows, id)
	return nil
}

type fakeStrategy struct {
	id       string
	handleFn func(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error)
}

func (s *fakeStrategy) ID() string { return s.id }
func (s *fakeStrategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{Type: "input", Group: s.id})
	return nil
}
func (s *fakeStrategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	return s.handleFn(ctx, f, body)
}

// --- tests ---

func TestManager_Create(t *testing.T) {
	repo := newFakeRepo()
	strategy := &fakeStrategy{id: "password"}
	m := flow.NewManager(repo, strategy)

	orgID := uuid.New()
	f, err := m.Create(context.Background(), domflow.TypeLogin, orgID, "/home")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ID == uuid.Nil {
		t.Error("expected non-nil flow ID")
	}
	if f.Type != domflow.TypeLogin {
		t.Errorf("expected TypeLogin, got %v", f.Type)
	}
	if f.OrgID != orgID {
		t.Errorf("org_id mismatch")
	}
	if len(f.UI.Nodes) == 0 {
		t.Error("expected strategy to populate UI nodes")
	}

	// Verify flow was persisted.
	stored, err := repo.ByID(context.Background(), f.ID)
	if err != nil || stored.ID != f.ID {
		t.Error("flow not persisted to repo")
	}
}

func TestManager_Get_Found(t *testing.T) {
	repo := newFakeRepo()
	m := flow.NewManager(repo)

	f, _ := m.Create(context.Background(), domflow.TypeRegistration, uuid.New(), "")
	got, err := m.Get(context.Background(), f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != f.ID {
		t.Errorf("ID mismatch")
	}
}

func TestManager_Get_NotFound(t *testing.T) {
	m := flow.NewManager(newFakeRepo())
	_, err := m.Get(context.Background(), uuid.New())
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %v", err)
	}
}

func TestManager_Submit_Success(t *testing.T) {
	repo := newFakeRepo()
	identityID := uuid.New()
	strategy := &fakeStrategy{
		id: "password",
		handleFn: func(_ context.Context, _ *domflow.Flow, _ json.RawMessage) (*uuid.UUID, error) {
			return &identityID, nil
		},
	}
	m := flow.NewManager(repo, strategy)

	f, _ := m.Create(context.Background(), domflow.TypeLogin, uuid.New(), "")
	body, _ := json.Marshal(map[string]string{"identifier": "u@e.com", "password": "pw"})

	result, err := m.Submit(context.Background(), f.ID, "password", body)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !result.Done {
		t.Error("expected result.Done = true")
	}
	if result.IdentityID == nil || *result.IdentityID != identityID {
		t.Errorf("identity ID mismatch")
	}
}

func TestManager_Submit_StrategyError(t *testing.T) {
	repo := newFakeRepo()
	strategy := &fakeStrategy{
		id: "password",
		handleFn: func(_ context.Context, f *domflow.Flow, _ json.RawMessage) (*uuid.UUID, error) {
			f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{Text: "bad creds", Type: "error"})
			return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid credentials")
		},
	}
	m := flow.NewManager(repo, strategy)
	f, _ := m.Create(context.Background(), domflow.TypeLogin, uuid.New(), "")

	result, err := m.Submit(context.Background(), f.ID, "password", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Done {
		t.Error("expected result.Done = false after strategy error")
	}
	if len(result.Flow.UI.Messages) == 0 {
		t.Error("expected UI messages on strategy error")
	}
}

func TestManager_Submit_UnknownMethod(t *testing.T) {
	m := flow.NewManager(newFakeRepo())
	f, _ := m.Create(context.Background(), domflow.TypeLogin, uuid.New(), "")
	_, err := m.Submit(context.Background(), f.ID, "webauthn", json.RawMessage(`{}`))
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument for unknown method, got %v", err)
	}
}
