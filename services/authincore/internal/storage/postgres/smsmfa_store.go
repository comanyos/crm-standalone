package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	smsmfa "github.com/authincore/authincore/internal/flow/strategy/smsmfa"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SMSOTPStore implements smsmfa.Store using Postgres.
type SMSOTPStore struct{ db *pgxpool.Pool }

func NewSMSOTPStore(db *pgxpool.Pool) *SMSOTPStore { return &SMSOTPStore{db: db} }

func (s *SMSOTPStore) Save(ctx context.Context, r *smsmfa.OTPRecord) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO sms_otps (org_id, identity_id, phone, code, expires_at)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (org_id, identity_id) DO UPDATE
		   SET code=EXCLUDED.code, expires_at=EXCLUDED.expires_at, attempts=0`,
		r.OrgID, r.IdentityID, r.Phone, r.Code, r.ExpiresAt,
	)
	return err
}

func (s *SMSOTPStore) Consume(ctx context.Context, orgID, identityID uuid.UUID, code string) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM sms_otps
		 WHERE identity_id=$1 AND code=$2 AND expires_at > $3 AND attempts < 5`,
		identityID, code, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("consume SMS OTP: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Increment attempts for throttling.
		_, _ = s.db.Exec(ctx,
			`UPDATE sms_otps SET attempts=attempts+1 WHERE identity_id=$1`,
			identityID,
		)
		return domainerr.New(domainerr.CodeUnauthenticated, "invalid or expired SMS code")
	}
	return nil
}

// SMSPhoneResolver resolves a phone number from identity traits.
type SMSPhoneResolver struct{ db *pgxpool.Pool }

func NewSMSPhoneResolver(db *pgxpool.Pool) *SMSPhoneResolver { return &SMSPhoneResolver{db: db} }

func (r *SMSPhoneResolver) PhoneForIdentity(ctx context.Context, orgID, identityID uuid.UUID) (string, error) {
	var phone string
	err := r.db.QueryRow(ctx,
		`SELECT traits->>'phone' FROM identities WHERE id=$1 AND org_id=$2`,
		identityID, orgID,
	).Scan(&phone)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domainerr.New(domainerr.CodeNotFound, "identity not found")
		}
		return "", fmt.Errorf("resolve phone: %w", err)
	}
	if phone == "" {
		return "", domainerr.New(domainerr.CodeNotFound, "no phone number on identity")
	}
	return phone, nil
}
