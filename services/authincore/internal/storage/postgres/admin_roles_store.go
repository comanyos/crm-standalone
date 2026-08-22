package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apihttp "github.com/authincore/authincore/internal/api/http"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminRoleStore implements apihttp.RoleManager.
type AdminRoleStore struct{ db *pgxpool.Pool }

func NewAdminRoleStore(db *pgxpool.Pool) *AdminRoleStore { return &AdminRoleStore{db: db} }

func (s *AdminRoleStore) ListRoles(ctx context.Context, orgID uuid.UUID) ([]*apihttp.AdminRole, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, key, display_name, created_at FROM roles WHERE org_id = $1 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	var out []*apihttp.AdminRole
	for rows.Next() {
		r := &apihttp.AdminRole{}
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Key, &r.DisplayName, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *AdminRoleStore) CreateRole(ctx context.Context, r *apihttp.AdminRole) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO roles (id, org_id, key, display_name) VALUES ($1,$2,$3,$4)`,
		r.ID, r.OrgID, r.Key, r.DisplayName,
	)
	return err
}

func (s *AdminRoleStore) DeleteRole(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM roles WHERE org_id=$1 AND id=$2`, orgID, id)
	return err
}

func (s *AdminRoleStore) AssignRole(ctx context.Context, orgID, identityID, roleID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`INSERT INTO role_assignments (id, org_id, identity_id, role_id)
		 SELECT $1, $2, i.id, r.id
		 FROM identities i
		 JOIN roles r ON r.id=$4 AND r.org_id=$2
		 WHERE i.id=$3 AND i.org_id=$2
		 ON CONFLICT (org_id, identity_id, role_id) DO NOTHING`,
		uuid.New(), orgID, identityID, roleID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeNotFound, "identity or role not found")
	}
	return nil
}

func (s *AdminRoleStore) UnassignRole(ctx context.Context, orgID, identityID, roleID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM role_assignments WHERE org_id=$1 AND identity_id=$2 AND role_id=$3`,
		orgID, identityID, roleID,
	)
	return err
}

// AdminApplicationStore implements apihttp.ApplicationManager.
type AdminApplicationStore struct{ db *pgxpool.Pool }

func NewAdminApplicationStore(db *pgxpool.Pool) *AdminApplicationStore {
	return &AdminApplicationStore{db: db}
}

func (s *AdminApplicationStore) ListApplications(ctx context.Context, orgID uuid.UUID) ([]*apihttp.AdminApplication, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, name, config, created_at FROM applications WHERE org_id=$1 AND type=1 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}
	defer rows.Close()
	var out []*apihttp.AdminApplication
	for rows.Next() {
		a := &apihttp.AdminApplication{}
		var configJSON []byte
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &configJSON, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan application: %w", err)
		}
		var cfg struct {
			ClientID      string   `json:"client_id"`
			RedirectURIs  []string `json:"redirect_uris"`
			GrantTypes    []string `json:"grant_types"`
			ResponseTypes []string `json:"response_types"`
		}
		_ = json.Unmarshal(configJSON, &cfg)
		a.ClientID = cfg.ClientID
		a.RedirectURIs = cfg.RedirectURIs
		a.GrantTypes = cfg.GrantTypes
		a.ResponseTypes = cfg.ResponseTypes
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *AdminApplicationStore) CreateApplication(ctx context.Context, a *apihttp.AdminApplication) error {
	cfg, err := json.Marshal(map[string]any{
		"client_id":      a.ClientID,
		"client_secret":  a.ClientSecret,
		"redirect_uris":  a.RedirectURIs,
		"grant_types":    a.GrantTypes,
		"response_types": a.ResponseTypes,
	})
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var projectID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM projects WHERE org_id=$1 ORDER BY created_at LIMIT 1`, a.OrgID,
	).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		projectID = uuid.New()
		if _, err = tx.Exec(ctx,
			`INSERT INTO projects (id, org_id, name) VALUES ($1,$2,$3)`,
			projectID, a.OrgID, "AuthinCore Applications",
		); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO applications (id, project_id, org_id, name, type, config)
		 VALUES ($1,$2,$3,$4,1,$5)`,
		a.ID, projectID, a.OrgID, a.Name, cfg,
	)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *AdminApplicationStore) DeleteApplication(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM applications WHERE org_id=$1 AND id=$2`, orgID, id)
	return err
}
