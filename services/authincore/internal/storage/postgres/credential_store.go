package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialStore implements password.CredentialStore against Postgres.
// It reads from identity_credentials where type='password' and looks up
// the identifier in the JSONB identifiers array.
type CredentialStore struct {
	db *pgxpool.Pool
}

func NewCredentialStore(db *pgxpool.Pool) *CredentialStore {
	return &CredentialStore{db: db}
}

type passwordConfig struct {
	HashedPassword string `json:"hashed_password"`
}

// FindIdentityByIdentifier looks up the identity by email/username and returns the password hash.
func (s *CredentialStore) FindIdentityByIdentifier(ctx context.Context, orgID uuid.UUID, identifier string) (uuid.UUID, string, error) {
	var identityID uuid.UUID
	var configJSON []byte

	identifierJSON, err := json.Marshal([]string{identifier})
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("marshal identifier: %w", err)
	}
	err = s.db.QueryRow(ctx,
		`SELECT ic.identity_id, ic.config
		 FROM identity_credentials ic
		 JOIN identities i ON i.id = ic.identity_id
		 WHERE i.org_id = $1
		   AND ic.type = 'password'
		   AND ic.identifiers @> $2::jsonb`,
		orgID,
		string(identifierJSON),
	).Scan(&identityID, &configJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", domainerr.New(domainerr.CodeNotFound, "identity not found")
		}
		return uuid.Nil, "", fmt.Errorf("find identity by identifier: %w", err)
	}

	var cfg passwordConfig
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return uuid.Nil, "", fmt.Errorf("unmarshal password config: %w", err)
	}
	return identityID, cfg.HashedPassword, nil
}

// StorePasswordCredential upserts the hashed password credential for an identity.
func (s *CredentialStore) StorePasswordCredential(ctx context.Context, identityID uuid.UUID, hash string) error {
	cfg, _ := json.Marshal(passwordConfig{HashedPassword: hash})
	_, err := s.db.Exec(ctx,
		`INSERT INTO identity_credentials (id, identity_id, type, identifiers, config, version)
		 VALUES (gen_random_uuid(), $1, 'password', '[]'::jsonb, $2, 'v1')
		 ON CONFLICT (identity_id, type)
		 DO UPDATE SET config = EXCLUDED.config, updated_at = NOW()`,
		identityID, cfg,
	)
	if err != nil {
		return fmt.Errorf("store password credential: %w", err)
	}
	return nil
}

// GetHashByIdentityID fetches the stored argon2id hash for a password credential.
func (s *CredentialStore) GetHashByIdentityID(ctx context.Context, identityID uuid.UUID) (string, error) {
	var configJSON []byte
	err := s.db.QueryRow(ctx,
		`SELECT config FROM identity_credentials WHERE identity_id = $1 AND type = 'password'`,
		identityID,
	).Scan(&configJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domainerr.New(domainerr.CodeNotFound, "password credential not found")
		}
		return "", fmt.Errorf("get hash by identity id: %w", err)
	}
	var cfg passwordConfig
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return "", fmt.Errorf("unmarshal password config: %w", err)
	}
	return cfg.HashedPassword, nil
}

// AddIdentifier appends an identifier (e.g. email) to a password credential's identifiers array.
func (s *CredentialStore) AddIdentifier(ctx context.Context, identityID uuid.UUID, identifier string) error {
	idJSON, err := json.Marshal([]string{identifier})
	if err != nil {
		return fmt.Errorf("marshal identifier: %w", err)
	}
	_, err = s.db.Exec(ctx,
		`UPDATE identity_credentials
		 SET identifiers = identifiers || $1::jsonb, updated_at = NOW()
		 WHERE identity_id = $2 AND type = 'password'`,
		string(idJSON), identityID,
	)
	if err != nil {
		return fmt.Errorf("add identifier: %w", err)
	}
	return nil
}
