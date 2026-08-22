package serviceaccount_test

import (
	"context"
	"errors"
	"testing"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/serviceaccount"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeStore struct {
	accounts map[string]*serviceaccount.ServiceAccount
}

func newFakeStore() *fakeStore {
	return &fakeStore{accounts: make(map[string]*serviceaccount.ServiceAccount)}
}

func (f *fakeStore) Insert(_ context.Context, sa *serviceaccount.ServiceAccount) error {
	f.accounts[sa.ClientID] = sa
	return nil
}
func (f *fakeStore) ByClientID(_ context.Context, orgID uuid.UUID, clientID string) (*serviceaccount.ServiceAccount, error) {
	sa, ok := f.accounts[clientID]
	if !ok || sa.OrgID != orgID {
		return nil, domainerr.New(domainerr.CodeNotFound, "not found")
	}
	return sa, nil
}
func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]*serviceaccount.ServiceAccount, error) {
	var out []*serviceaccount.ServiceAccount
	for _, sa := range f.accounts {
		if sa.OrgID == orgID {
			out = append(out, sa)
		}
	}
	return out, nil
}
func (f *fakeStore) Delete(_ context.Context, _, id uuid.UUID) error {
	for k, sa := range f.accounts {
		if sa.ID == id {
			delete(f.accounts, k)
		}
	}
	return nil
}

func realSigner(t *testing.T) *token.RS256Signer {
	t.Helper()
	s, err := token.NewRS256Signer(nil, "test")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return s
}

// --- tests ---

func TestCreate_GeneratesClientIDAndSecret(t *testing.T) {
	store := newFakeStore()
	signer := realSigner(t)
	svc := serviceaccount.New(store, signer, "https://test", 60*time.Minute)

	result, err := svc.Create(context.Background(), uuid.New(), "my-service", []string{"openid"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.ClientSecret == "" {
		t.Error("expected non-empty client secret")
	}
	if result.ServiceAccount.ClientID == "" {
		t.Error("expected non-empty client ID")
	}
	if result.ServiceAccount.SecretHash == "" {
		t.Error("expected secret to be hashed")
	}
	if result.ServiceAccount.SecretHash == result.ClientSecret {
		t.Error("secret must be hashed, not stored in plain text")
	}
}

func TestIssueToken_ValidCredentials(t *testing.T) {
	store := newFakeStore()
	signer := realSigner(t)
	svc := serviceaccount.New(store, signer, "https://test", 60*time.Minute)

	orgID := uuid.New()
	result, _ := svc.Create(context.Background(), orgID, "worker", []string{"openid"})

	tok, err := svc.IssueToken(context.Background(), orgID, result.ServiceAccount.ClientID, result.ClientSecret)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token")
	}
	claims, err := signer.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != "openid" || claims.Scope != "openid" {
		t.Fatalf("expected issued scopes in token, got scopes=%v scope=%q", claims.Scopes, claims.Scope)
	}
}

func TestIssueToken_CrossOrgRejected(t *testing.T) {
	store := newFakeStore()
	signer := realSigner(t)
	svc := serviceaccount.New(store, signer, "https://test", 60*time.Minute)
	result, err := svc.Create(context.Background(), uuid.New(), "worker", []string{"jobs:write"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.IssueToken(context.Background(), uuid.New(), result.ServiceAccount.ClientID, result.ClientSecret); !domainerr.IsUnauthenticated(err) {
		t.Fatalf("expected cross-org credentials to be rejected, got %v", err)
	}
}

func TestIssueToken_WrongSecret(t *testing.T) {
	store := newFakeStore()
	svc := serviceaccount.New(store, realSigner(t), "https://test", 60*time.Minute)

	orgID := uuid.New()
	result, _ := svc.Create(context.Background(), orgID, "worker", nil)

	_, err := svc.IssueToken(context.Background(), orgID, result.ServiceAccount.ClientID, "wrong-secret")
	if err == nil {
		t.Fatal("expected error on wrong secret")
	}
	if !domainerr.IsUnauthenticated(err) {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestIssueToken_UnknownClientID(t *testing.T) {
	store := newFakeStore()
	svc := serviceaccount.New(store, realSigner(t), "https://test", 60*time.Minute)

	_, err := svc.IssueToken(context.Background(), uuid.New(), "sa_nonexistent", "secret")
	if err == nil {
		t.Fatal("expected error on unknown client ID")
	}
	if !domainerr.IsUnauthenticated(err) {
		t.Errorf("expected Unauthenticated, got %v", err)
	}
}

func TestIssueToken_InactiveAccount(t *testing.T) {
	store := newFakeStore()
	svc := serviceaccount.New(store, realSigner(t), "https://test", 60*time.Minute)

	orgID := uuid.New()
	result, _ := svc.Create(context.Background(), orgID, "worker", nil)
	// Deactivate manually.
	result.ServiceAccount.Active = false

	_, err := svc.IssueToken(context.Background(), orgID, result.ServiceAccount.ClientID, result.ClientSecret)
	if err == nil {
		t.Fatal("expected error for inactive account")
	}
	var de *domainerr.Error
	if !errors.As(err, &de) {
		t.Errorf("expected domain error, got %v", err)
	}
}
