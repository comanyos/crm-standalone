package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/authincore/authincore/internal/identity/schema"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SchemaLoader implements schema.Loader by reading from identity_schemas table.
type SchemaLoader struct{ db *pgxpool.Pool }

func NewSchemaLoader(db *pgxpool.Pool) *SchemaLoader { return &SchemaLoader{db: db} }

func (s *SchemaLoader) Load(ctx context.Context, orgID, schemaID string) (*schema.Schema, error) {
	var rawSchema []byte
	var version int
	err := s.db.QueryRow(ctx,
		`SELECT schema, version FROM identity_schemas WHERE org_id=$1 AND id=$2`,
		orgID, schemaID).Scan(&rawSchema, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Fallback: return a permissive schema so orgs without explicit schemas work.
			return &schema.Schema{
				ID:      schemaID,
				OrgID:   orgID,
				Version: 1,
				Raw:     json.RawMessage(`{"type":"object"}`),
			}, nil
		}
		return nil, fmt.Errorf("load schema %s/%s: %w", orgID, schemaID, err)
	}
	return &schema.Schema{
		ID:      schemaID,
		OrgID:   orgID,
		Version: version,
		Raw:     json.RawMessage(rawSchema),
	}, nil
}
