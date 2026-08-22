package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/flow/strategy/recovery"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecoveryStore implements recovery.Store using the recovery_codes table (migration 004).
type RecoveryStore struct {
	db *pgxpool.Pool
}

func NewRecoveryStore(db *pgxpool.Pool) *RecoveryStore {
	return &RecoveryStore{db: db}
}

func (s *RecoveryStore) Create(ctx context.Context, rc *recovery.RecoveryCode) error {
	// Delete any existing unused codes for this email before inserting.
	_, _ = s.db.Exec(ctx,
		`DELETE FROM recovery_codes WHERE org_id=$1 AND email=$2`, rc.OrgID, rc.Email,
	)
	_, err := s.db.Exec(ctx,
		`INSERT INTO recovery_codes
		   (id, org_id, identity_id, email, code, expires_at, attempts)
		 VALUES ($1,$2,$3,$4,$5,$6,0)`,
		rc.ID, rc.OrgID, rc.IdentityID, rc.Email, rc.Code, rc.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create recovery code: %w", err)
	}
	return nil
}

func (s *RecoveryStore) Consume(ctx context.Context, orgID uuid.UUID, email, code string) (*recovery.RecoveryCode, error) {
	var rc recovery.RecoveryCode

	err := s.db.QueryRow(ctx,
		`WITH deleted AS (
		   DELETE FROM recovery_codes
		   WHERE org_id=$1 AND email=$2 AND code=$3 AND expires_at > NOW() AND attempts < 5
		   RETURNING id, org_id, identity_id, email, code, expires_at, attempts
		 ) SELECT * FROM deleted`,
		orgID, email, code,
	).Scan(&rc.ID, &rc.OrgID, &rc.IdentityID, &rc.Email, &rc.Code, &rc.ExpiresAt, &rc.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Increment attempt counter on any existing code (rate-limit code guessing).
			_, _ = s.db.Exec(ctx,
				`UPDATE recovery_codes SET attempts = attempts + 1 WHERE org_id=$1 AND email=$2`,
				orgID, email,
			)
			return nil, domainerr.New(domainerr.CodeNotFound, "invalid or expired recovery code")
		}
		return nil, fmt.Errorf("consume recovery code: %w", err)
	}
	return &rc, nil
}

func (s *RecoveryStore) FindIdentityByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error) {
	var identityID uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT ic.identity_id
		 FROM identity_credentials ic
		 JOIN identities i ON i.id = ic.identity_id
		 WHERE i.org_id = $1
		   AND ic.type = 'password'
		   AND ic.identifiers @> $2::jsonb`,
		orgID, fmt.Sprintf(`[%q]`, email),
	).Scan(&identityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, domainerr.New(domainerr.CodeNotFound, "identity not found")
		}
		return uuid.Nil, fmt.Errorf("find identity by email: %w", err)
	}
	return identityID, nil
}

// PurgeExpiredCodes deletes all expired recovery codes. Call on a maintenance timer.
func (s *RecoveryStore) PurgeExpiredCodes(ctx context.Context) (int64, error) {
	ct, err := s.db.Exec(ctx,
		`DELETE FROM recovery_codes WHERE expires_at < $1`, time.Now().UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("purge recovery codes: %w", err)
	}
	return ct.RowsAffected(), nil
}
