package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/domain/identity"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityRepo implements command.IdentityRepository against Postgres.
type IdentityRepo struct {
	db *pgxpool.Pool
}

func NewIdentityRepo(db *pgxpool.Pool) *IdentityRepo {
	return &IdentityRepo{db: db}
}

func (r *IdentityRepo) Insert(ctx context.Context, i *identity.Identity) error {
	traits, err := json.Marshal(i.Traits)
	if err != nil {
		return fmt.Errorf("identity insert marshal traits: %w", err)
	}
	metaPub, _ := json.Marshal(i.MetadataPublic)
	metaAdmin, _ := json.Marshal(i.MetadataAdmin)

	_, err = database.ExecutorFor(ctx, r.db).Exec(ctx,
		`INSERT INTO identities
		   (id, org_id, external_id, schema_id, traits, state, available_aal,
		    metadata_public, metadata_admin, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		i.ID, i.OrgID, i.ExternalID, i.SchemaID, traits, int(i.State), int(i.AvailableAAL),
		metaPub, metaAdmin, i.CreatedAt, i.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domainerr.New(domainerr.CodeAlreadyExists, "identity already exists")
		}
		return fmt.Errorf("identity insert: %w", err)
	}
	return nil
}

func (r *IdentityRepo) Update(ctx context.Context, i *identity.Identity) error {
	traits, err := json.Marshal(i.Traits)
	if err != nil {
		return fmt.Errorf("identity update marshal traits: %w", err)
	}
	metaPub, _ := json.Marshal(i.MetadataPublic)
	metaAdmin, _ := json.Marshal(i.MetadataAdmin)

	ct, err := database.ExecutorFor(ctx, r.db).Exec(ctx,
		`UPDATE identities
		 SET schema_id=$1, traits=$2, state=$3, available_aal=$4,
		     metadata_public=$5, metadata_admin=$6, updated_at=$7
		 WHERE id=$8 AND org_id=$9`,
		i.SchemaID, traits, int(i.State), int(i.AvailableAAL),
		metaPub, metaAdmin, time.Now().UTC(),
		i.ID, i.OrgID,
	)
	if err != nil {
		return fmt.Errorf("identity update: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeNotFound, "identity not found")
	}
	return nil
}

func (r *IdentityRepo) Delete(ctx context.Context, id, orgID uuid.UUID) error {
	ct, err := database.ExecutorFor(ctx, r.db).Exec(ctx,
		`DELETE FROM identities WHERE id=$1 AND org_id=$2`, id, orgID,
	)
	if err != nil {
		return fmt.Errorf("identity delete: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return domainerr.New(domainerr.CodeNotFound, "identity not found")
	}
	return nil
}

func (r *IdentityRepo) ByID(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	row := database.ExecutorFor(ctx, r.db).QueryRow(ctx,
		`SELECT id, org_id, external_id, schema_id, traits, state,
		        available_aal, metadata_public, metadata_admin, created_at, updated_at
		 FROM identities WHERE id=$1 AND org_id=$2`,
		id, orgID,
	)
	return scanIdentity(row)
}

// FindOrCreateByEmail looks up an identity by email trait within an org.
// If none exists it creates a stub identity with that email in the traits.
// Returns the identity ID in both cases.
func (r *IdentityRepo) FindOrCreateByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error) {
	var id uuid.UUID
	err := database.ExecutorFor(ctx, r.db).QueryRow(ctx,
		`SELECT id FROM identities
		 WHERE org_id=$1 AND traits @> jsonb_build_object('email', $2::text)
		 LIMIT 1`,
		orgID, email,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find identity by email: %w", err)
	}

	// Create stub identity.
	id = uuid.New()
	traits, _ := json.Marshal(map[string]string{"email": email})
	_, err = database.ExecutorFor(ctx, r.db).Exec(ctx,
		`INSERT INTO identities (id, org_id, schema_id, traits, state, available_aal)
		 VALUES ($1,$2,'default',$3,'active',1)`,
		id, orgID, traits,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create identity by email: %w", err)
	}
	return id, nil
}

func scanIdentity(row pgx.Row) (*identity.Identity, error) {
	i := &identity.Identity{}
	var traits, metaPub, metaAdmin []byte
	if err := row.Scan(
		&i.ID, &i.OrgID, &i.ExternalID, &i.SchemaID, &traits, &i.State,
		&i.AvailableAAL, &metaPub, &metaAdmin, &i.CreatedAt, &i.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "identity not found")
		}
		return nil, fmt.Errorf("scan identity: %w", err)
	}
	i.Traits = json.RawMessage(traits)
	i.MetadataPublic = json.RawMessage(metaPub)
	i.MetadataAdmin = json.RawMessage(metaAdmin)
	return i, nil
}
