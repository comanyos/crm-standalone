package magiclink_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow/strategy/magiclink"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeStore struct {
	createFn  func(*magiclink.LoginCode) error
	consumeFn func(orgID uuid.UUID, email, code string) (*magiclink.LoginCode, error)
}

func (f *fakeStore) Create(_ context.Context, lc *magiclink.LoginCode) error {
	if f.createFn != nil {
		return f.createFn(lc)
	}
	return nil
}
func (f *fakeStore) Consume(_ context.Context, orgID uuid.UUID, email, code string) (*magiclink.LoginCode, error) {
	if f.consumeFn != nil {
		return f.consumeFn(orgID, email, code)
	}
	return nil, errors.New("not implemented")
}

type fakeResolver struct {
	fn func(orgID uuid.UUID, email string) (uuid.UUID, error)
}

func (f *fakeResolver) FindOrCreateByEmail(_ context.Context, orgID uuid.UUID, email string) (uuid.UUID, error) {
	return f.fn(orgID, email)
}

func loginFlow() *domflow.Flow { return domflow.New(domflow.TypeLogin, uuid.New(), "") }

// --- tests ---

func TestHandle_Phase1_SendsCode(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{}
	resolver := &fakeResolver{fn: func(_ uuid.UUID, _ string) (uuid.UUID, error) { return identityID, nil }}

	s := magiclink.New(store, resolver, nil)
	f := loginFlow()
	body, _ := json.Marshal(map[string]string{"email": "user@example.com"})

	got, err := s.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil identity on phase 1")
	}
	if f.State != domflow.StateSentEmail {
		t.Errorf("expected StateSentEmail, got %v", f.State)
	}
	if len(f.UI.Messages) == 0 {
		t.Error("expected info message after sending code")
	}
}

func TestHandle_Phase1_UnknownEmail_SameResponse(t *testing.T) {
	store := &fakeStore{}
	resolver := &fakeResolver{fn: func(_ uuid.UUID, _ string) (uuid.UUID, error) {
		return uuid.Nil, errors.New("not found")
	}}
	s := magiclink.New(store, resolver, nil)
	f := loginFlow()
	body, _ := json.Marshal(map[string]string{"email": "nobody@example.com"})

	got, err := s.Handle(context.Background(), f, body)
	// Must not reveal that the email doesn't exist.
	if err != nil {
		t.Fatalf("unexpected error (enumeration leak): %v", err)
	}
	if got != nil {
		t.Error("expected nil identity")
	}
	if len(f.UI.Messages) == 0 {
		t.Error("expected info message even for unknown email")
	}
}

func TestHandle_Phase2_ValidCode(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{
		consumeFn: func(_ uuid.UUID, _, _ string) (*magiclink.LoginCode, error) {
			return &magiclink.LoginCode{IdentityID: identityID}, nil
		},
	}
	s := magiclink.New(store, nil, nil)
	f := loginFlow()

	// Set internal context to phase 2.
	ic, _ := json.Marshal(map[string]any{"phase": 2, "email": "user@example.com"})
	f.InternalContext = ic

	body, _ := json.Marshal(map[string]string{"code": "123456"})
	got, err := s.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || *got != identityID {
		t.Errorf("expected identity %s, got %v", identityID, got)
	}
}

func TestHandle_Phase2_InvalidCode(t *testing.T) {
	store := &fakeStore{
		consumeFn: func(_ uuid.UUID, _, _ string) (*magiclink.LoginCode, error) {
			return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid")
		},
	}
	s := magiclink.New(store, nil, nil)
	f := loginFlow()
	ic, _ := json.Marshal(map[string]any{"phase": 2, "email": "user@example.com"})
	f.InternalContext = ic

	body, _ := json.Marshal(map[string]string{"code": "000000"})
	got, err := s.Handle(context.Background(), f, body)
	if got != nil {
		t.Error("expected nil identity on invalid code")
	}
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeUnauthenticated {
		t.Errorf("expected CodeUnauthenticated, got %v", err)
	}
}

func TestHandle_Phase1_MissingEmail(t *testing.T) {
	s := magiclink.New(&fakeStore{}, &fakeResolver{fn: func(_ uuid.UUID, _ string) (uuid.UUID, error) {
		return uuid.Nil, nil
	}}, nil)
	f := loginFlow()
	body, _ := json.Marshal(map[string]string{})
	_, err := s.Handle(context.Background(), f, body)
	if err == nil {
		t.Error("expected error for missing email")
	}
}

func TestLoginCode_ExpiryFields(t *testing.T) {
	lc := &magiclink.LoginCode{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		Email:     "test@example.com",
		Code:      "123456",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	if lc.ExpiresAt.Before(time.Now()) {
		t.Error("expected future expiry")
	}
}
