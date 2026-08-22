package domain

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// setRLS binds the request's tenancy to the transaction running the statement.
func setRLS(ctx context.Context, tx pgx.Tx, scope Scope) error {
	if scope.TenantID == "" {
		return ErrNoTenant
	}
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, true)`,
		scope.TenantID,
	); err != nil {
		return fmt.Errorf("bind rls settings: %w", err)
	}
	return nil
}

// scopedDB is the query surface every PgStore method uses to guarantee RLS enforcement.
type scopedDB struct {
	pool *pgxpool.Pool
}

func newScopedDB(pool *pgxpool.Pool) *scopedDB {
	return &scopedDB{pool: pool}
}

// inTx runs fn in a transaction with the RLS settings bound.
func (d *scopedDB) inTx(
	ctx context.Context, scope Scope, opts pgx.TxOptions, fn func(pgx.Tx) error,
) error {
	tx, err := d.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := setRLS(ctx, tx, scope); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// begin starts a transaction with the tenancy already bound, for the callers
// that own a multi-statement change — the projection and outbox writes that
// have to land in the same transaction as the business row.
//
// The caller owns Rollback and Commit. A transaction started straight off the
// pool would compile and pass every test on a superuser connection, then return
// zero rows the moment the app connects as a role RLS actually applies to.
func (d *scopedDB) begin(ctx context.Context, scope Scope) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	if err := setRLS(ctx, tx, scope); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// Exec runs a statement with the settings bound.
func (d *scopedDB) Exec(
	ctx context.Context, scope Scope, sql string, args ...any,
) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := d.inTx(ctx, scope, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var e error
		tag, e = tx.Exec(ctx, sql, args...)
		return e
	})
	return tag, err
}

// Query runs a query whose rows outlive this call.
func (d *scopedDB) Query(
	ctx context.Context, scope Scope, sql string, args ...any,
) (pgx.Rows, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire: %w", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("begin: %w", err)
	}
	if err := setRLS(ctx, tx, scope); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		return nil, err
	}
	return &scopedRows{Rows: rows, tx: tx, conn: conn, ctx: ctx}, nil
}

// QueryRow runs a query returning at most one row.
func (d *scopedDB) QueryRow(
	ctx context.Context, scope Scope, sql string, args ...any,
) pgx.Row {
	rows, err := d.Query(ctx, scope, sql, args...)
	if err != nil {
		return errRow{err}
	}
	return &scopedSingleRow{rows: rows}
}

// scopedRows keeps the transaction and connection alive for the reader.
type scopedRows struct {
	pgx.Rows
	tx     pgx.Tx
	conn   *pgxpool.Conn
	ctx    context.Context
	closed bool
}

// Close finishes the read and releases the connection.
func (r *scopedRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()

	if r.Err() != nil {
		_ = r.tx.Rollback(r.ctx)
	} else {
		_ = r.tx.Commit(r.ctx)
	}
	r.conn.Release()
}

// scopedSingleRow adapts Rows to the pgx.Row contract.
type scopedSingleRow struct {
	rows pgx.Rows
}

func (r *scopedSingleRow) Scan(dest ...any) error {
	defer r.rows.Close()

	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	return r.rows.Err()
}

type errRow struct{ err error }

func (r errRow) Scan(_ ...any) error { return r.err }
