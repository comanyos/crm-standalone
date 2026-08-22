// Package pgtest holds the two things the PostgreSQL test tier has to agree
// on: which database it talks to, and what happens when that database is not
// there.
//
// Both were duplicated per package before, which is how the tier came to be
// dead in CI without anyone noticing — see Unavailable's comment.
package pgtest

import (
	"os"
	"testing"
)

// DefaultDSN is this repository's own disposable test instance —
// `make test-db-up`, compose service crm-postgres-test, tmpfs-backed, on a
// port of its own. It is a superuser connection so migration 0003 can create
// both crm_app and crm_drain, which the tenant-isolation tests connect as.
const DefaultDSN = "postgres://postgres:crm_admin_test@localhost:15533/crm?sslmode=disable"

// RequireEnv, when set to anything non-empty, turns "the database is not
// reachable" from a skip into a failure.
const RequireEnv = "REQUIRE_LIVE_POSTGRES"

// DSN is the connection every test in this tier uses, honouring the two
// documented overrides (CONTRIBUTING.md) before falling back to the
// disposable instance.
func DSN() string {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return DefaultDSN
}

// Required reports whether an unreachable database must fail the run.
func Required() bool { return os.Getenv(RequireEnv) != "" }

// Unavailable ends a test that cannot reach its database.
//
// On a developer's machine that is a skip: not everyone has the test instance
// running, and failing their whole suite for it would be hostile.
//
// In CI it must not be. A skip is indistinguishable from a pass in every
// summary anyone reads, so a tier that skips is a tier that has silently
// stopped running — which is exactly what happened here: the workflow started
// no database, so every PostgreSQL test in the repository skipped on every
// pull request while CI reported green. Setting REQUIRE_LIVE_POSTGRES makes
// that condition loud instead.
func Unavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	if Required() {
		t.Fatalf(RequireEnv+" is set, so this cannot be skipped: "+format, args...)
	}
	t.Skipf(format, args...)
}
