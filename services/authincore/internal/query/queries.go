// Package query is the CQRS read side. All reads go through here.
// Read models are relational projections — authoritative, fast.
package query

import (
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queries is the DI root for all read-side operations.
type Queries struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Queries {
	return &Queries{db: db}
}
