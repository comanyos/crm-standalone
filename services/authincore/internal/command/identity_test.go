package command

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/domain/identity"
	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
)

// mockIdentityRepo implements IdentityRepository for testing
type mockIdentityRepo struct {
	identities map[uuid.UUID]*identity.Identity
	insertErr  error
	updateErr  error
	deleteErr  error
	byIDErr    error
}

func newMockIdentityRepo() *mockIdentityRepo {
	return &mockIdentityRepo{
		identities: make(map[uuid.UUID]*identity.Identity),
	}
}

func (m *mockIdentityRepo) Insert(ctx context.Context, i *identity.Identity) error {
	if m.insertErr != nil {
		return m.insertErr
	}
	m.identities[i.ID] = i
	return nil
}

func (m *mockIdentityRepo) Update(ctx context.Context, i *identity.Identity) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.identities[i.ID] = i
	return nil
}

func (m *mockIdentityRepo) Delete(ctx context.Context, id, orgID uuid.UUID) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.identities, id)
	return nil
}

func (m *mockIdentityRepo) ByID(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	if m.byIDErr != nil {
		return nil, m.byIDErr
	}
	i, ok := m.identities[id]
	if !ok {
		return nil, domainerr.New(domainerr.CodeNotFound, "identity not found")
	}
	return i, nil
}

// mockEventstore implements eventstore.Eventstore for testing
type mockEventstore struct {
	events  []*eventstore.Event
	pushErr error
	pushed  int
}

func newMockEventstore() *mockEventstore {
	return &mockEventstore{
		events: make([]*eventstore.Event, 0),
	}
}

func (m *mockEventstore) Push(ctx context.Context, events ...*eventstore.Event) error {
	if m.pushErr != nil {
		return m.pushErr
	}
	m.events = append(m.events, events...)
	m.pushed += len(events)
	return nil
}

func (m *mockEventstore) ByAggregate(_ context.Context, aggregateType eventstore.AggregateType, aggregateID uuid.UUID) ([]*eventstore.Event, error) {
	var matched []*eventstore.Event
	for _, event := range m.events {
		if event.AggregateType == aggregateType && event.AggregateID == aggregateID {
			matched = append(matched, event)
		}
	}
	return matched, nil
}

func (m *mockEventstore) Search(_ context.Context, filter eventstore.Filter) ([]*eventstore.Event, error) {
	var matched []*eventstore.Event
	for _, event := range m.events {
		if filter.OrgID != nil && event.OrgID != *filter.OrgID {
			continue
		}
		if filter.AggregateType != nil && event.AggregateType != *filter.AggregateType {
			continue
		}
		if filter.AggregateID != nil && event.AggregateID != *filter.AggregateID {
			continue
		}
		matched = append(matched, event)
		if filter.Limit > 0 && len(matched) >= filter.Limit {
			break
		}
	}
	return matched, nil
}

// mockSchemaValidator implements SchemaValidator for testing
type mockSchemaValidator struct {
	validateErr error
	validateFn  func(ctx context.Context, orgID, schemaID string, traits json.RawMessage) error
}

func (m *mockSchemaValidator) Validate(ctx context.Context, orgID, schemaID string, traits json.RawMessage) error {
	if m.validateFn != nil {
		return m.validateFn(ctx, orgID, schemaID, traits)
	}
	return m.validateErr
}

func TestCreateIdentity(t *testing.T) {
	tests := []struct {
		name        string
		cmd         CreateIdentityCommand
		schema      SchemaValidator
		insertErr   error
		pushErr     error
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			cmd: CreateIdentityCommand{
				OrgID:    uuid.New(),
				SchemaID: "default",
				Traits:   json.RawMessage(`{"email":"test@example.com"}`),
			},
			wantErr: false,
		},
		{
			name: "missing schema_id",
			cmd: CreateIdentityCommand{
				OrgID:    uuid.New(),
				SchemaID: "",
				Traits:   json.RawMessage(`{"email":"test@example.com"}`),
			},
			wantErr:     true,
			errContains: "schema_id is required",
		},
		{
			name: "schema validation fails",
			cmd: CreateIdentityCommand{
				OrgID:    uuid.New(),
				SchemaID: "default",
				Traits:   json.RawMessage(`{"email":"invalid"}`),
			},
			schema: &mockSchemaValidator{
				validateErr: errors.New("validation failed"),
			},
			wantErr:     true,
			errContains: "traits validation",
		},
		{
			name: "insert fails",
			cmd: CreateIdentityCommand{
				OrgID:    uuid.New(),
				SchemaID: "default",
				Traits:   json.RawMessage(`{"email":"test@example.com"}`),
			},
			insertErr:   errors.New("database error"),
			wantErr:     true,
			errContains: "create identity insert",
		},
		{
			name: "event push fails",
			cmd: CreateIdentityCommand{
				OrgID:    uuid.New(),
				SchemaID: "default",
				Traits:   json.RawMessage(`{"email":"test@example.com"}`),
			},
			pushErr:     errors.New("event store error"),
			wantErr:     true,
			errContains: "create identity push event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockIdentityRepo()
			repo.insertErr = tt.insertErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, repo, nil)
			if tt.schema != nil {
				cmds = cmds.WithSchemaValidator(tt.schema)
			}

			result, err := cmds.CreateIdentity(context.Background(), tt.cmd)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("expected result, got nil")
				return
			}

			if result.OrgID != tt.cmd.OrgID {
				t.Errorf("expected OrgID %v, got %v", tt.cmd.OrgID, result.OrgID)
			}

			if result.SchemaID != tt.cmd.SchemaID {
				t.Errorf("expected SchemaID %q, got %q", tt.cmd.SchemaID, result.SchemaID)
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestUpdateIdentity(t *testing.T) {
	orgID := uuid.New()
	identityID := uuid.New()

	tests := []struct {
		name        string
		setup       func(*mockIdentityRepo)
		cmd         UpdateIdentityCommand
		schema      SchemaValidator
		updateErr   error
		pushErr     error
		wantErr     bool
		errContains string
	}{
		{
			name: "success with traits",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:       identityID,
					OrgID:    orgID,
					SchemaID: "default",
					Traits:   json.RawMessage(`{"email":"old@example.com"}`),
				}
			},
			cmd: UpdateIdentityCommand{
				ID:     identityID,
				OrgID:  orgID,
				Traits: json.RawMessage(`{"email":"new@example.com"}`),
			},
			wantErr: false,
		},
		{
			name: "success without traits",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:       identityID,
					OrgID:    orgID,
					SchemaID: "default",
					Traits:   json.RawMessage(`{"email":"test@example.com"}`),
				}
			},
			cmd: UpdateIdentityCommand{
				ID:     identityID,
				OrgID:  orgID,
				Traits: nil,
			},
			wantErr: false,
		},
		{
			name: "identity not found",
			setup: func(repo *mockIdentityRepo) {
				// Don't add identity
			},
			cmd: UpdateIdentityCommand{
				ID:     identityID,
				OrgID:  orgID,
				Traits: json.RawMessage(`{"email":"test@example.com"}`),
			},
			wantErr:     true,
			errContains: "update identity fetch",
		},
		{
			name: "schema validation fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:       identityID,
					OrgID:    orgID,
					SchemaID: "default",
					Traits:   json.RawMessage(`{"email":"test@example.com"}`),
				}
			},
			cmd: UpdateIdentityCommand{
				ID:     identityID,
				OrgID:  orgID,
				Traits: json.RawMessage(`{"email":"invalid"}`),
			},
			schema: &mockSchemaValidator{
				validateErr: errors.New("validation failed"),
			},
			wantErr:     true,
			errContains: "traits validation",
		},
		{
			name: "update fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:       identityID,
					OrgID:    orgID,
					SchemaID: "default",
					Traits:   json.RawMessage(`{"email":"test@example.com"}`),
				}
			},
			cmd: UpdateIdentityCommand{
				ID:     identityID,
				OrgID:  orgID,
				Traits: json.RawMessage(`{"email":"new@example.com"}`),
			},
			updateErr:   errors.New("database error"),
			wantErr:     true,
			errContains: "update identity write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockIdentityRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}
			repo.updateErr = tt.updateErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, repo, nil)
			if tt.schema != nil {
				cmds = cmds.WithSchemaValidator(tt.schema)
			}

			result, err := cmds.UpdateIdentity(context.Background(), tt.cmd)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("expected result, got nil")
				return
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestDeactivateIdentity(t *testing.T) {
	orgID := uuid.New()
	identityID := uuid.New()

	tests := []struct {
		name        string
		setup       func(*mockIdentityRepo)
		updateErr   error
		pushErr     error
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
					State: identity.StateActive,
				}
			},
			wantErr: false,
		},
		{
			name: "identity not found",
			setup: func(repo *mockIdentityRepo) {
				// Don't add identity
			},
			wantErr:     true,
			errContains: "deactivate identity fetch",
		},
		{
			name: "update fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
					State: identity.StateActive,
				}
			},
			updateErr:   errors.New("database error"),
			wantErr:     true,
			errContains: "deactivate identity write",
		},
		{
			name: "event push fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
					State: identity.StateActive,
				}
			},
			pushErr:     errors.New("event store error"),
			wantErr:     true,
			errContains: "deactivate identity push event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockIdentityRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}
			repo.updateErr = tt.updateErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, repo, nil)
			result, err := cmds.DeactivateIdentity(context.Background(), identityID, orgID)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("expected result, got nil")
				return
			}

			if result.State != identity.StateInactive {
				t.Errorf("expected state Inactive, got %v", result.State)
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestReactivateIdentity(t *testing.T) {
	orgID := uuid.New()
	identityID := uuid.New()

	tests := []struct {
		name        string
		setup       func(*mockIdentityRepo)
		updateErr   error
		pushErr     error
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
					State: identity.StateInactive,
				}
			},
			wantErr: false,
		},
		{
			name: "identity not found",
			setup: func(repo *mockIdentityRepo) {
				// Don't add identity
			},
			wantErr:     true,
			errContains: "reactivate identity fetch",
		},
		{
			name: "update fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
					State: identity.StateInactive,
				}
			},
			updateErr:   errors.New("database error"),
			wantErr:     true,
			errContains: "reactivate identity write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockIdentityRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}
			repo.updateErr = tt.updateErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, repo, nil)
			result, err := cmds.ReactivateIdentity(context.Background(), identityID, orgID)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Error("expected result, got nil")
				return
			}

			if result.State != identity.StateActive {
				t.Errorf("expected state Active, got %v", result.State)
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestDeleteIdentity(t *testing.T) {
	orgID := uuid.New()
	identityID := uuid.New()

	tests := []struct {
		name        string
		setup       func(*mockIdentityRepo)
		deleteErr   error
		pushErr     error
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
				}
			},
			wantErr: false,
		},
		{
			name: "delete fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
				}
			},
			deleteErr:   errors.New("database error"),
			wantErr:     true,
			errContains: "delete identity write",
		},
		{
			name: "event push fails",
			setup: func(repo *mockIdentityRepo) {
				repo.identities[identityID] = &identity.Identity{
					ID:    identityID,
					OrgID: orgID,
				}
			},
			pushErr:     errors.New("event store error"),
			wantErr:     true,
			errContains: "delete identity push event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockIdentityRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}
			repo.deleteErr = tt.deleteErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, repo, nil)
			err := cmds.DeleteIdentity(context.Background(), identityID, orgID)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestActorFromContext(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		expected string
	}{
		{
			name:     "no actor in context",
			ctx:      context.Background(),
			expected: "system",
		},
		{
			name:     "actor in context",
			ctx:      WithActor(context.Background(), "user-123"),
			expected: "user-123",
		},
		{
			name:     "empty actor in context",
			ctx:      WithActor(context.Background(), ""),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := actorFromContext(tt.ctx)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
