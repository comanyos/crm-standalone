package query

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// OrgView is the read model for an organization.
type OrgView struct {
	ID            uuid.UUID `json:"id"`
	InstanceID    uuid.UUID `json:"instance_id"`
	Name          string    `json:"name"`
	PrimaryDomain string    `json:"primary_domain"`
	State         int       `json:"state"`
}

// ListOrgs returns all orgs for an instance.
func (q *Queries) ListOrgs(ctx context.Context, instanceID uuid.UUID) ([]*OrgView, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, instance_id, name, primary_domain, state FROM orgs WHERE instance_id = $1 ORDER BY name`,
		instanceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()
	var out []*OrgView
	for rows.Next() {
		o := &OrgView{}
		if err := rows.Scan(&o.ID, &o.InstanceID, &o.Name, &o.PrimaryDomain, &o.State); err != nil {
			return nil, fmt.Errorf("scan org: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ProjectView is the read model for a project.
type ProjectView struct {
	ID    uuid.UUID `json:"id"`
	OrgID uuid.UUID `json:"org_id"`
	Name  string    `json:"name"`
}

// ListProjects returns all projects for an org.
func (q *Queries) ListProjects(ctx context.Context, orgID uuid.UUID) ([]*ProjectView, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id, name FROM projects WHERE org_id = $1 ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var out []*ProjectView
	for rows.Next() {
		p := &ProjectView{}
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AppView is the read model for an application.
type AppView struct {
	ID        uuid.UUID       `json:"id"`
	ProjectID uuid.UUID       `json:"project_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	Name      string          `json:"name"`
	Type      int             `json:"type"`
	Config    json.RawMessage `json:"config"`
}

// ListApps returns all applications for a project.
func (q *Queries) ListApps(ctx context.Context, projectID, orgID uuid.UUID) ([]*AppView, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, project_id, org_id, name, type, config
		 FROM applications WHERE project_id = $1 AND org_id=$2 ORDER BY name`,
		projectID, orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()
	var out []*AppView
	for rows.Next() {
		a := &AppView{}
		var cfg []byte
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.OrgID, &a.Name, &a.Type, &cfg); err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		a.Config = json.RawMessage(cfg)
		out = append(out, a)
	}
	return out, rows.Err()
}
