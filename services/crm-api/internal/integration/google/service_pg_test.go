package google

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/pgtest"
)

// The connect flow over real PostgreSQL.
//
// service_test.go runs the same flow on MemStore, and that is not enough on
// its own: MemStore round-trips whole Go structs, so a field the SQL schema
// never stores survives in memory and vanishes in production. This file
// exists because exactly that happened — the pending authorization carried an
// owner that no column held, so on Postgres a *successful* Google
// authorization ended in "the connection could not be saved".
//
// Any state the flow relies on between Start and Complete has to survive a
// real write and read. That is what these assert.

// migrationLockKey is the key internal/domain's Postgres tier takes
// exclusively while it applies migrations (pgstore_test.go). This package
// only reads the schema, so it holds the shared half for the length of each
// test — several readers may hold it together, and the writer waits.
const migrationLockKey = 0x63726D5F6D696701

func holdSchemaLock(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		pgtest.Unavailable(t,
			"skipping live Postgres test (cannot reach %s — run `make test-db-up`): %v", pgtest.DSN(), err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire for schema lock: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, int64(migrationLockKey)); err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("take shared schema lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock_shared($1)`, int64(migrationLockKey))
		conn.Release()
		pool.Close()
	})
}

// requireGoogleSchema fails with the command that fixes it when the database
// is reachable but has never had the migrations applied. This package runs no
// DDL of its own, so the schema only ever has one writer.
func requireGoogleSchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	defer pool.Close()

	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'google_connections' AND relkind = 'r')
		    AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'google_oauth_states' AND relkind = 'r')`).
		Scan(&ok); err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot read the catalog): %v", err)
	}
	if !ok {
		pgtest.Unavailable(t, "the Google integration tables are not present — run `make test-db-up`")
	}
}

func livePgService(t *testing.T, provider *stubProvider) (*Service, domain.Store) {
	t.Helper()
	holdSchemaLock(t)
	requireGoogleSchema(t)

	store, err := domain.NewPgStore(context.Background(), pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot open a store): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	svc, _ := newTestService(t, provider, "")
	return New(store, svc.oauth, ""), store
}

// pgScope keeps each run's rows to itself. The test database outlives the
// run, and the unique index on `state` is global by design.
func pgScope(label string) domain.Scope {
	stamp := time.Now().UnixNano()
	return domain.Scope{
		TenantID: fmt.Sprintf("tenant-google-pg-%s-%d", label, stamp),
		OrgID:    fmt.Sprintf("org-google-pg-%s-%d", label, stamp),
	}
}

// TestConnectFlowSurvivesARealWriteAndRead is the regression test for the
// dropped owner. It walks Start -> Complete against PostgreSQL and asserts
// the connection actually lands, attributed to the user who started it.
func TestConnectFlowSurvivesARealWriteAndRead(t *testing.T) {
	provider := newStubProvider()
	svc, store := livePgService(t, provider)
	ctx := context.Background()
	scope := pgScope("connect")

	authorizationURL, err := svc.Start(ctx, scope, "user-pg-1", "/settings/integrations")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", "")
	if !result.Connected {
		t.Fatalf("a successful authorization did not connect over PostgreSQL: reason %q", result.Reason)
	}
	if result.ReturnPath != "/settings/integrations" {
		t.Errorf("return path = %q; it did not survive the round trip", result.ReturnPath)
	}

	stored, err := store.GetGoogleConnection(ctx, scope, "user-pg-1")
	if err != nil {
		t.Fatalf("the connection was not stored against the user who started the flow: %v", err)
	}
	if stored.Status != domain.GoogleStatusConnected {
		t.Errorf("status = %q", stored.Status)
	}
	if stored.ConnectedBy != "user-pg-1" {
		t.Errorf("connected_by = %q, want user-pg-1", stored.ConnectedBy)
	}
	if stored.GoogleAccountEmail != "person@example.com" {
		t.Errorf("account email = %q", stored.GoogleAccountEmail)
	}

	// And the sealed credential opens, which it only can if the binding used
	// at Complete matches the one used to read it back.
	token, err := svc.AccessToken(ctx, scope, "user-pg-1")
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	if token != "access-1" {
		t.Errorf("access token = %q", token)
	}
}

// TestStateCannotBeReplayedOverPostgres re-runs the property that matters
// most against the atomic UPDATE that actually implements it, rather than
// against MemStore's mutex.
func TestStateCannotBeReplayedOverPostgres(t *testing.T) {
	provider := newStubProvider()
	svc, _ := livePgService(t, provider)
	ctx := context.Background()
	scope := pgScope("replay")

	authorizationURL, err := svc.Start(ctx, scope, "user-pg-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	state := stateFrom(t, authorizationURL)

	if first := svc.Complete(ctx, state, "auth-code", ""); !first.Connected {
		t.Fatalf("first callback: %q", first.Reason)
	}
	if second := svc.Complete(ctx, state, "auth-code", ""); second.Connected {
		t.Fatal("the same OAuth callback connected twice over PostgreSQL")
	}
}

// TestDisconnectOverPostgres walks the withdrawal path through real SQL,
// including the CHECK constraint that refuses a disconnected row still
// holding token material.
func TestDisconnectOverPostgres(t *testing.T) {
	provider := newStubProvider()
	svc, store := livePgService(t, provider)
	ctx := context.Background()
	scope := pgScope("disconnect")

	authorizationURL, err := svc.Start(ctx, scope, "user-pg-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}

	if _, err := svc.Disconnect(ctx, scope, "user-pg-1", "user-pg-1", false); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if provider.revokedToken != "refresh-1" {
		t.Errorf("revoked %q, want the refresh token", provider.revokedToken)
	}
	stored, err := store.GetGoogleConnection(ctx, scope, "user-pg-1")
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if len(stored.TokenCiphertext) != 0 {
		t.Error("the disconnected row still holds a sealed credential")
	}
}
