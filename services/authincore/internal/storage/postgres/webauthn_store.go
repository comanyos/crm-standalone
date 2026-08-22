package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebAuthnCredentialStore persists and retrieves WebAuthn credentials.
type WebAuthnCredentialStore struct {
	db *pgxpool.Pool
}

func NewWebAuthnCredentialStore(db *pgxpool.Pool) *WebAuthnCredentialStore {
	return &WebAuthnCredentialStore{db: db}
}

// SaveCredential persists a newly registered WebAuthn credential.
func (s *WebAuthnCredentialStore) SaveCredential(ctx context.Context, identityID uuid.UUID, cred *webauthn.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal webauthn credential: %w", err)
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO webauthn_credentials
		   (id, identity_id, credential_id, public_key, raw, created_at)
		 VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
		 ON CONFLICT (credential_id) DO UPDATE SET raw = EXCLUDED.raw`,
		identityID, cred.ID, cred.PublicKey, raw, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("save webauthn credential: %w", err)
	}
	return nil
}

// CredentialsForIdentity loads all WebAuthn credentials for an identity.
func (s *WebAuthnCredentialStore) CredentialsForIdentity(ctx context.Context, identityID uuid.UUID) ([]webauthn.Credential, error) {
	rows, err := s.db.Query(ctx,
		`SELECT raw FROM webauthn_credentials WHERE identity_id = $1`,
		identityID,
	)
	if err != nil {
		return nil, fmt.Errorf("load webauthn credentials: %w", err)
	}
	defer rows.Close()

	var out []webauthn.Credential
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan webauthn credential: %w", err)
		}
		var cred webauthn.Credential
		if err := json.Unmarshal(raw, &cred); err == nil {
			out = append(out, cred)
		}
	}
	return out, rows.Err()
}

// UpdateSignCount updates the stored sign count after successful assertion.
func (s *WebAuthnCredentialStore) UpdateSignCount(ctx context.Context, credentialID []byte, count uint32) error {
	raw := json.RawMessage{}
	err := s.db.QueryRow(ctx,
		`SELECT raw FROM webauthn_credentials WHERE credential_id = $1`,
		credentialID,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("webauthn credential not found")
		}
		return fmt.Errorf("read webauthn credential for update: %w", err)
	}

	var cred webauthn.Credential
	if err := json.Unmarshal(raw, &cred); err != nil {
		return fmt.Errorf("unmarshal webauthn credential: %w", err)
	}
	cred.Authenticator.SignCount = count
	updated, _ := json.Marshal(cred)
	_, err = s.db.Exec(ctx,
		`UPDATE webauthn_credentials SET raw = $1 WHERE credential_id = $2`,
		updated, credentialID,
	)
	return err
}
