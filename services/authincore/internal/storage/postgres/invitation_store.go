package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/invitation"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InvitationStore implements invitation.Store.
type InvitationStore struct{ db *pgxpool.Pool }

func NewInvitationStore(db *pgxpool.Pool) *InvitationStore { return &InvitationStore{db: db} }

func (s *InvitationStore) Insert(ctx context.Context, inv *invitation.Invitation) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO invitations (id, org_id, email, role, token, inviter_id, expires_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		inv.ID, inv.OrgID, inv.Email, inv.Role, inv.Token, inv.InviterID, inv.ExpiresAt, inv.CreatedAt,
	)
	return err
}

func (s *InvitationStore) ByToken(ctx context.Context, token string) (*invitation.Invitation, error) {
	inv := &invitation.Invitation{}
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, email, role, token, inviter_id, expires_at, accepted_at, identity_id, created_at
		 FROM invitations WHERE token=$1`, token,
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.Token, &inv.InviterID,
		&inv.ExpiresAt, &inv.AcceptedAt, &inv.IdentityID, &inv.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "invitation not found")
		}
		return nil, fmt.Errorf("invitation by token: %w", err)
	}
	return inv, nil
}

func (s *InvitationStore) Accept(ctx context.Context, token string, identityID uuid.UUID) error {
	now := time.Now().UTC()
	tag, err := s.db.Exec(ctx,
		`UPDATE invitations SET accepted_at=$1, identity_id=$2 WHERE token=$3 AND accepted_at IS NULL`,
		now, identityID, token,
	)
	if err != nil {
		return fmt.Errorf("accept invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeAlreadyExists, "invitation already accepted")
	}
	return nil
}

func (s *InvitationStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]*invitation.Invitation, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, email, role, token, inviter_id, expires_at, accepted_at, identity_id, created_at
		 FROM invitations WHERE org_id=$1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()
	var out []*invitation.Invitation
	for rows.Next() {
		inv := &invitation.Invitation{}
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.Token, &inv.InviterID,
			&inv.ExpiresAt, &inv.AcceptedAt, &inv.IdentityID, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *InvitationStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM invitations WHERE id=$1 AND org_id=$2`, id, orgID)
	return err
}
