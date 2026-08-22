package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InvitationRoleAssigner resolves invitation role keys and assigns them to the
// accepted identity. Unknown role keys are created as tenant-local roles.
type InvitationRoleAssigner struct{ db *pgxpool.Pool }

func NewInvitationRoleAssigner(db *pgxpool.Pool) *InvitationRoleAssigner {
	return &InvitationRoleAssigner{db: db}
}

func (a *InvitationRoleAssigner) AssignRole(ctx context.Context, orgID, identityID uuid.UUID, role string) error {
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" {
		role = "member"
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("assign invitation role: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var roleID uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO roles (id, org_id, key, display_name)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (org_id, key) DO UPDATE SET display_name=roles.display_name
		 RETURNING id`,
		uuid.New(), orgID, role, strings.ToUpper(role[:1])+role[1:],
	).Scan(&roleID); err != nil {
		return fmt.Errorf("assign invitation role: resolve role: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`INSERT INTO role_assignments (id, org_id, identity_id, role_id)
		 SELECT $1,$2,i.id,$4 FROM identities i WHERE i.id=$3 AND i.org_id=$2
		 ON CONFLICT (org_id, identity_id, role_id) DO NOTHING`,
		uuid.New(), orgID, identityID, roleID,
	)
	if err != nil {
		return fmt.Errorf("assign invitation role: insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("assign invitation role: identity does not belong to organization")
	}
	return tx.Commit(ctx)
}
