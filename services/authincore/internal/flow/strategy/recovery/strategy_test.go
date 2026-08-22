package recovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow/strategy/recovery"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeStore struct {
	code       *recovery.RecoveryCode
	created    bool
	consumeErr error
	findID     uuid.UUID
	findErr    error
}

func (s *fakeStore) Create(_ context.Context, c *recovery.RecoveryCode) error {
	s.code = c
	s.created = true
	return nil
}
func (s *fakeStore) Consume(_ context.Context, _ uuid.UUID, _, _ string) (*recovery.RecoveryCode, error) {
	if s.consumeErr != nil {
		return nil, s.consumeErr
	}
	return s.code, nil
}
func (s *fakeStore) FindIdentityByEmail(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, error) {
	return s.findID, s.findErr
}

type fakeResetter struct{ stored bool }

func (r *fakeResetter) StorePasswordCredential(_ context.Context, _ uuid.UUID, _ string) error {
	r.stored = true
	return nil
}

type fakeHasher struct{}

func (h *fakeHasher) Hash(pw string) (string, error) { return "hashed:" + pw, nil }

type fakeCourier struct{ sent bool }

func (c *fakeCourier) Send(_ context.Context, _ interface{}) error {
	c.sent = true
	return nil
}

func newFlow(t *testing.T, state domflow.State) *domflow.Flow {
	t.Helper()
	f := domflow.New(domflow.TypeRecovery, uuid.New(), "")
	f.SetState(state)
	return f
}

// --- tests ---

func TestRecovery_Phase1_SendsCode(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{findID: identityID, code: &recovery.RecoveryCode{IdentityID: identityID}}
	strat := recovery.New(store, &fakeResetter{}, &fakeHasher{}, nil)

	f := newFlow(t, domflow.StateChooseMethod)
	body, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	_, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("phase 1 unexpected error: %v", err)
	}
	if !store.created {
		t.Error("expected recovery code to be stored")
	}
	if f.State != domflow.StateSentEmail {
		t.Errorf("expected StateSentEmail, got %v", f.State)
	}
}

func TestRecovery_Phase1_UnknownEmail_EnumerationSafe(t *testing.T) {
	store := &fakeStore{findErr: domainerr.New(domainerr.CodeNotFound, "not found")}
	strat := recovery.New(store, &fakeResetter{}, &fakeHasher{}, nil)

	f := newFlow(t, domflow.StateChooseMethod)
	body, _ := json.Marshal(map[string]string{"email": "ghost@example.com"})
	_, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("expected no error for unknown email (enumeration safe), got: %v", err)
	}
	if f.State != domflow.StateSentEmail {
		t.Errorf("expected StateSentEmail (same as known email), got %v", f.State)
	}
}

func TestRecovery_Phase1_MissingEmail_Error(t *testing.T) {
	strat := recovery.New(&fakeStore{}, &fakeResetter{}, &fakeHasher{}, nil)
	f := newFlow(t, domflow.StateChooseMethod)
	_, err := strat.Handle(context.Background(), f, json.RawMessage(`{}`))
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument for missing email, got %v", err)
	}
}

func TestRecovery_Phase2_ValidCode_ResetsPassword(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{code: &recovery.RecoveryCode{IdentityID: identityID}}
	resetter := &fakeResetter{}
	strat := recovery.New(store, resetter, &fakeHasher{}, nil)

	f := newFlow(t, domflow.StateSentEmail)
	ctxData, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	f.InternalContext = ctxData

	body, _ := json.Marshal(map[string]string{"code": "123456", "new_password": "newpass123"})
	gotID, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("phase 2 unexpected error: %v", err)
	}
	if gotID == nil || *gotID != identityID {
		t.Errorf("expected identityID %s, got %v", identityID, gotID)
	}
	if !resetter.stored {
		t.Error("expected new password credential to be stored")
	}
}

func TestRecovery_Phase2_InvalidCode_Error(t *testing.T) {
	store := &fakeStore{consumeErr: domainerr.New(domainerr.CodeUnauthenticated, "invalid code")}
	strat := recovery.New(store, &fakeResetter{}, &fakeHasher{}, nil)

	f := newFlow(t, domflow.StateSentEmail)
	ctxData, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	f.InternalContext = ctxData

	body, _ := json.Marshal(map[string]string{"code": "000000", "new_password": "x"})
	_, err := strat.Handle(context.Background(), f, body)
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeUnauthenticated {
		t.Errorf("expected CodeUnauthenticated for invalid code, got %v", err)
	}
}

func TestRecovery_Phase2_MissingFields_Error(t *testing.T) {
	strat := recovery.New(&fakeStore{}, &fakeResetter{}, &fakeHasher{}, nil)
	f := newFlow(t, domflow.StateSentEmail)
	body, _ := json.Marshal(map[string]string{"code": "123456"}) // missing new_password
	_, err := strat.Handle(context.Background(), f, body)
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %v", err)
	}
}
