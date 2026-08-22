package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/domain/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type IdentitySearchQuery struct {
	OrgID    uuid.UUID
	Query    string
	PageSize int
	Page     int
}

func (q *Queries) GetIdentity(ctx context.Context, id, orgID uuid.UUID) (*identity.Identity, error) {
	row := q.db.QueryRow(ctx,
		`SELECT id, org_id, external_id, schema_id, traits, state,
		        available_aal, metadata_public, metadata_admin, created_at, updated_at
		 FROM identities WHERE id = $1 AND org_id = $2`,
		id, orgID,
	)
	i, err := scanIdentityRow(row)
	if err != nil {
		return nil, err
	}
	return i, nil
}

func (q *Queries) ListIdentities(ctx context.Context, orgID uuid.UUID, pageSize int, pageToken string) ([]*identity.Identity, string, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id, external_id, schema_id, traits, state,
		        available_aal, metadata_public, metadata_admin, created_at, updated_at
		 FROM identities
		 WHERE org_id = $1 AND ($2 = '' OR id > $2::uuid)
		 ORDER BY id ASC
		 LIMIT $3`,
		orgID, pageToken, pageSize+1,
	)
	if err != nil {
		return nil, "", fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()

	var out []*identity.Identity
	for rows.Next() {
		i, err := scanIdentityRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list identities scan: %w", err)
	}

	nextToken := ""
	if len(out) > pageSize {
		nextToken = out[pageSize-1].ID.String()
		out = out[:pageSize]
	}
	return out, nextToken, nil
}

func (q *Queries) SearchIdentities(ctx context.Context, sq IdentitySearchQuery) ([]*identity.Identity, int64, error) {
	offset := sq.Page * sq.PageSize
	if sq.PageSize <= 0 {
		sq.PageSize = 50
	}
	// Use GIN-indexed trigram search when a query string is present.
	// Falls back to full-table for empty query (list all).
	rows, err := q.db.Query(ctx,
		`SELECT id, org_id, external_id, schema_id, traits, state,
		        available_aal, metadata_public, metadata_admin, created_at, updated_at
		 FROM identities
		 WHERE org_id = $1
		   AND ($2 = '' OR traits::text % $2 OR traits::text ILIKE '%' || $2 || '%')
		 ORDER BY created_at DESC
		 LIMIT $3 OFFSET $4`,
		sq.OrgID, sq.Query, sq.PageSize, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("search identities: %w", err)
	}
	defer rows.Close()

	var out []*identity.Identity
	for rows.Next() {
		i, err := scanIdentityRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("search identities scan: %w", err)
	}

	var total int64
	_ = q.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM identities WHERE org_id = $1
		 AND ($2 = '' OR traits::text % $2 OR traits::text ILIKE '%' || $2 || '%')`,
		sq.OrgID, sq.Query,
	).Scan(&total)

	return out, total, nil
}

type scanner interface {
	Scan(...any) error
}

func scanIdentityRow(row scanner) (*identity.Identity, error) {
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
