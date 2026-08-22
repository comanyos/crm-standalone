package postgres

import (
	"context"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	magiclink "github.com/authincore/authincore/internal/flow/strategy/magiclink"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MagicLinkStore implements magiclink.Store using Postgres.
type MagicLinkStore struct{ db *pgxpool.Pool }

func NewMagicLinkStore(db *pgxpool.Pool) *MagicLinkStore { return &MagicLinkStore{db: db} }

func (s *MagicLinkStore) Create(ctx context.Context, lc *magiclink.LoginCode) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO login_codes (id, org_id, identity_id, email, code, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (org_id, email) DO UPDATE
		   SET code=EXCLUDED.code, expires_at=EXCLUDED.expires_at, attempts=0, created_at=now()`,
		lc.ID, lc.OrgID, lc.IdentityID, lc.Email, lc.Code, lc.ExpiresAt,
	)
	return err
}

func (s *MagicLinkStore) Consume(ctx context.Context, orgID uuid.UUID, email, code string) (*magiclink.LoginCode, error) {
	var lc magiclink.LoginCode
	err := s.db.QueryRow(ctx,
		`DELETE FROM login_codes
		 WHERE org_id=$1 AND email=$2 AND code=$3 AND expires_at > now() AND attempts < 5
		 RETURNING id, org_id, identity_id, email, code, expires_at, attempts`,
		orgID, email, code,
	).Scan(&lc.ID, &lc.OrgID, &lc.IdentityID, &lc.Email, &lc.Code, &lc.ExpiresAt, &lc.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Increment attempts if record exists but code is wrong.
			_, _ = s.db.Exec(ctx,
				`UPDATE login_codes SET attempts=attempts+1 WHERE org_id=$1 AND email=$2`,
				orgID, email,
			)
			return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid or expired code")
		}
		return nil, fmt.Errorf("consume login code: %w", err)
	}
	return &lc, nil
}
