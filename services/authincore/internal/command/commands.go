// Package command is the CQRS write side. All state mutations go through here.
// Handlers validate intent, write to relational tables (system of record),
// and append audit events — atomically.
package command

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityRepository is the minimal write-side interface for identity persistence.
type IdentityRepository interface {
	identityWriter
	identityReader
}

func withTransaction[T any](ctx context.Context, db *pgxpool.Pool, fn func(context.Context) (T, error)) (T, error) {
	if db == nil { // unit-test fakes do not require a database transaction
		return fn(ctx)
	}
	var zero T
	tx, err := db.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin command transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := fn(database.WithTx(ctx, tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit command transaction: %w", err)
	}
	return result, nil
}

// SessionRepository is the minimal write-side interface for session persistence.
type SessionRepository interface {
	sessionWriter
}

// SchemaValidator validates identity traits against an org's schema.
type SchemaValidator interface {
	Validate(ctx context.Context, orgID, schemaID string, traits json.RawMessage) error
}

// Commands is the DI root for all write-side operations.
type Commands struct {
	db         *pgxpool.Pool
	es         eventstore.Eventstore
	identities IdentityRepository
	sessions   SessionRepository
	schema     SchemaValidator
}

func New(
	db *pgxpool.Pool,
	es eventstore.Eventstore,
	identities IdentityRepository,
	sessions SessionRepository,
) *Commands {
	return &Commands{
		db:         db,
		es:         es,
		identities: identities,
		sessions:   sessions,
	}
}

// WithSchemaValidator attaches a schema validator (optional).
func (c *Commands) WithSchemaValidator(v SchemaValidator) *Commands {
	c.schema = v
	return c
}
