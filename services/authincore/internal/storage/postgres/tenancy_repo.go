package postgres

import (
	"context"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenancyRepo implements tenancy.Resolver against Postgres.
type TenancyRepo struct {
	db *pgxpool.Pool
}

func NewTenancyRepo(db *pgxpool.Pool) *TenancyRepo {
	return &TenancyRepo{db: db}
}

// ByDomain resolves org by primary_domain.
func (r *TenancyRepo) ByDomain(ctx context.Context, domain string) (*tenancy.Tenant, error) {
	var t tenancy.Tenant
	var instanceID uuid.UUID

	err := r.db.QueryRow(ctx,
		`SELECT o.id, o.instance_id, o.primary_domain
		 FROM orgs o
		 WHERE o.primary_domain = $1 AND o.state = 1`,
		domain,
	).Scan(&t.OrgID, &instanceID, &t.Domain)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "tenant not found for domain")
		}
		return nil, fmt.Errorf("tenancy by domain: %w", err)
	}
	t.InstanceID = instanceID
	t.OrgSlug = domain
	return &t, nil
}

// ByOrgID resolves org by ID.
func (r *TenancyRepo) ByOrgID(ctx context.Context, orgID uuid.UUID) (*tenancy.Tenant, error) {
	var t tenancy.Tenant
	var instanceID uuid.UUID

	err := r.db.QueryRow(ctx,
		`SELECT o.id, o.instance_id, o.primary_domain
		 FROM orgs o
		 WHERE o.id = $1 AND o.state = 1`,
		orgID,
	).Scan(&t.OrgID, &instanceID, &t.Domain)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "tenant not found")
		}
		return nil, fmt.Errorf("tenancy by org_id: %w", err)
	}
	t.InstanceID = instanceID
	t.OrgSlug = t.Domain
	return &t, nil
}
