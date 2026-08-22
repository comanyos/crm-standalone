package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/flow/strategy/verification"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VerificationStore implements verification.Store.
type VerificationStore struct {
	db *pgxpool.Pool
}

func NewVerificationStore(db *pgxpool.Pool) *VerificationStore {
	return &VerificationStore{db: db}
}

func (s *VerificationStore) Create(ctx context.Context, vc *verification.VerificationCode) error {
	// Replace any existing unverified code for this email.
	_, _ = s.db.Exec(ctx,
		`DELETE FROM verification_codes WHERE org_id=$1 AND email=$2`, vc.OrgID, vc.Email,
	)
	_, err := s.db.Exec(ctx,
		`INSERT INTO verification_codes
		   (id, org_id, identity_id, email, code, expires_at, attempts)
		 VALUES ($1,$2,$3,$4,$5,$6,0)`,
		vc.ID, vc.OrgID, vc.IdentityID, vc.Email, vc.Code, vc.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create verification code: %w", err)
	}
	return nil
}

func (s *VerificationStore) Consume(ctx context.Context, orgID uuid.UUID, email, code string) (*verification.VerificationCode, error) {
	var vc verification.VerificationCode
	err := s.db.QueryRow(ctx,
		`WITH deleted AS (
		   DELETE FROM verification_codes
		   WHERE org_id=$1 AND email=$2 AND code=$3 AND expires_at > NOW() AND attempts < 5
		   RETURNING id, org_id, identity_id, email, code, expires_at, attempts
		 ) SELECT * FROM deleted`,
		orgID, email, code,
	).Scan(&vc.ID, &vc.OrgID, &vc.IdentityID, &vc.Email, &vc.Code, &vc.ExpiresAt, &vc.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_, _ = s.db.Exec(ctx,
				`UPDATE verification_codes SET attempts = attempts + 1 WHERE org_id=$1 AND email=$2`,
				orgID, email,
			)
			return nil, domainerr.New(domainerr.CodeNotFound, "invalid or expired verification code")
		}
		return nil, fmt.Errorf("consume verification code: %w", err)
	}
	return &vc, nil
}

// MarkVerified writes an `email_verified` flag into the identity's metadata_admin JSONB.
// Production: replace with a dedicated verified_addresses table.
func (s *VerificationStore) MarkVerified(ctx context.Context, identityID uuid.UUID, email string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE identities
		 SET metadata_admin = jsonb_set(
		       COALESCE(metadata_admin, '{}'),
		       '{verified_emails}',
		       COALESCE(metadata_admin->'verified_emails', '[]'::jsonb) || to_jsonb($1::text)
		     ),
		     updated_at = $2
		 WHERE id = $3`,
		email, time.Now().UTC(), identityID,
	)
	if err != nil {
		return fmt.Errorf("mark verified: %w", err)
	}
	return nil
}

// FindIdentityByEmail satisfies verification.IdentityEmailResolver by delegating to credential lookup.
func (s *VerificationStore) FindIdentityByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error) {
	// Reuse the same credentials lookup as the recovery store.
	rs := RecoveryStore{db: s.db}
	return rs.FindIdentityByEmail(ctx, orgID, email)
}
