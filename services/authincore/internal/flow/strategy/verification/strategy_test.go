package verification_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow/strategy/verification"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeStore struct {
	code       *verification.VerificationCode
	consumeErr error
	findID     uuid.UUID
	findErr    error
	marked     bool
}

func (s *fakeStore) Create(_ context.Context, c *verification.VerificationCode) error {
	s.code = c
	return nil
}
func (s *fakeStore) Consume(_ context.Context, _ uuid.UUID, _, _ string) (*verification.VerificationCode, error) {
	if s.consumeErr != nil {
		return nil, s.consumeErr
	}
	return s.code, nil
}
func (s *fakeStore) MarkVerified(_ context.Context, _ uuid.UUID, _ string) error {
	s.marked = true
	return nil
}
func (s *fakeStore) FindIdentityByEmail(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, error) {
	return s.findID, s.findErr
}

func newFlow(t *testing.T, state domflow.State) *domflow.Flow {
	t.Helper()
	f := domflow.New(domflow.TypeVerification, uuid.New(), "")
	f.SetState(state)
	return f
}

// --- tests ---

func TestVerification_Phase1_SendsCode(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{
		findID: identityID,
		code:   &verification.VerificationCode{IdentityID: identityID},
	}
	strat := verification.New(store, store, nil)

	f := newFlow(t, domflow.StateChooseMethod)
	body, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	_, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.code == nil {
		t.Error("expected verification code to be created")
	}
	if f.State != domflow.StateSentEmail {
		t.Errorf("expected StateSentEmail, got %v", f.State)
	}
}

func TestVerification_Phase1_UnknownEmail_EnumerationSafe(t *testing.T) {
	store := &fakeStore{findErr: domainerr.New(domainerr.CodeNotFound, "not found")}
	strat := verification.New(store, store, nil)

	f := newFlow(t, domflow.StateChooseMethod)
	body, _ := json.Marshal(map[string]string{"email": "ghost@example.com"})
	_, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("expected no error for unknown email (enumeration-safe), got: %v", err)
	}
	if f.State != domflow.StateSentEmail {
		t.Errorf("expected StateSentEmail even for unknown email, got %v", f.State)
	}
}

func TestVerification_Phase1_MissingEmail_Error(t *testing.T) {
	strat := verification.New(&fakeStore{}, &fakeStore{}, nil)
	f := newFlow(t, domflow.StateChooseMethod)
	_, err := strat.Handle(context.Background(), f, json.RawMessage(`{}`))
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %v", err)
	}
}

func TestVerification_Phase2_ValidCode_MarksVerified(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{code: &verification.VerificationCode{IdentityID: identityID}}
	strat := verification.New(store, store, nil)

	f := newFlow(t, domflow.StateSentEmail)
	ctxData, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	f.InternalContext = ctxData

	body, _ := json.Marshal(map[string]string{"code": "123456"})
	gotID, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotID == nil || *gotID != identityID {
		t.Errorf("expected identityID %s, got %v", identityID, gotID)
	}
	if !store.marked {
		t.Error("expected MarkVerified to be called")
	}
}

func TestVerification_Phase2_InvalidCode_Error(t *testing.T) {
	store := &fakeStore{consumeErr: domainerr.New(domainerr.CodeUnauthenticated, "invalid")}
	strat := verification.New(store, store, nil)

	f := newFlow(t, domflow.StateSentEmail)
	ctxData, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	f.InternalContext = ctxData

	body, _ := json.Marshal(map[string]string{"code": "000000"})
	_, err := strat.Handle(context.Background(), f, body)
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeUnauthenticated {
		t.Errorf("expected CodeUnauthenticated, got %v", err)
	}
}

func TestVerification_Phase2_MissingCode_Error(t *testing.T) {
	strat := verification.New(&fakeStore{}, &fakeStore{}, nil)
	f := newFlow(t, domflow.StateSentEmail)
	ctxData, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	f.InternalContext = ctxData
	_, err := strat.Handle(context.Background(), f, json.RawMessage(`{}`))
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %v", err)
	}
}
