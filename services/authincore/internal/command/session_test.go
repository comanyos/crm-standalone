package command

import (
	"context"
	"errors"
	"testing"
	"time"

	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/google/uuid"
)

// mockSessionRepo implements SessionRepository for testing
type mockSessionRepo struct {
	sessions             map[uuid.UUID]*domsession.Session
	insertErr            error
	updateStateErr       error
	updateExpiryErr      error
	revokeAllErr         error
	revokeAllReturnCount int64
}

func newMockSessionRepo() *mockSessionRepo {
	return &mockSessionRepo{
		sessions: make(map[uuid.UUID]*domsession.Session),
	}
}

func (m *mockSessionRepo) Insert(ctx context.Context, s *domsession.Session) error {
	if m.insertErr != nil {
		return m.insertErr
	}
	m.sessions[s.ID] = s
	return nil
}

func (m *mockSessionRepo) UpdateState(ctx context.Context, id, orgID uuid.UUID, state domsession.State) error {
	if m.updateStateErr != nil {
		return m.updateStateErr
	}
	if s, ok := m.sessions[id]; ok && s.OrgID == orgID {
		s.State = state
	}
	return nil
}

func (m *mockSessionRepo) UpdateExpiry(ctx context.Context, id, orgID uuid.UUID, expiresAt time.Time) error {
	if m.updateExpiryErr != nil {
		return m.updateExpiryErr
	}
	if s, ok := m.sessions[id]; ok && s.OrgID == orgID {
		s.ExpiresAt = expiresAt
	}
	return nil
}

func (m *mockSessionRepo) RevokeAllByIdentity(ctx context.Context, identityID, orgID uuid.UUID) (int64, error) {
	if m.revokeAllErr != nil {
		return 0, m.revokeAllErr
	}
	return m.revokeAllReturnCount, nil
}

func TestCreateSession(t *testing.T) {
	tests := []struct {
		name        string
		cmd         CreateSessionCommand
		insertErr   error
		pushErr     error
		wantErr     bool
		errContains string
		wantTTL     time.Duration
	}{
		{
			name: "success with custom TTL",
			cmd: CreateSessionCommand{
				IdentityID: uuid.New(),
				OrgID:      uuid.New(),
				Methods: []domsession.AuthMethod{
					{Method: "password", CompletedAt: time.Now()},
				},
				TTL: 2 * time.Hour,
			},
			wantErr: false,
			wantTTL: 2 * time.Hour,
		},
		{
			name: "success with default TTL",
			cmd: CreateSessionCommand{
				IdentityID: uuid.New(),
				OrgID:      uuid.New(),
				Methods: []domsession.AuthMethod{
					{Method: "password", CompletedAt: time.Now()},
				},
				TTL: 0, // Should use default
			},
			wantErr: false,
			wantTTL: defaultSessionTTL,
		},
		{
			name: "success with multiple auth methods",
			cmd: CreateSessionCommand{
				IdentityID: uuid.New(),
				OrgID:      uuid.New(),
				Methods: []domsession.AuthMethod{
					{Method: "password", CompletedAt: time.Now()},
					{Method: "totp", CompletedAt: time.Now()},
				},
				TTL: 1 * time.Hour,
			},
			wantErr: false,
			wantTTL: 1 * time.Hour,
		},
		{
			name: "insert fails",
			cmd: CreateSessionCommand{
				IdentityID: uuid.New(),
				OrgID:      uuid.New(),
				Methods: []domsession.AuthMethod{
					{Method: "password", CompletedAt: time.Now()},
				},
				TTL: 1 * time.Hour,
			},
			insertErr:   errors.New("database error"),
			wantErr:     true,
			errContains: "create session insert",
		},
		{
			name: "event push fails",
			cmd: CreateSessionCommand{
				IdentityID: uuid.New(),
				OrgID:      uuid.New(),
				Methods: []domsession.AuthMethod{
					{Method: "password", CompletedAt: time.Now()},
				},
				TTL: 1 * time.Hour,
			},
			pushErr:     errors.New("event store error"),
			wantErr:     true,
			errContains: "create session push event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockSessionRepo()
			repo.insertErr = tt.insertErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, nil, repo)
			result, err := cmds.CreateSession(context.Background(), tt.cmd)

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

			if result.IdentityID != tt.cmd.IdentityID {
				t.Errorf("expected IdentityID %v, got %v", tt.cmd.IdentityID, result.IdentityID)
			}

			if result.OrgID != tt.cmd.OrgID {
				t.Errorf("expected OrgID %v, got %v", tt.cmd.OrgID, result.OrgID)
			}

			// Check TTL was applied correctly
			expectedExpiry := result.IssuedAt.Add(tt.wantTTL)
			if !result.ExpiresAt.Equal(expectedExpiry) {
				t.Errorf("expected ExpiresAt %v, got %v", expectedExpiry, result.ExpiresAt)
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestRevokeSession(t *testing.T) {
	sessionID := uuid.New()
	orgID := uuid.New()

	tests := []struct {
		name           string
		setup          func(*mockSessionRepo)
		updateStateErr error
		pushErr        error
		wantErr        bool
		errContains    string
	}{
		{
			name: "success",
			setup: func(repo *mockSessionRepo) {
				repo.sessions[sessionID] = &domsession.Session{
					ID:    sessionID,
					OrgID: orgID,
					State: domsession.StateActive,
				}
			},
			wantErr: false,
		},
		{
			name: "update state fails",
			setup: func(repo *mockSessionRepo) {
				repo.sessions[sessionID] = &domsession.Session{
					ID:    sessionID,
					OrgID: orgID,
					State: domsession.StateActive,
				}
			},
			updateStateErr: errors.New("database error"),
			wantErr:        true,
			errContains:    "revoke session write",
		},
		{
			name: "event push fails",
			setup: func(repo *mockSessionRepo) {
				repo.sessions[sessionID] = &domsession.Session{
					ID:    sessionID,
					OrgID: orgID,
					State: domsession.StateActive,
				}
			},
			pushErr:     errors.New("event store error"),
			wantErr:     true,
			errContains: "revoke session push event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockSessionRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}
			repo.updateStateErr = tt.updateStateErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, nil, repo)
			err := cmds.RevokeSession(context.Background(), sessionID, orgID)

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

			// Verify session state was updated
			if s, ok := repo.sessions[sessionID]; ok {
				if s.State != domsession.StateInactive {
					t.Errorf("expected state Inactive, got %v", s.State)
				}
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestRevokeAllSessions(t *testing.T) {
	identityID := uuid.New()
	orgID := uuid.New()

	tests := []struct {
		name                 string
		revokeAllReturnCount int64
		revokeAllErr         error
		pushErr              error
		wantErr              bool
		errContains          string
		wantCount            int64
		wantEventsPushed     int
	}{
		{
			name:                 "success with sessions revoked",
			revokeAllReturnCount: 3,
			wantErr:              false,
			wantCount:            3,
			wantEventsPushed:     1,
		},
		{
			name:                 "success with no sessions",
			revokeAllReturnCount: 0,
			wantErr:              false,
			wantCount:            0,
			wantEventsPushed:     0, // No event pushed when count is 0
		},
		{
			name:         "revoke all fails",
			revokeAllErr: errors.New("database error"),
			wantErr:      true,
			errContains:  "revoke all sessions",
		},
		{
			name:                 "event push fails",
			revokeAllReturnCount: 2,
			pushErr:              errors.New("event store error"),
			wantErr:              true,
			errContains:          "revoke all sessions push event",
			wantCount:            2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockSessionRepo()
			repo.revokeAllReturnCount = tt.revokeAllReturnCount
			repo.revokeAllErr = tt.revokeAllErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, nil, repo)
			count, err := cmds.RevokeAllSessions(context.Background(), identityID, orgID)

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

			if count != tt.wantCount {
				t.Errorf("expected count %d, got %d", tt.wantCount, count)
			}

			if es.pushed != tt.wantEventsPushed {
				t.Errorf("expected %d events pushed, got %d", tt.wantEventsPushed, es.pushed)
			}
		})
	}
}

func TestExtendSession(t *testing.T) {
	sessionID := uuid.New()
	orgID := uuid.New()

	tests := []struct {
		name            string
		setup           func(*mockSessionRepo)
		ttl             time.Duration
		updateExpiryErr error
		pushErr         error
		wantErr         bool
		errContains     string
	}{
		{
			name: "success",
			setup: func(repo *mockSessionRepo) {
				repo.sessions[sessionID] = &domsession.Session{
					ID:        sessionID,
					OrgID:     orgID,
					ExpiresAt: time.Now().Add(1 * time.Hour),
				}
			},
			ttl:     2 * time.Hour,
			wantErr: false,
		},
		{
			name: "update expiry fails",
			setup: func(repo *mockSessionRepo) {
				repo.sessions[sessionID] = &domsession.Session{
					ID:        sessionID,
					OrgID:     orgID,
					ExpiresAt: time.Now().Add(1 * time.Hour),
				}
			},
			ttl:             2 * time.Hour,
			updateExpiryErr: errors.New("database error"),
			wantErr:         true,
			errContains:     "extend session write",
		},
		{
			name: "event push fails",
			setup: func(repo *mockSessionRepo) {
				repo.sessions[sessionID] = &domsession.Session{
					ID:        sessionID,
					OrgID:     orgID,
					ExpiresAt: time.Now().Add(1 * time.Hour),
				}
			},
			ttl:         2 * time.Hour,
			pushErr:     errors.New("event store error"),
			wantErr:     true,
			errContains: "extend session push event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockSessionRepo()
			if tt.setup != nil {
				tt.setup(repo)
			}
			repo.updateExpiryErr = tt.updateExpiryErr
			es := newMockEventstore()
			es.pushErr = tt.pushErr

			cmds := New(nil, es, nil, repo)
			err := cmds.ExtendSession(context.Background(), sessionID, orgID, tt.ttl)

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

			// Verify session expiry was updated
			if s, ok := repo.sessions[sessionID]; ok {
				// Check that expiry is approximately now + ttl (within 1 second tolerance)
				expectedExpiry := time.Now().Add(tt.ttl)
				diff := s.ExpiresAt.Sub(expectedExpiry)
				if diff < -time.Second || diff > time.Second {
					t.Errorf("expected ExpiresAt around %v, got %v", expectedExpiry, s.ExpiresAt)
				}
			}

			if es.pushed != 1 {
				t.Errorf("expected 1 event pushed, got %d", es.pushed)
			}
		})
	}
}

func TestDefaultSessionTTL(t *testing.T) {
	if defaultSessionTTL != 24*time.Hour {
		t.Errorf("expected defaultSessionTTL to be 24 hours, got %v", defaultSessionTTL)
	}
}
