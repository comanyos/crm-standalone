package invitation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/invitation"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeStore struct {
	inv       map[string]*invitation.Invitation
	insertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{inv: make(map[string]*invitation.Invitation)}
}

func (f *fakeStore) Insert(_ context.Context, inv *invitation.Invitation) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inv[inv.Token] = inv
	return nil
}
func (f *fakeStore) ByToken(_ context.Context, token string) (*invitation.Invitation, error) {
	inv, ok := f.inv[token]
	if !ok {
		return nil, domainerr.New(domainerr.CodeNotFound, "not found")
	}
	return inv, nil
}
func (f *fakeStore) Accept(_ context.Context, token string, identityID uuid.UUID) error {
	inv, ok := f.inv[token]
	if !ok {
		return domainerr.New(domainerr.CodeNotFound, "not found")
	}
	if inv.AcceptedAt != nil {
		return domainerr.New(domainerr.CodeAlreadyExists, "already accepted")
	}
	now := time.Now().UTC()
	inv.AcceptedAt = &now
	inv.IdentityID = &identityID
	return nil
}
func (f *fakeStore) ListByOrg(_ context.Context, orgID uuid.UUID) ([]*invitation.Invitation, error) {
	var out []*invitation.Invitation
	for _, inv := range f.inv {
		if inv.OrgID == orgID {
			out = append(out, inv)
		}
	}
	return out, nil
}
func (f *fakeStore) Delete(_ context.Context, _, id uuid.UUID) error {
	for tok, inv := range f.inv {
		if inv.ID == id {
			delete(f.inv, tok)
			return nil
		}
	}
	return nil
}

type fakeRoles struct{ called bool }

func (f *fakeRoles) AssignRole(_ context.Context, _, _ uuid.UUID, _ string) error {
	f.called = true
	return nil
}

type fakeResolver struct{ id uuid.UUID }

func (f *fakeResolver) FindOrCreateByEmail(_ context.Context, _ uuid.UUID, _ string) (uuid.UUID, error) {
	return f.id, nil
}

// --- tests ---

func TestCreate_StoresAndReturnsInvitation(t *testing.T) {
	store := newFakeStore()
	roles := &fakeRoles{}
	resolver := &fakeResolver{id: uuid.New()}
	svc := invitation.New(store, roles, resolver, nil, "http://localhost")

	orgID := uuid.New()
	inv, err := svc.Create(context.Background(), orgID, uuid.New(), "new@example.com", "admin")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.Token == "" {
		t.Error("expected non-empty token")
	}
	if inv.Email != "new@example.com" {
		t.Errorf("email mismatch: %q", inv.Email)
	}
	if inv.Role != "admin" {
		t.Errorf("role mismatch: %q", inv.Role)
	}
	if inv.ExpiresAt.Before(time.Now()) {
		t.Error("expected future expiry")
	}
}

func TestAccept_AssignsRoleAndMarksAccepted(t *testing.T) {
	store := newFakeStore()
	roles := &fakeRoles{}
	identityID := uuid.New()
	resolver := &fakeResolver{id: identityID}
	svc := invitation.New(store, roles, resolver, nil, "http://localhost")

	orgID := uuid.New()
	inv, _ := svc.Create(context.Background(), orgID, uuid.New(), "user@example.com", "member")

	accepted, err := svc.Accept(context.Background(), inv.Token, "user@example.com")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.AcceptedAt == nil {
		t.Error("expected AcceptedAt to be set")
	}
	if accepted.IdentityID == nil || *accepted.IdentityID != identityID {
		t.Errorf("expected identity %s, got %v", identityID, accepted.IdentityID)
	}
	if !roles.called {
		t.Error("expected role to be assigned")
	}
}

func TestAccept_ExpiredInvitation(t *testing.T) {
	store := newFakeStore()
	svc := invitation.New(store, &fakeRoles{}, &fakeResolver{id: uuid.New()}, nil, "http://localhost")

	orgID := uuid.New()
	inv, _ := svc.Create(context.Background(), orgID, uuid.New(), "user@example.com", "member")
	// Manually expire it.
	inv.ExpiresAt = time.Now().Add(-time.Hour)

	_, err := svc.Accept(context.Background(), inv.Token, "user@example.com")
	if err == nil {
		t.Fatal("expected error on expired invitation")
	}
	if !domainerr.IsInvalidArgument(err) {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestAccept_EmailMustMatchInvitation(t *testing.T) {
	store := newFakeStore()
	roles := &fakeRoles{}
	svc := invitation.New(store, roles, &fakeResolver{id: uuid.New()}, nil, "http://localhost")
	inv, err := svc.Create(context.Background(), uuid.New(), uuid.New(), "invited@example.com", "member")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Accept(context.Background(), inv.Token, "attacker@example.com"); !domainerr.IsInvalidArgument(err) {
		t.Fatalf("expected email mismatch rejection, got %v", err)
	}
	if roles.called {
		t.Fatal("role must not be assigned for a mismatched email")
	}
}

func TestAccept_AlreadyAccepted(t *testing.T) {
	store := newFakeStore()
	resolver := &fakeResolver{id: uuid.New()}
	svc := invitation.New(store, &fakeRoles{}, resolver, nil, "http://localhost")

	orgID := uuid.New()
	inv, _ := svc.Create(context.Background(), orgID, uuid.New(), "user@example.com", "member")
	_, _ = svc.Accept(context.Background(), inv.Token, "user@example.com")

	_, err := svc.Accept(context.Background(), inv.Token, "user@example.com")
	if err == nil {
		t.Fatal("expected error on double accept")
	}
	if !domainerr.IsAlreadyExists(err) {
		t.Errorf("expected AlreadyExists, got %v", err)
	}
}

func TestAccept_InvalidToken(t *testing.T) {
	store := newFakeStore()
	svc := invitation.New(store, &fakeRoles{}, &fakeResolver{id: uuid.New()}, nil, "http://localhost")

	_, err := svc.Accept(context.Background(), "nonexistent-token", "user@example.com")
	if err == nil {
		t.Fatal("expected error on invalid token")
	}
	if !domainerr.IsNotFound(err) {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestCreate_StoreError(t *testing.T) {
	store := newFakeStore()
	store.insertErr = errors.New("db down")
	svc := invitation.New(store, &fakeRoles{}, &fakeResolver{id: uuid.New()}, nil, "http://localhost")

	_, err := svc.Create(context.Background(), uuid.New(), uuid.New(), "user@example.com", "member")
	if err == nil {
		t.Fatal("expected error when store fails")
	}
}
