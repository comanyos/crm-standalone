package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type txContextKey struct{}

// WithTx makes a pgx transaction available to repositories participating in a
// command. It is intentionally internal to the process and cannot be supplied
// by request data.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txContextKey{}, tx)
}

func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return tx, ok && tx != nil
}

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func ExecutorFor(ctx context.Context, pool *pgxpool.Pool) Executor {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return pool
}
