package domain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// defaultTestDSN is this repository's own disposable test instance —
// `make test-db-up`, compose service crm-postgres-test, tmpfs-backed, on a
// port of its own. It is a superuser connection so migration 0003 can create
// both crm_app and crm_drain, which the tenant-isolation tests connect as.
//
// The default has to name a database this repository owns. It previously
// named a port belonging to an unrelated local project, where it could only
// ever fail to connect — and a failed connection here is t.Skip, so the
// entire PostgreSQL tier, row-level security and cross-tenant proofs
// included, quietly did not run for anyone who had not set
// TEST_DATABASE_URL.
const defaultTestDSN = "postgres://postgres:crm_admin_test@localhost:15533/crm?sslmode=disable"

func livePgStore(t *testing.T) *PgStore {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		dsn = defaultTestDSN
	}
	s, err := NewPgStore(context.Background(), dsn)
	if err != nil {
		t.Skipf("skipping live Postgres test (cannot connect to %s): %v", dsn, err)
		return nil
	}
	t.Cleanup(func() { _ = s.Close() })

	applyMigrations(t, s)
	return s
}

func applyMigrations(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
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
		return
	}
	files, err := filepath.Glob(filepath.Join(migDir, "*.sql"))
	if err != nil {
		return
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read migration %s: %v", f, err)
		}
		if _, err := s.pool.Exec(ctx, string(b)); err != nil && !alreadyApplied(err) {
			t.Fatalf("apply migration %s: %v", f, err)
		}
	}
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
