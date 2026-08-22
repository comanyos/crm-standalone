package domain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/santoshguru/crm/internal/pgtest"
)

// alreadyApplied reports whether an error is only the migration having run
// before. Everything else fails the test: swallowing every error here meant a
// migration that never applied still left the suite green, which is how a
// column can be "added" in a file and absent from the database.
func alreadyApplied(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P07", // duplicate_table
		"42710", // duplicate_object
		"42701", // duplicate_column
		"42P16", // invalid_table_definition
		"42704", // undefined_object
		"42703": // undefined_column — a RENAME that already happened
		return true
	}
	return false
}

// testDSN is the connection every test in this tier uses. The policy — the
// two documented overrides and the disposable instance they fall back to —
// lives in internal/pgtest, shared with internal/server's Postgres tier so
// the two cannot drift onto different databases.
func testDSN() string { return pgtest.DSN() }

func livePgStore(t *testing.T) *PgStore {
	t.Helper()
	dsn := testDSN()
	s, err := NewPgStore(context.Background(), dsn)
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect to %s): %v", dsn, err)
		return nil
	}
	t.Cleanup(func() { _ = s.Close() })

	applyMigrations(t, s)
	return s
}

// migrationLockKey is an arbitrary but fixed advisory-lock key, shared with
// internal/server's Postgres tier (revenue_workflow_pg_test.go).
//
// `go test ./...` runs each package's tests in its own process, at the same
// time, and two of them now use this database. This package is the only one
// that brings the schema up to date, and the DDL it runs takes ACCESS
// EXCLUSIVE locks on tables the other package is querying — which deadlocks,
// nondeterministically, if the two overlap.
//
// So the two take opposite halves of one advisory lock: this package takes it
// exclusively while it applies migrations, and internal/server holds it in
// shared mode for the length of each of its Postgres-backed tests. Neither
// waits on the other for long — migrations are applied once per process, and
// those tests are a fraction of a second each.
const migrationLockKey = 0x63726D5F6D696701

// migrationsOnce keeps the schema work to one burst per test binary. It used
// to run on every livePgStore call, which was harmless while this was the
// only package touching the database and is not any more.
var migrationsOnce sync.Once

func applyMigrations(t *testing.T, s *PgStore) {
	t.Helper()
	var failure error
	migrationsOnce.Do(func() { failure = applyMigrationsOnce(s) })
	if failure != nil {
		t.Fatalf("apply migrations: %v", failure)
	}
}

func applyMigrationsOnce(s *PgStore) error {
	ctx := context.Background()

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire for migration lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(migrationLockKey)); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(migrationLockKey))
	}()

	candidates := []string{
		"../../migrations",
		"../../../migrations",
		"migrations",
	}
	var migDir string
	for _, c := range candidates {
		if stat, err := os.Stat(c); err == nil && stat.IsDir() {
			migDir = c
			break
		}
	}
	if migDir == "" {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(migDir, "*.sql"))
	if err != nil {
		return nil
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := conn.Exec(ctx, string(b)); err != nil {
			if !alreadyApplied(err) {
				return fmt.Errorf("apply migration %s: %w", f, err)
			}
			// The migrations wrap themselves in BEGIN/COMMIT, so a tolerated
			// error leaves this session holding an aborted transaction that
			// would reject every statement after it. Clear it before the next
			// file — the advisory lock has to stay held, so taking a fresh
			// connection instead is not an option.
			if _, rberr := conn.Exec(ctx, `ROLLBACK`); rberr != nil {
				return fmt.Errorf("clear aborted transaction after %s: %w", f, rberr)
			}
		}
	}
	return nil
}

// TestPgStoreNewFieldsRoundTripAndTenantIsolation proves Account.ParentID and
// Opportunity.CloseReason persist through Postgres and that tenant B still
// cannot read tenant A's rows now that the store routes through scopedDB.
func TestPgStoreNewFieldsRoundTripAndTenantIsolation(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-crm-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-crm-b-%d", stamp), OrgID: "org-b"}

	parent, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Parent Co", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create parent account: %v", err)
	}
	child, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Child Co", OwnerID: "owner-a", ParentID: parent.ID})
	if err != nil {
		t.Fatalf("create child account: %v", err)
	}

	got, err := s.GetAccount(ctx, tenantA, child.ID)
	if err != nil {
		t.Fatalf("get child account: %v", err)
	}
	if got.ParentID != parent.ID {
		t.Fatalf("ParentID = %q, want %q", got.ParentID, parent.ID)
	}

	if _, err := s.GetAccount(ctx, tenantB, child.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("tenant B GetAccount err = %v; want ErrNotFound", err)
	}

	oppAccount, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Big Deal Co", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("create opportunity's account: %v", err)
	}
	opp, err := s.CreateOpportunity(ctx, tenantA, &Opportunity{Name: "Big Deal", AccountID: oppAccount.ID})
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	opp.CloseReason = "lost-to-competitor"
	updated, err := s.UpdateOpportunity(ctx, tenantA, opp)
	if err != nil {
		t.Fatalf("update opportunity: %v", err)
	}
	if updated.CloseReason != "lost-to-competitor" {
		t.Fatalf("CloseReason = %q, want %q", updated.CloseReason, "lost-to-competitor")
	}

	gotOpp, err := s.GetOpportunity(ctx, tenantA, opp.ID)
	if err != nil {
		t.Fatalf("get opportunity: %v", err)
	}
	if gotOpp.CloseReason != "lost-to-competitor" {
		t.Fatalf("persisted CloseReason = %q, want %q", gotOpp.CloseReason, "lost-to-competitor")
	}

	if _, err := s.GetOpportunity(ctx, tenantB, opp.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("tenant B GetOpportunity err = %v; want ErrNotFound", err)
	}
}
