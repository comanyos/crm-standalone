// Package credential manages the persistence of identity credentials.
package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/domain/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Store(ctx context.Context, c *identity.Credential) error {
	configJSON, _ := json.Marshal(c.Config)
	identifiersJSON, _ := json.Marshal(c.Identifiers)
	_, err := r.db.Exec(ctx,
		`INSERT INTO identity_credentials (id, identity_id, type, identifiers, config, version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (identity_id, type) DO UPDATE
		   SET identifiers = EXCLUDED.identifiers,
		       config = EXCLUDED.config,
		       version = EXCLUDED.version,
		       updated_at = EXCLUDED.updated_at`,
		c.ID, c.IdentityID, c.Type, identifiersJSON, configJSON, c.Version,
		c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (r *Repository) ByIdentityAndType(ctx context.Context, identityID uuid.UUID, credType identity.CredentialType) (*identity.Credential, error) {
	row := r.db.QueryRow(ctx,
		`SELECT id, identity_id, type, identifiers, config, version, created_at, updated_at
		 FROM identity_credentials WHERE identity_id = $1 AND type = $2`,
		identityID, credType,
	)
	c := &identity.Credential{Type: credType}
	var identifiersJSON, configJSON []byte
	if err := row.Scan(&c.ID, &c.IdentityID, &c.Type, &identifiersJSON, &configJSON, &c.Version, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, fmt.Errorf("credential not found: %w", err)
	}
	_ = json.Unmarshal(identifiersJSON, &c.Identifiers)
	c.Config = configJSON
	return c, nil
}

// FindByIdentifier looks up an identity by a credential identifier (e.g. email address).
func (r *Repository) FindByIdentifier(ctx context.Context, orgID uuid.UUID, credType identity.CredentialType, identifier string) (uuid.UUID, *identity.Credential, error) {
	row := r.db.QueryRow(ctx,
		`SELECT ic.id, ic.identity_id, ic.type, ic.identifiers, ic.config, ic.version, ic.created_at, ic.updated_at
		 FROM identity_credentials ic
		 JOIN identities i ON i.id = ic.identity_id
		 WHERE i.org_id = $1
		   AND ic.type = $2
		   AND ic.identifiers @> $3::jsonb`,
		orgID, credType, fmt.Sprintf(`["%s"]`, identifier),
	)
	c := &identity.Credential{Type: credType}
	var identityID uuid.UUID
	var identifiersJSON, configJSON []byte
	if err := row.Scan(&c.ID, &identityID, &c.Type, &identifiersJSON, &configJSON, &c.Version, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return uuid.Nil, nil, fmt.Errorf("credential not found for identifier: %w", err)
	}
	_ = json.Unmarshal(identifiersJSON, &c.Identifiers)
	c.Config = configJSON
	c.IdentityID = identityID
	return identityID, c, nil
}

func (r *Repository) Delete(ctx context.Context, identityID uuid.UUID, credType identity.CredentialType) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM identity_credentials WHERE identity_id = $1 AND type = $2`,
		identityID, credType,
	)
	return err
}

// NewCredential is a helper for creating a credential value.
func NewCredential(identityID uuid.UUID, credType identity.CredentialType, identifiers []string, config any) (*identity.Credential, error) {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &identity.Credential{
		ID:          uuid.New(),
		IdentityID:  identityID,
		Type:        credType,
		Identifiers: identifiers,
		Config:      configJSON,
		Version:     "v1",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}
