package postgres

import (
	"context"
	"fmt"

	"github.com/authincore/authincore/internal/authz"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthzRoleStore implements authz.RoleStore.
type AuthzRoleStore struct {
	db *pgxpool.Pool
}

func NewAuthzRoleStore(db *pgxpool.Pool) *AuthzRoleStore {
	return &AuthzRoleStore{db: db}
}

func (s *AuthzRoleStore) RolesForIdentity(ctx context.Context, orgID, identityID uuid.UUID, scope authz.RoleScope) ([]authz.Role, error) {
	// The project predicate is chosen here rather than passed in, so no caller
	// can widen it. Organization scope means project_id IS NULL — assignments
	// made against a specific project are deliberately invisible to it.
	const base = `SELECT r.key, COALESCE(ra.project_id, '00000000-0000-0000-0000-000000000000'::uuid), r.org_id, r.display_name
		 FROM role_assignments ra
		 JOIN roles r ON r.id = ra.role_id
		 WHERE ra.org_id = $1 AND ra.identity_id = $2 AND `

	var rows pgx.Rows
	var err error
	if scope.ProjectID == nil {
		rows, err = s.db.Query(ctx, base+`ra.project_id IS NULL`, orgID, identityID)
	} else {
		rows, err = s.db.Query(ctx, base+`ra.project_id = $3`, orgID, identityID, *scope.ProjectID)
	}
	if err != nil {
		return nil, fmt.Errorf("roles for identity: %w", err)
	}
	defer rows.Close()

	var out []authz.Role
	for rows.Next() {
		var r authz.Role
		if err := rows.Scan(&r.Key, &r.ProjectID, &r.OrgID, &r.DisplayName); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuthzPermissionStore implements authz.PermissionStore.
type AuthzPermissionStore struct {
	db *pgxpool.Pool
}

func NewAuthzPermissionStore(db *pgxpool.Pool) *AuthzPermissionStore {
	return &AuthzPermissionStore{db: db}
}

func (s *AuthzPermissionStore) PermissionsForRoles(ctx context.Context, orgID uuid.UUID, roles []authz.Role) ([]authz.Permission, error) {
	if len(roles) == 0 {
		return nil, nil
	}
	keys := make([]string, len(roles))
	for i, r := range roles {
		keys[i] = r.Key
	}

	rows, err := s.db.Query(ctx,
		`SELECT res.id, res.type, p.scopes
		 FROM permissions p
		 JOIN roles r ON r.id = p.role_id
		 JOIN resources res ON res.id = p.resource_id
		 WHERE r.org_id = $1 AND r.key = ANY($2)`,
		orgID, keys,
	)
	if err != nil {
		return nil, fmt.Errorf("permissions for roles: %w", err)
	}
	defer rows.Close()

	var out []authz.Permission
	for rows.Next() {
		var p authz.Permission
		var scopes []string
		if err := rows.Scan(&p.ResourceID, &p.ResourceType, &scopes); err != nil {
			return nil, fmt.Errorf("scan permission: %w", err)
		}
		p.Scopes = scopes
		out = append(out, p)
	}
	return out, rows.Err()
}
