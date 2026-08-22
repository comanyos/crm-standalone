package scim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// TokenIssuer creates and manages SCIM bearer tokens for organizations.
type TokenIssuer interface {
	// Issue creates a new SCIM bearer token for the given org.
	// Returns the plaintext token (only shown once) and the token metadata.
	Issue(ctx context.Context, orgID uuid.UUID, description string) (plaintext string, meta *TokenMeta, err error)

	// List returns all tokens for an org (without plaintext values).
	List(ctx context.Context, orgID uuid.UUID) ([]*TokenMeta, error)

	// Revoke deletes a token by ID.
	Revoke(ctx context.Context, orgID uuid.UUID, tokenID string) error
}

// TokenMeta is metadata about a SCIM token (without the plaintext value).
type TokenMeta struct {
	ID          string     `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// TokenStore persists SCIM tokens.
type TokenStore interface {
	// Save stores a new token (hash + metadata).
	Save(ctx context.Context, orgID uuid.UUID, tokenID, hash, description string) error

	// List returns all tokens for an org.
	List(ctx context.Context, orgID uuid.UUID) ([]*TokenMeta, error)

	// Delete removes a token by ID.
	Delete(ctx context.Context, orgID uuid.UUID, tokenID string) error

	// Validate checks if a plaintext token is valid for the org.
	Validate(ctx context.Context, orgID uuid.UUID, plaintext string) error
}

// tokenIssuer implements TokenIssuer.
type tokenIssuer struct {
	store TokenStore
}

// NewTokenIssuer creates a new token issuer.
func NewTokenIssuer(store TokenStore) TokenIssuer {
	return &tokenIssuer{store: store}
}

func (ti *tokenIssuer) Issue(ctx context.Context, orgID uuid.UUID, description string) (string, *TokenMeta, error) {
	// Generate 32-byte random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	plaintext := hex.EncodeToString(tokenBytes)

	// Hash with bcrypt (cost 10)
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), 10)
	if err != nil {
		return "", nil, fmt.Errorf("hash token: %w", err)
	}

	tokenID := uuid.New().String()
	if err := ti.store.Save(ctx, orgID, tokenID, string(hash), description); err != nil {
		return "", nil, fmt.Errorf("save token: %w", err)
	}

	meta := &TokenMeta{
		ID:          tokenID,
		OrgID:       orgID,
		Description: description,
		CreatedAt:   time.Now(),
	}

	return plaintext, meta, nil
}

func (ti *tokenIssuer) List(ctx context.Context, orgID uuid.UUID) ([]*TokenMeta, error) {
	return ti.store.List(ctx, orgID)
}

func (ti *tokenIssuer) Revoke(ctx context.Context, orgID uuid.UUID, tokenID string) error {
	return ti.store.Delete(ctx, orgID, tokenID)
}
