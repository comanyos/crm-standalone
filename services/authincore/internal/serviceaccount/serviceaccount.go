// Package serviceaccount manages machine/service-account identities that
// authenticate via client_credentials (OAuth2 RFC 6749 §4.4).
package serviceaccount

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// ServiceAccount represents a machine identity.
type ServiceAccount struct {
	ID         uuid.UUID `json:"id"`
	OrgID      uuid.UUID `json:"org_id"`
	Name       string    `json:"name"`
	ClientID   string    `json:"client_id"`
	SecretHash string    `json:"-"`
	Scopes     []string  `json:"scopes"`
	Active     bool      `json:"active"`
	CreatedAt  time.Time `json:"created_at"`
}

// Store persists service accounts.
type Store interface {
	Insert(ctx context.Context, sa *ServiceAccount) error
	ByClientID(ctx context.Context, orgID uuid.UUID, clientID string) (*ServiceAccount, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*ServiceAccount, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// TokenSigner signs tokens for service accounts.
type TokenSigner interface {
	Sign(c token.Claims) (string, error)
}

// Service manages service account lifecycle and token issuance.
type Service struct {
	store  Store
	signer TokenSigner
	issuer string
	ttl    time.Duration
}

func New(store Store, signer TokenSigner, issuer string, ttl time.Duration) *Service {
	return &Service{store: store, signer: signer, issuer: issuer, ttl: ttl}
}

// CreateResult is returned when a service account is created (includes plain-text secret).
type CreateResult struct {
	ServiceAccount *ServiceAccount `json:"service_account"`
	ClientSecret   string          `json:"client_secret"`
}

// Create generates a new service account with a random client_id and client_secret.
// The plain-text secret is returned only once; the hash is stored.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, name string, scopes []string) (*CreateResult, error) {
	clientID := "sa_" + newToken(16)
	plainSecret := newToken(32)
	hash, err := bcrypt.GenerateFromPassword([]byte(plainSecret), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash secret: %w", err)
	}

	sa := &ServiceAccount{
		ID:         uuid.New(),
		OrgID:      orgID,
		Name:       name,
		ClientID:   clientID,
		SecretHash: string(hash),
		Scopes:     scopes,
		Active:     true,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.store.Insert(ctx, sa); err != nil {
		return nil, fmt.Errorf("insert service account: %w", err)
	}
	return &CreateResult{ServiceAccount: sa, ClientSecret: plainSecret}, nil
}

// IssueToken validates client credentials and issues an access token.
func (s *Service) IssueToken(ctx context.Context, orgID uuid.UUID, clientID, clientSecret string) (string, error) {
	sa, err := s.store.ByClientID(ctx, orgID, clientID)
	if err != nil {
		return "", domainerr.New(domainerr.CodeUnauthenticated, "invalid client credentials")
	}
	if !sa.Active {
		return "", domainerr.New(domainerr.CodeUnauthenticated, "service account inactive")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(sa.SecretHash), []byte(clientSecret)); err != nil {
		return "", domainerr.New(domainerr.CodeUnauthenticated, "invalid client credentials")
	}

	now := time.Now().UTC()
	claims := token.Claims{
		Subject:   sa.ID.String(),
		Issuer:    s.issuer,
		Audience:  []string{s.issuer},
		IssuedAt:  now,
		ExpiresAt: now.Add(s.ttl),
		JWTID:     uuid.NewString(),
		OrgID:     sa.OrgID.String(),
		Scopes:    append([]string(nil), sa.Scopes...),
		Scope:     strings.Join(sa.Scopes, " "),
	}
	return s.signer.Sign(claims)
}

// CreateHandler handles POST /api/v1/service-accounts.
func (s *Service) CreateHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgID  uuid.UUID `json:"org_id"`
		Name   string    `json:"name"`
		Scopes []string  `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	result, err := s.Create(r.Context(), body.OrgID, body.Name, body.Scopes)
	if err != nil {
		http.Error(w, fmt.Sprintf("create: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	// #nosec G117 -- the newly generated client secret is intentionally returned once at creation and never returned by list/read APIs.
	_ = json.NewEncoder(w).Encode(result)
}

// TokenHandler handles POST /api/v1/service-accounts/token.
func (s *Service) TokenHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	orgID, err := uuid.Parse(r.FormValue("org_id"))
	if err != nil {
		http.Error(w, "org_id required", http.StatusBadRequest)
		return
	}
	tok, err := s.IssueToken(r.Context(), orgID, r.FormValue("client_id"), r.FormValue("client_secret"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": tok,
		"token_type":   "Bearer",
		"expires_in":   int(s.ttl.Seconds()),
	})
}

func newToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
