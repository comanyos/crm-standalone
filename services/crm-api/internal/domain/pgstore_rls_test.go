package domain

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshguru/crm/internal/pgtest"
)

// restrictedDSN rewrites the test DSN to connect as crm_app — the role
// created by migration 0003 that does NOT bypass row-level security.
//
// This indirection is the whole point of the file. The default test
// connection is a superuser that also owns the tables (see defaultTestDSN),
// and a superuser is exempt from RLS outright — so a test against the
// ordinary store proves nothing about whether the policies actually apply.
// This connects as the role the policies are meant to bind.
func restrictedDSN(t *testing.T) string {
	t.Helper()
	dsn := testDSN()
	at := strings.Index(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 {
		pgtest.Unavailable(t, "cannot derive a restricted DSN from %q", dsn)
	}
	return dsn[:scheme+3] + "crm_app:crm_app" + dsn[at:]
}

func restrictedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, restrictedDSN(t))
	if err != nil {
		pgtest.Unavailable(t, "skipping RLS test (cannot build restricted pool): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		pgtest.Unavailable(t, "skipping RLS test (role crm_app cannot connect — migration 0003 needs a superuser): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// countAccounts runs one SELECT on the restricted connection, binding
// tenantID the way scopedDB does. An empty tenantID means "bind nothing",
// which is the shape of a code path that forgot to set the scope.
func countAccounts(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenantID, id string) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin on restricted pool: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if tenantID != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatalf("set app.tenant_id: %v", err)
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return n
}

// TestRLSIsEnforcedForTheApplicationRole proves the policies actually filter,
// rather than merely existing.
func TestRLSIsEnforcedForTheApplicationRole(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	pool := restrictedPool(t)

	stamp := time.Now().UnixNano()
	tenantA := Scope{TenantID: fmt.Sprintf("tenant-rls-a-%d", stamp), OrgID: "org-a"}
	tenantB := Scope{TenantID: fmt.Sprintf("tenant-rls-b-%d", stamp), OrgID: "org-b"}

	acc, err := s.CreateAccount(ctx, tenantA, &Account{Name: "Widgets Inc", OwnerID: "owner-a"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if n := countAccounts(ctx, t, pool, tenantA.TenantID, acc.ID); n != 1 {
		t.Errorf("tenant A sees %d rows of its own account; want 1", n)
	}
	if n := countAccounts(ctx, t, pool, tenantB.TenantID, acc.ID); n != 0 {
		t.Errorf("tenant B sees %d of tenant A's rows; want 0 — RLS is not filtering", n)
	}
	// The important one: no tenant bound at all. Under a working policy this is
	// zero rows. Under a policy that is enabled but not FORCEd, or on a role
	// with BYPASSRLS, it is every row in the table.
	if n := countAccounts(ctx, t, pool, "", acc.ID); n != 0 {
		t.Errorf("an unscoped connection sees %d rows; want 0 — the policy is not being applied", n)
	}
}

// TestRLSRejectsWritingIntoAnotherTenant proves the WITH CHECK half. USING
// filters reads; without WITH CHECK a connection scoped to tenant B could
// still INSERT rows labelled tenant A.
func TestRLSRejectsWritingIntoAnotherTenant(t *testing.T) {
	_ = livePgStore(t)
	ctx := context.Background()
	pool := restrictedPool(t)

	stamp := time.Now().UnixNano()
	victim := fmt.Sprintf("tenant-rls-victim-%d", stamp)
	attacker := fmt.Sprintf("tenant-rls-attacker-%d", stamp)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, attacker); err != nil {
		t.Fatalf("set app.tenant_id: %v", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO accounts (tenant_id, id, name, created_at, updated_at)
		 VALUES ($1, $2, 'Planted', now(), now())`,
		victim, fmt.Sprintf("planted-%d", stamp))
	if err == nil {
		t.Fatal("a connection scoped to one tenant inserted a row belonging to another; WITH CHECK is not enforced")
	}
}
