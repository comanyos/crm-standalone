package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TOTPStore implements totp.SecretStore against Postgres.
type TOTPStore struct {
	db *pgxpool.Pool
}

func NewTOTPStore(db *pgxpool.Pool) *TOTPStore {
	return &TOTPStore{db: db}
}

// GetSecret returns the active TOTP secret for an identity (used during login MFA).
func (s *TOTPStore) GetSecret(ctx context.Context, identityID uuid.UUID) (string, error) {
	var secret string
	err := s.db.QueryRow(ctx,
		`SELECT secret FROM totp_secrets WHERE identity_id=$1 AND active=true`,
		identityID,
	).Scan(&secret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domainerr.New(domainerr.CodeNotFound, "totp secret not found")
		}
		return "", fmt.Errorf("get totp secret: %w", err)
	}
	return secret, nil
}

// StorePending inserts or replaces an unactivated TOTP secret (enrollment phase 1).
func (s *TOTPStore) StorePending(ctx context.Context, identityID uuid.UUID, secret string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO totp_secrets (id, identity_id, secret, active, created_at)
		 VALUES (gen_random_uuid(), $1, $2, false, $3)
		 ON CONFLICT (identity_id)
		 DO UPDATE SET secret=EXCLUDED.secret, active=false, created_at=EXCLUDED.created_at`,
		identityID, secret, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("store pending totp: %w", err)
	}
	return nil
}

// Activate promotes the pending secret to active (enrollment phase 2).
func (s *TOTPStore) Activate(ctx context.Context, identityID uuid.UUID) error {
	ct, err := s.db.Exec(ctx,
		`UPDATE totp_secrets SET active=true WHERE identity_id=$1`,
		identityID,
	)
	if err != nil {
		return fmt.Errorf("activate totp: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeNotFound, "pending totp secret not found")
	}
	return nil
}

// SaveBackupCodes replaces all backup codes for an identity (stored as plaintext for now;
// production should hash them with bcrypt/argon2id).
func (s *TOTPStore) SaveBackupCodes(ctx context.Context, identityID uuid.UUID, codes []string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("totp backup codes tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `DELETE FROM totp_backup_codes WHERE identity_id=$1`, identityID)
	if err != nil {
		return fmt.Errorf("clear backup codes: %w", err)
	}
	for _, code := range codes {
		_, err = tx.Exec(ctx,
			`INSERT INTO totp_backup_codes (id, identity_id, code, used) VALUES (gen_random_uuid(), $1, $2, false)`,
			identityID, code,
		)
		if err != nil {
			return fmt.Errorf("insert backup code: %w", err)
		}
	}
	return tx.Commit(ctx)
}
