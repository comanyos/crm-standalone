package password

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/authincore/authincore/internal/account/lockout"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockHasher struct {
	hashFn   func(password string) (string, error)
	verifyFn func(password, hash string) (bool, error)
}

func (m *mockHasher) Hash(password string) (string, error) {
	if m.hashFn != nil {
		return m.hashFn(password)
	}
	return "hashed:" + password, nil
}

func (m *mockHasher) Verify(password, hash string) (bool, error) {
	if m.verifyFn != nil {
		return m.verifyFn(password, hash)
	}
	return password == hash, nil
}

type mockCredentialStore struct {
	findFn  func(ctx context.Context, orgID uuid.UUID, identifier string) (uuid.UUID, string, error)
	storeFn func(ctx context.Context, identityID uuid.UUID, hash string) error
	addIDFn func(ctx context.Context, identityID uuid.UUID, identifier string) error
}

func (m *mockCredentialStore) FindIdentityByIdentifier(ctx context.Context, orgID uuid.UUID, identifier string) (uuid.UUID, string, error) {
	if m.findFn != nil {
		return m.findFn(ctx, orgID, identifier)
	}
	return uuid.Nil, "", errors.New("not found")
}

func (m *mockCredentialStore) StorePasswordCredential(ctx context.Context, identityID uuid.UUID, hash string) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, identityID, hash)
	}
	return nil
}

func (m *mockCredentialStore) AddIdentifier(ctx context.Context, identityID uuid.UUID, identifier string) error {
	if m.addIDFn != nil {
		return m.addIDFn(ctx, identityID, identifier)
	}
	return nil
}

type mockLockoutStore struct {
	isLockedFn     func(ctx context.Context, orgID, identityID uuid.UUID) (bool, time.Time, error)
	incrFailureFn  func(ctx context.Context, orgID, identityID uuid.UUID) (int, error)
	lockFn         func(ctx context.Context, orgID, identityID uuid.UUID, expiresAt time.Time) error
	unlockFn       func(ctx context.Context, orgID, identityID uuid.UUID) error
	resetFailureFn func(ctx context.Context, orgID, identityID uuid.UUID) error
}

func (m *mockLockoutStore) IsLocked(ctx context.Context, orgID, identityID uuid.UUID) (bool, time.Time, error) {
	if m.isLockedFn != nil {
		return m.isLockedFn(ctx, orgID, identityID)
	}
	return false, time.Time{}, nil
}

func (m *mockLockoutStore) IncrFailure(ctx context.Context, orgID, identityID uuid.UUID) (int, error) {
	if m.incrFailureFn != nil {
		return m.incrFailureFn(ctx, orgID, identityID)
	}
	return 1, nil
}

func (m *mockLockoutStore) Lock(ctx context.Context, orgID, identityID uuid.UUID, expiresAt time.Time) error {
	if m.lockFn != nil {
		return m.lockFn(ctx, orgID, identityID, expiresAt)
	}
	return nil
}

func (m *mockLockoutStore) Unlock(ctx context.Context, orgID, identityID uuid.UUID) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, orgID, identityID)
	}
	return nil
}

func (m *mockLockoutStore) ResetFailures(ctx context.Context, orgID, identityID uuid.UUID) error {
	if m.resetFailureFn != nil {
		return m.resetFailureFn(ctx, orgID, identityID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newLoginFlow() *domflow.Flow {
	return domflow.New(domflow.TypeLogin, uuid.New(), "")
}

func buildLoginBody(identifier, password string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{
		"identifier": identifier,
		"password":   password,
	})
	return b
}

func assertUIError(t *testing.T, f *domflow.Flow) {
	t.Helper()
	assert.NotEmpty(t, f.UI.Messages, "expected at least one UI error message")
}

func assertDomainCode(t *testing.T, err error, code domainerr.Code) {
	t.Helper()
	require.Error(t, err)
	var de *domainerr.Error
	require.True(t, errors.As(err, &de), "expected domain error, got %T: %v", err, err)
	assert.Equal(t, code, de.Code)
}

func extractNodeAttrNames(t *testing.T, nodes []domflow.UINode) []string {
	t.Helper()
	var names []string
	for _, n := range nodes {
		var attrs map[string]any
		if err := json.Unmarshal(n.Attributes, &attrs); err != nil {
			continue
		}
		if name, ok := attrs["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestStrategy_ID(t *testing.T) {
	s := New(&mockHasher{}, &mockCredentialStore{}, nil, nil)
	assert.Equal(t, "password", s.ID())
}

func TestStrategy_HandleLogin(t *testing.T) {
	t.Parallel()

	identityID := uuid.New()
	const correctHash = "correct-hash"

	successStore := &mockCredentialStore{
		findFn: func(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, string, error) {
			return identityID, correctHash, nil
		},
	}
	successHasher := &mockHasher{
		verifyFn: func(password, hash string) (bool, error) {
			return password == "correct" && hash == correctHash, nil
		},
	}
	lockedLockout := lockout.New(&mockLockoutStore{
		isLockedFn: func(_ context.Context, _, _ uuid.UUID) (bool, time.Time, error) {
			return true, time.Now().Add(10 * time.Minute), nil
		},
	})

	tests := []struct {
		name         string
		body         json.RawMessage
		hasher       Hasher
		store        CredentialStore
		lockoutSvc   *lockout.Service
		wantIDNotNil bool
		wantCode     domainerr.Code
		wantUIError  bool
	}{
		{
			name:         "valid credentials returns identity ID",
			body:         buildLoginBody("alice@example.com", "correct"),
			hasher:       successHasher,
			store:        successStore,
			wantIDNotNil: true,
		},
		{
			name:        "wrong password returns unauthenticated",
			body:        buildLoginBody("alice@example.com", "wrong"),
			hasher:      successHasher,
			store:       successStore,
			wantCode:    domainerr.CodeUnauthenticated,
			wantUIError: true,
		},
		{
			name:   "unknown identifier returns unauthenticated",
			body:   buildLoginBody("nobody@example.com", "pw"),
			hasher: successHasher,
			store: &mockCredentialStore{
				findFn: func(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, string, error) {
					return uuid.Nil, "", errors.New("not found")
				},
			},
			wantCode:    domainerr.CodeUnauthenticated,
			wantUIError: true,
		},
		{
			name:        "account locked returns unauthenticated",
			body:        buildLoginBody("alice@example.com", "correct"),
			hasher:      successHasher,
			store:       successStore,
			lockoutSvc:  lockedLockout,
			wantCode:    domainerr.CodeUnauthenticated,
			wantUIError: true,
		},
		{
			name:        "missing email field returns invalid argument",
			body:        buildLoginBody("", "somepassword"),
			hasher:      successHasher,
			store:       successStore,
			wantCode:    domainerr.CodeInvalidArgument,
			wantUIError: true,
		},
		{
			name:        "missing password field returns invalid argument",
			body:        buildLoginBody("alice@example.com", ""),
			hasher:      successHasher,
			store:       successStore,
			wantCode:    domainerr.CodeInvalidArgument,
			wantUIError: true,
		},
		{
			name:        "malformed JSON returns invalid argument",
			body:        json.RawMessage(`{not valid json`),
			hasher:      successHasher,
			store:       successStore,
			wantCode:    domainerr.CodeInvalidArgument,
			wantUIError: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := New(tc.hasher, tc.store, nil, tc.lockoutSvc)
			f := newLoginFlow()
			id, err := s.Handle(context.Background(), f, tc.body)

			if tc.wantIDNotNil {
				require.NoError(t, err)
				require.NotNil(t, id)
				assert.Equal(t, identityID, *id)
			} else {
				assertDomainCode(t, err, tc.wantCode)
				assert.Nil(t, id)
			}

			if tc.wantUIError {
				assertUIError(t, f)
			}
		})
	}
}

func TestStrategy_PopulateFlow_Login(t *testing.T) {
	s := New(&mockHasher{}, &mockCredentialStore{}, nil, nil)
	f := newLoginFlow()
	require.NoError(t, s.PopulateFlow(context.Background(), f))
	require.NotEmpty(t, f.UI.Nodes, "login flow should have UI nodes")

	names := extractNodeAttrNames(t, f.UI.Nodes)
	assert.Contains(t, names, "identifier")
	assert.Contains(t, names, "password")
	assert.NotContains(t, names, "traits.email", "login flow must not expose registration-only field")
}

func TestStrategy_PopulateFlow_Registration(t *testing.T) {
	s := New(&mockHasher{}, &mockCredentialStore{}, nil, nil)
	f := domflow.New(domflow.TypeRegistration, uuid.New(), "")
	require.NoError(t, s.PopulateFlow(context.Background(), f))
	require.NotEmpty(t, f.UI.Nodes)

	names := extractNodeAttrNames(t, f.UI.Nodes)
	assert.Contains(t, names, "traits.email", "registration flow must include traits.email")
}

func TestStrategy_Handle_UnsupportedFlowType(t *testing.T) {
	s := New(&mockHasher{}, &mockCredentialStore{}, nil, nil)
	f := domflow.New(domflow.TypeRecovery, uuid.New(), "")
	_, err := s.Handle(context.Background(), f, buildLoginBody("a@b.com", "pw"))
	assertDomainCode(t, err, domainerr.CodeInvalidArgument)
}

func TestStrategy_Login_LockoutFailureRecorded(t *testing.T) {
	identityID := uuid.New()
	failureRecorded := false

	ls := lockout.New(&mockLockoutStore{
		isLockedFn: func(_ context.Context, _, _ uuid.UUID) (bool, time.Time, error) {
			return false, time.Time{}, nil
		},
		incrFailureFn: func(_ context.Context, _, _ uuid.UUID) (int, error) {
			failureRecorded = true
			return 1, nil
		},
	})
	store := &mockCredentialStore{
		findFn: func(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, string, error) {
			return identityID, "correct", nil
		},
	}
	hasher := &mockHasher{
		verifyFn: func(_, _ string) (bool, error) { return false, nil },
	}

	s := New(hasher, store, nil, ls)
	f := newLoginFlow()
	_, err := s.Handle(context.Background(), f, buildLoginBody("alice@example.com", "bad"))
	require.Error(t, err)
	assert.True(t, failureRecorded, "expected lockout failure counter to be incremented")
}

func TestStrategy_Login_LockoutClearedOnSuccess(t *testing.T) {
	identityID := uuid.New()
	successRecorded := false

	ls := lockout.New(&mockLockoutStore{
		isLockedFn: func(_ context.Context, _, _ uuid.UUID) (bool, time.Time, error) {
			return false, time.Time{}, nil
		},
		resetFailureFn: func(_ context.Context, _, _ uuid.UUID) error {
			successRecorded = true
			return nil
		},
	})
	store := &mockCredentialStore{
		findFn: func(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, string, error) {
			return identityID, "correct", nil
		},
	}
	hasher := &mockHasher{
		verifyFn: func(_, _ string) (bool, error) { return true, nil },
	}

	s := New(hasher, store, nil, ls)
	f := newLoginFlow()
	id, err := s.Handle(context.Background(), f, buildLoginBody("alice@example.com", "correct"))
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.True(t, successRecorded, "expected lockout counters to be reset on successful login")
}
