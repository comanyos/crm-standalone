package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/eventstore"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func isUniqueViolation(err error) bool {
	var pge *pgconn.PgError
	return errors.As(err, &pge) && pge.Code == "23505"
}

// --- Org ---

type CreateOrgCommand struct {
	InstanceID    uuid.UUID `json:"instance_id"`
	Name          string    `json:"name"`
	PrimaryDomain string    `json:"primary_domain"`
}

type OrgResult struct {
	ID            uuid.UUID `json:"id"`
	InstanceID    uuid.UUID `json:"instance_id"`
	Name          string    `json:"name"`
	PrimaryDomain string    `json:"primary_domain"`
	State         int       `json:"state"`
}

func (c *Commands) CreateOrg(ctx context.Context, cmd CreateOrgCommand) (*OrgResult, error) {
	// Canonicalise before anything else touches the value. Tenant resolution
	// matches a canonicalised request Host against this column by exact
	// equality, so storing the caller's raw string creates an organization that
	// no request can ever resolve to — the failure that took the whole
	// deployment down and surfaced only as "tenant not found".
	domain, err := tenancy.CanonicalDomain(cmd.PrimaryDomain)
	if err != nil {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, err.Error())
	}
	cmd.PrimaryDomain = domain

	id := uuid.New()

	// Wrap INSERT + event Push in a single transaction so relational write
	// and event append are atomic (the documented core invariant, §0).
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create org: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	txCtx := database.WithTx(ctx, tx)

	if _, err := tx.Exec(txCtx,
		`INSERT INTO orgs (id, instance_id, name, primary_domain, state)
		 VALUES ($1, $2, $3, $4, 1)`,
		id, cmd.InstanceID, cmd.Name, cmd.PrimaryDomain,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("org with domain %s already exists", cmd.PrimaryDomain)
		}
		return nil, fmt.Errorf("create org: %w", err)
	}

	ev, _ := eventstore.NewEvent(eventstore.AggregateType("org"), id, id, "org.created",
		map[string]any{"name": cmd.Name, "primary_domain": cmd.PrimaryDomain}, actorFromContext(ctx))
	if err := c.es.Push(txCtx, ev); err != nil {
		return nil, fmt.Errorf("create org: push event: %w", err)
	}

	if err := tx.Commit(txCtx); err != nil {
		return nil, fmt.Errorf("create org: commit: %w", err)
	}

	return &OrgResult{ID: id, InstanceID: cmd.InstanceID, Name: cmd.Name, PrimaryDomain: cmd.PrimaryDomain, State: 1}, nil
}

// --- Project ---

type CreateProjectCommand struct {
	OrgID uuid.UUID `json:"org_id"`
	Name  string    `json:"name"`
}

type ProjectResult struct {
	ID    uuid.UUID `json:"id"`
	OrgID uuid.UUID `json:"org_id"`
	Name  string    `json:"name"`
}

func (c *Commands) CreateProject(ctx context.Context, cmd CreateProjectCommand) (*ProjectResult, error) {
	id := uuid.New()
	_, err := c.db.Exec(ctx,
		`INSERT INTO projects (id, org_id, name) VALUES ($1, $2, $3)`,
		id, cmd.OrgID, cmd.Name,
	)
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return &ProjectResult{ID: id, OrgID: cmd.OrgID, Name: cmd.Name}, nil
}

// --- Application ---

type CreateAppCommand struct {
	ProjectID uuid.UUID       `json:"project_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	Name      string          `json:"name"`
	Type      int             `json:"type"` // 1=oidc 2=saml 3=api
	Config    json.RawMessage `json:"config"`
}

type AppResult struct {
	ID        uuid.UUID       `json:"id"`
	ProjectID uuid.UUID       `json:"project_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	Name      string          `json:"name"`
	Type      int             `json:"type"`
	Config    json.RawMessage `json:"config"`
}

func (c *Commands) CreateApp(ctx context.Context, cmd CreateAppCommand) (*AppResult, error) {
	if len(cmd.Config) == 0 {
		cmd.Config = json.RawMessage("{}")
	}
	id := uuid.New()
	ct, err := c.db.Exec(ctx,
		`INSERT INTO applications (id, project_id, org_id, name, type, config)
		 SELECT $1, p.id, p.org_id, $4, $5, $6
		 FROM projects p WHERE p.id=$2 AND p.org_id=$3`,
		id, cmd.ProjectID, cmd.OrgID, cmd.Name, cmd.Type, []byte(cmd.Config),
	)
	if err != nil {
		return nil, fmt.Errorf("create app: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, domainerr.New(domainerr.CodeNotFound, "project not found")
	}
	return &AppResult{ID: id, ProjectID: cmd.ProjectID, OrgID: cmd.OrgID,
		Name: cmd.Name, Type: cmd.Type, Config: cmd.Config}, nil
}
