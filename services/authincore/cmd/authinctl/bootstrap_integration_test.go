package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	passwordstrategy "github.com/authincore/authincore/internal/flow/strategy/password"
	"github.com/authincore/authincore/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql, used by goose
	"github.com/pressly/goose/v3"
)

// Bootstrap lifecycle integration tests (task.md §1.1).
//
// These run against a real, migrated AuthinCore database rather than a mock,
// because everything worth checking here is a property of the SQL: that a
// second invocation inserts nothing, that two organizations sharing an
// administrator's email address and a client id stay separate, and that a
// re-run never quietly rewrites a password. None of those can fail in a fake.
//
// The suite is written so that every test owns its own organization domain,
// rather than depending on the database being empty when it starts. A test
// that only passes as the first one to run is a test that will start failing
// for reasons unrelated to what it checks.

// defaultAuthTestDSN is this repository's own disposable AuthinCore instance:
// `make test-db-up`, compose service auth-postgres-test, tmpfs-backed, on a
// port of its own. Overridable with AUTH_TEST_DATABASE_URL.
const defaultAuthTestDSN = "postgres://authincore:authincore_test@localhost:15532/authincore?sslmode=disable"

// migrateOnce guards the schema setup so parallel packages do not race goose.
var migrated bool

// liveAuthDB returns a pool against a migrated AuthinCore database.
//
// It skips when the database is unreachable, which is the only sensible thing
// to do — but a skip here silently removes every check in this file, so run
// `go test -v` and look for SKIP before treating a green run as proof. See
// CONTRIBUTING.md's test tiers.
func liveAuthDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("AUTH_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultAuthTestDSN
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("skipping bootstrap integration test (cannot build pool for %s): %v", dsn, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skipping bootstrap integration test (cannot reach %s — run `make test-db-up`): %v", dsn, err)
	}
	t.Cleanup(pool.Close)

	if !migrated {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatalf("open database for migrations: %v", err)
		}
		defer func() { _ = db.Close() }()
		goose.SetBaseFS(migrations.FS)
		goose.SetLogger(goose.NopLogger())
		if err := goose.SetDialect("postgres"); err != nil {
			t.Fatalf("set goose dialect: %v", err)
		}
		if err := goose.Up(db, "."); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}
		migrated = true
	}
	return pool
}

// testConfig builds a bootstrap config whose organization domain is unique to
// this test, so tests neither collide nor depend on running first.
func testConfig(t *testing.T, label string) bootstrapConfig {
	t.Helper()
	stamp := time.Now().UnixNano()
	cfg, err := bootstrapConfigFromEnv(envMap{
		"BOOTSTRAP_ORG_NAME":       label + " Industries",
		"BOOTSTRAP_ORG_DOMAIN":     fmt.Sprintf("%s-%d.example", label, stamp),
		"BOOTSTRAP_ADMIN_NAME":     label + " Administrator",
		"BOOTSTRAP_ADMIN_EMAIL":    fmt.Sprintf("admin@%s-%d.example", label, stamp),
		"BOOTSTRAP_ADMIN_PASSWORD": "correct-horse-battery-staple",
		"CRM_WEB_CLIENT_ID":        "crm-web",
		"CRM_WEB_REDIRECT_URI":     "http://localhost:3000/auth/callback",
	}.get)
	if err != nil {
		t.Fatalf("build bootstrap config: %v", err)
	}
	return cfg
}

// envMap adapts a map to the getenv signature bootstrapConfigFromEnv takes.
type envMap map[string]string

func (m envMap) get(key string) string { return m[key] }

// orgCounts is every row a bootstrap is responsible for, counted per
// organization. Comparing the whole struct rather than one table at a time is
// what makes the idempotency test meaningful: a duplicate anywhere shows up.
//
// The role, resource and permission counts are deliberately restricted to
// what bootstrap provisions. They are not raw table counts, because this
// database also seeds roles for an unrelated product — see
// TestOrganizationsCarryRolesForAnUnrelatedProduct, which pins that
// separately. Counting the whole table here would quietly make these tests
// depend on how many DBCore roles happen to exist, which is not something
// bootstrap should have an opinion about either way.
type orgCounts struct {
	Organizations   int
	Identities      int
	Credentials     int
	Roles           int
	Resources       int
	Permissions     int
	RoleAssignments int
	Projects        int
	Applications    int
}

// bootstrapRoleKeys and bootstrapResourceTypes name what bootstrap owns.
func bootstrapRoleKeys() []string {
	out := make([]string, 0, len(crmRoleDefinitions))
	for _, definition := range crmRoleDefinitions {
		out = append(out, definition.Key)
	}
	return out
}

func bootstrapResourceTypes() []string {
	seen := map[string]bool{}
	var out []string
	for _, definition := range crmRoleDefinitions {
		if !seen[definition.ResourceType] {
			seen[definition.ResourceType] = true
			out = append(out, definition.ResourceType)
		}
	}
	return out
}

func countFor(ctx context.Context, t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) orgCounts {
	t.Helper()
	roleKeys := bootstrapRoleKeys()
	resourceTypes := bootstrapResourceTypes()

	var c orgCounts
	queries := []struct {
		into *int
		sql  string
		args []any
	}{
		{&c.Organizations, `SELECT count(*) FROM orgs WHERE id=$1`, nil},
		{&c.Identities, `SELECT count(*) FROM identities WHERE org_id=$1`, nil},
		{&c.Credentials, `SELECT count(*) FROM identity_credentials c JOIN identities i ON i.id=c.identity_id WHERE i.org_id=$1`, nil},
		{&c.Roles, `SELECT count(*) FROM roles WHERE org_id=$1 AND key = ANY($2)`, []any{roleKeys}},
		{&c.Resources, `SELECT count(*) FROM resources WHERE org_id=$1 AND type = ANY($2)`, []any{resourceTypes}},
		{&c.Permissions, `SELECT count(*) FROM permissions p
		                   JOIN roles r ON r.id = p.role_id
		                   JOIN resources res ON res.id = p.resource_id
		                  WHERE p.org_id=$1 AND r.key = ANY($2) AND res.type = ANY($3)`,
			[]any{roleKeys, resourceTypes}},
		{&c.RoleAssignments, `SELECT count(*) FROM role_assignments WHERE org_id=$1`, nil},
		{&c.Projects, `SELECT count(*) FROM projects WHERE org_id=$1`, nil},
		{&c.Applications, `SELECT count(*) FROM applications WHERE org_id=$1`, nil},
	}
	for _, q := range queries {
		args := append([]any{orgID}, q.args...)
		if err := pool.QueryRow(ctx, q.sql, args...).Scan(q.into); err != nil {
			t.Fatalf("count (%s): %v", q.sql, err)
		}
	}
	return c
}

// TestBootstrapOnACleanDatabaseCreatesTheWholeModel checks that one
// invocation leaves an organization somebody can actually log into: not just
// an orgs row, but the credential, the role model, the grants, and the public
// OAuth client the web app authenticates against.
func TestBootstrapOnACleanDatabaseCreatesTheWholeModel(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "clean")

	result, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !result.CreatedOrganization || !result.CreatedAdministrator {
		t.Errorf("first bootstrap reported created_organization=%v created_administrator=%v; want both true",
			result.CreatedOrganization, result.CreatedAdministrator)
	}
	orgID := uuid.MustParse(result.OrganizationID)

	got := countFor(ctx, t, pool, orgID)
	want := orgCounts{
		Organizations: 1, Identities: 1, Credentials: 1,
		// owner + the four CRM roles.
		Roles: len(crmRoleDefinitions),
		// authincore:administration and crm:crm.
		Resources: 2,
		// One permission row per bootstrap role, on a bootstrap resource.
		Permissions: len(crmRoleDefinitions),
		// assignBootstrapRoles grants owner and crm_admin.
		RoleAssignments: 2,
		Projects:        1, Applications: 1,
	}
	if got != want {
		t.Errorf("row counts after bootstrap = %+v, want %+v", got, want)
	}

	// The administrator can be found the way a login would find them.
	var identityID uuid.UUID
	err = pool.QueryRow(ctx,
		`SELECT i.id FROM identities i
		   JOIN identity_credentials c ON c.identity_id = i.id
		  WHERE i.org_id=$1 AND c.type='password' AND c.identifiers @> jsonb_build_array($2::text)`,
		orgID, cfg.AdminEmail).Scan(&identityID)
	if err != nil {
		t.Fatalf("the bootstrapped administrator is not resolvable by email: %v", err)
	}
	if identityID.String() != result.AdministratorID {
		t.Errorf("resolved administrator %s, result reported %s", identityID, result.AdministratorID)
	}

	// The organization is active, not merely present.
	var state int
	var domain string
	if err := pool.QueryRow(ctx, `SELECT state, primary_domain FROM orgs WHERE id=$1`, orgID).
		Scan(&state, &domain); err != nil {
		t.Fatalf("read organization: %v", err)
	}
	if state != 1 {
		t.Errorf("organization state = %d, want 1 (active)", state)
	}
	if domain != cfg.OrgDomain {
		t.Errorf("organization domain = %q, want the canonicalized %q", domain, cfg.OrgDomain)
	}

	// An audit event exists for both creations — bootstrap is an
	// administrative action and must not be invisible afterwards.
	for _, want := range []struct{ eventType, aggregate string }{
		{"org.created", "org"},
		{"identity.created", "identity"},
	} {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM events WHERE org_id=$1 AND event_type=$2 AND aggregate_type=$3`,
			orgID, want.eventType, want.aggregate).Scan(&n); err != nil {
			t.Fatalf("count %s events: %v", want.eventType, err)
		}
		if n != 1 {
			t.Errorf("%d %s events, want 1", n, want.eventType)
		}
	}
}

// TestSecondBootstrapCreatesNoDuplicates is the idempotency proof. Re-running
// bootstrap is the normal way an operator applies a change to the role model,
// so "it inserts a second organization" would be discovered in production.
func TestSecondBootstrapCreatesNoDuplicates(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "idempotent")

	first, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	orgID := uuid.MustParse(first.OrganizationID)
	before := countFor(ctx, t, pool, orgID)

	second, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	if second.OrganizationID != first.OrganizationID {
		t.Errorf("second bootstrap reported organization %s, first reported %s",
			second.OrganizationID, first.OrganizationID)
	}
	if second.AdministratorID != first.AdministratorID {
		t.Errorf("second bootstrap reported administrator %s, first reported %s",
			second.AdministratorID, first.AdministratorID)
	}
	if second.CreatedOrganization || second.CreatedAdministrator {
		t.Errorf("second bootstrap reported created_organization=%v created_administrator=%v; want both false",
			second.CreatedOrganization, second.CreatedAdministrator)
	}

	if after := countFor(ctx, t, pool, orgID); after != before {
		t.Errorf("a second bootstrap changed row counts:\n  before %+v\n  after  %+v", before, after)
	}

	// A third, to catch anything that only duplicates on an even run.
	if _, err := bootstrap(ctx, pool, cfg); err != nil {
		t.Fatalf("third bootstrap: %v", err)
	}
	if after := countFor(ctx, t, pool, orgID); after != before {
		t.Errorf("a third bootstrap changed row counts:\n  before %+v\n  after  %+v", before, after)
	}

	// Duplicate audit events would misrepresent the organization's history
	// even though no entity was duplicated.
	var events int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE org_id=$1 AND event_type='org.created'`, orgID).Scan(&events); err != nil {
		t.Fatalf("count org.created events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d org.created events after three bootstraps, want 1", events)
	}
}

// TestBootstrapNeverResetsAnExistingAdministratorPassword is the safety
// property behind task.md §1.1's "never silently reset on normal bootstrap".
//
// An operator re-running bootstrap to pick up a role change must not thereby
// change the administrator's password to whatever value happens to be in the
// environment — which, in a shell that still has the original bootstrap
// exported, would be silent and invisible, and in a shell that does not would
// lock the administrator out.
func TestBootstrapNeverResetsAnExistingAdministratorPassword(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "nopwreset")

	first, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	identityID := uuid.MustParse(first.AdministratorID)

	readHash := func() string {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(ctx,
			`SELECT config FROM identity_credentials WHERE identity_id=$1 AND type='password'`,
			identityID).Scan(&raw); err != nil {
			t.Fatalf("read credential: %v", err)
		}
		var cred struct {
			HashedPassword string `json:"hashed_password"`
		}
		if err := json.Unmarshal(raw, &cred); err != nil {
			t.Fatalf("parse credential config: %v", err)
		}
		return cred.HashedPassword
	}

	originalHash := readHash()
	if originalHash == "" {
		t.Fatal("the bootstrapped administrator has no password hash")
	}

	// Re-run with a different password in the environment.
	changed := cfg
	changed.AdminPassword = "an-entirely-different-password"
	if _, err := bootstrap(ctx, pool, changed); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	if readHash() != originalHash {
		t.Fatal("a normal bootstrap rewrote the administrator's password hash")
	}

	hasher := passwordstrategy.NewArgon2idHasher()
	ok, err := hasher.Verify(cfg.AdminPassword, originalHash)
	if err != nil || !ok {
		t.Errorf("the original password no longer verifies (ok=%v err=%v)", ok, err)
	}
	ok, err = hasher.Verify(changed.AdminPassword, originalHash)
	if err == nil && ok {
		t.Error("the password supplied to the second bootstrap now authenticates; " +
			"bootstrap silently reset the credential")
	}
}

// TestTwoOrganizationsBootstrapIndependently is the fixture task.md §1.3 asked
// for and could not build from the CRM side: two real organizations, each with
// its own administrator, role model and OAuth client, provisioned by the real
// bootstrap path.
//
// They deliberately share an administrator email address and a client id.
// Those are the two values most likely to have been given a global unique
// constraint by accident, and either would make it impossible for two
// customers to use the same deployment.
func TestTwoOrganizationsBootstrapIndependently(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	sharedEmail := fmt.Sprintf("admin+shared-%d@example.com", stamp)

	build := func(label string) bootstrapConfig {
		cfg, err := bootstrapConfigFromEnv(envMap{
			"BOOTSTRAP_ORG_NAME":       label,
			"BOOTSTRAP_ORG_DOMAIN":     fmt.Sprintf("%s-%d.example", label, stamp),
			"BOOTSTRAP_ADMIN_EMAIL":    sharedEmail,
			"BOOTSTRAP_ADMIN_PASSWORD": "correct-horse-battery-staple",
			"CRM_WEB_CLIENT_ID":        "crm-web",
			"CRM_WEB_REDIRECT_URI":     "http://localhost:3000/auth/callback",
		}.get)
		if err != nil {
			t.Fatalf("config for %s: %v", label, err)
		}
		return cfg
	}

	alpha, err := bootstrap(ctx, pool, build("alpha"))
	if err != nil {
		t.Fatalf("bootstrap alpha: %v", err)
	}
	beta, err := bootstrap(ctx, pool, build("beta"))
	if err != nil {
		t.Fatalf("bootstrap beta — two organizations cannot share an administrator "+
			"email address or a client id: %v", err)
	}

	if alpha.OrganizationID == beta.OrganizationID {
		t.Fatal("both bootstraps resolved to the same organization")
	}
	if alpha.AdministratorID == beta.AdministratorID {
		t.Error("both organizations share one administrator identity; an account in one " +
			"customer's organization would be an account in the other's")
	}

	alphaID := uuid.MustParse(alpha.OrganizationID)
	betaID := uuid.MustParse(beta.OrganizationID)

	// Each organization owns a complete, separate model.
	for name, id := range map[string]uuid.UUID{"alpha": alphaID, "beta": betaID} {
		got := countFor(ctx, t, pool, id)
		want := orgCounts{
			Organizations: 1, Identities: 1, Credentials: 1,
			Roles: len(crmRoleDefinitions), Resources: 2,
			Permissions: len(crmRoleDefinitions), RoleAssignments: 2,
			Projects: 1, Applications: 1,
		}
		if got != want {
			t.Errorf("%s row counts = %+v, want %+v", name, got, want)
		}
	}

	// No role, resource, permission, project or application is shared. A row
	// visible from both organizations is a cross-tenant authorization path.
	for _, shared := range []struct {
		table string
		sql   string
	}{
		{"roles", `SELECT count(*) FROM roles WHERE id IN (SELECT id FROM roles WHERE org_id=$1) AND org_id=$2`},
		{"resources", `SELECT count(*) FROM resources WHERE id IN (SELECT id FROM resources WHERE org_id=$1) AND org_id=$2`},
		{"permissions", `SELECT count(*) FROM permissions WHERE id IN (SELECT id FROM permissions WHERE org_id=$1) AND org_id=$2`},
		{"projects", `SELECT count(*) FROM projects WHERE id IN (SELECT id FROM projects WHERE org_id=$1) AND org_id=$2`},
		{"applications", `SELECT count(*) FROM applications WHERE id IN (SELECT id FROM applications WHERE org_id=$1) AND org_id=$2`},
	} {
		var n int
		if err := pool.QueryRow(ctx, shared.sql, alphaID, betaID).Scan(&n); err != nil {
			t.Fatalf("check shared %s: %v", shared.table, err)
		}
		if n != 0 {
			t.Errorf("%d %s rows belong to both organizations", n, shared.table)
		}
	}

	// The administrator of one is granted nothing in the other.
	var crossGrants int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM role_assignments WHERE identity_id=$1 AND org_id=$2`,
		uuid.MustParse(alpha.AdministratorID), betaID).Scan(&crossGrants); err != nil {
		t.Fatalf("check cross-organization grants: %v", err)
	}
	if crossGrants != 0 {
		t.Errorf("alpha's administrator holds %d role assignments in beta", crossGrants)
	}

	// Both organizations really do have a client under the same client id,
	// each scoped to its own organization.
	for name, id := range map[string]uuid.UUID{"alpha": alphaID, "beta": betaID} {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM applications WHERE org_id=$1 AND config->>'client_id'='crm-web'`,
			id).Scan(&n); err != nil {
			t.Fatalf("count %s clients: %v", name, err)
		}
		if n != 1 {
			t.Errorf("%s has %d crm-web clients, want 1", name, n)
		}
	}
}

// TestBootstrapOutputAndClientCarryNoSecrets covers §1.1's second acceptance
// criterion. The result is printed to stdout, which in CI ends up in a build
// log; the client config is served to a browser.
func TestBootstrapOutputAndClientCarryNoSecrets(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "nosecrets")

	result, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	printed := string(encoded)
	for _, secret := range []string{cfg.AdminPassword, "password", "secret", "private", "hashed"} {
		if containsFold(printed, secret) {
			t.Errorf("bootstrap output contains %q: %s", secret, printed)
		}
	}

	// The web client is public: a browser cannot keep a secret, so one must
	// not be issued for it.
	var raw []byte
	if err := pool.QueryRow(ctx,
		`SELECT config FROM applications WHERE org_id=$1 AND config->>'client_id'=$2`,
		uuid.MustParse(result.OrganizationID), cfg.ClientID).Scan(&raw); err != nil {
		t.Fatalf("read client config: %v", err)
	}
	var client struct {
		ClientSecret   string `json:"client_secret"`
		AuthMethodType string `json:"auth_method_type"`
		ApplicationTyp string `json:"application_type"`
	}
	if err := json.Unmarshal(raw, &client); err != nil {
		t.Fatalf("parse client config: %v", err)
	}
	if client.ClientSecret != "" {
		t.Error("the crm-web client was issued a client secret; it is a browser application " +
			"and cannot keep one")
	}
	if client.AuthMethodType != "none" {
		t.Errorf("crm-web auth_method_type = %q, want \"none\" (public client, PKCE)", client.AuthMethodType)
	}
	if client.ApplicationTyp != "spa" {
		t.Errorf("crm-web application_type = %q, want \"spa\"", client.ApplicationTyp)
	}
}

// containsFold is a case-insensitive substring test, kept local so the secret
// scan above reads as one thing.
func containsFold(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	h, n := []rune(haystack), []rune(needle)
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestOrganizationsCarryRolesForAnUnrelatedProduct pins a finding rather than
// a requirement (task.md §1.1, and docs/THIRD_PARTY_NOTICES.md's open item).
//
// migrations/025_dbcore_role_scopes.sql installs an AFTER INSERT trigger on
// `orgs` that seeds roles for DBCore — a different product, whose source is
// not in this repository — into every organization ever created, and
// backfills the ones that already existed. So a customer organization
// provisioned by this CRM's `make bootstrap` also carries "DBCore Admin",
// "DBCore Operator" and "DBCore Read-only", plus a `dbcore` resource, and the
// `owner` role is granted `dbcore:*` on it.
//
// Removing that is a product decision, not a test's call: it means deleting
// authority rows from organizations that already exist. Until it is decided,
// this test does two things. It records the current behaviour, so nobody has
// to rediscover it. And it bounds the blast radius by asserting the part that
// would actually matter to this product — that none of the foreign roles can
// reach the CRM, and that they leave the CRM's own role model alone.
//
// If the seeding is removed, this test should fail, and deleting it is then
// the correct fix.
func TestOrganizationsCarryRolesForAnUnrelatedProduct(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()

	result, err := bootstrap(ctx, pool, testConfig(t, "foreignroles"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	orgID := uuid.MustParse(result.OrganizationID)

	mine := map[string]bool{}
	for _, key := range bootstrapRoleKeys() {
		mine[key] = true
	}

	rows, err := pool.Query(ctx,
		`SELECT r.key, res.type, p.scopes
		   FROM roles r
		   JOIN permissions p ON p.role_id = r.id
		   JOIN resources res ON res.id = p.resource_id
		  WHERE r.org_id = $1
		  ORDER BY r.key, res.type`, orgID)
	if err != nil {
		t.Fatalf("read role model: %v", err)
	}
	defer rows.Close()

	var foreignRoles []string
	sawCRMResource := false
	for rows.Next() {
		var roleKey, resourceType string
		var scopes []string
		if err := rows.Scan(&roleKey, &resourceType, &scopes); err != nil {
			t.Fatalf("scan role model: %v", err)
		}
		if resourceType == "crm" {
			sawCRMResource = true
		}
		if !mine[roleKey] {
			foreignRoles = append(foreignRoles, roleKey)
		}
		// The property that matters: nothing outside this product's own role
		// model may hold authority over this product.
		for _, scope := range scopes {
			if resourceType != "crm" && (scope == "crm:*" || len(scope) > 4 && scope[:4] == "crm:") {
				t.Errorf("role %q holds %q on the %q resource — a role this CRM does not "+
					"define can reach the CRM", roleKey, scope, resourceType)
			}
			if !mine[roleKey] && (scope == "crm:*" || len(scope) > 4 && scope[:4] == "crm:") {
				t.Errorf("foreign role %q is granted the CRM scope %q", roleKey, scope)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate role model: %v", err)
	}
	if !sawCRMResource {
		t.Fatal("no CRM resource found for a freshly bootstrapped organization; " +
			"this test would pass vacuously")
	}

	if len(foreignRoles) > 0 {
		t.Logf("this organization carries %d role(s) this product does not define: %v — "+
			"seeded by migrations/025_dbcore_role_scopes.sql; see the test comment",
			len(foreignRoles), foreignRoles)
	}

	// Whatever else is seeded, the CRM's own roles must be exactly the ones
	// the shared matrix describes, with the scopes it describes.
	for _, definition := range crmRoleDefinitions {
		var scopes []string
		err := pool.QueryRow(ctx,
			`SELECT p.scopes FROM permissions p
			   JOIN roles r ON r.id = p.role_id
			   JOIN resources res ON res.id = p.resource_id
			  WHERE p.org_id=$1 AND r.key=$2 AND res.type=$3 AND res.name=$4`,
			orgID, definition.Key, definition.ResourceType, definition.ResourceName).Scan(&scopes)
		if err != nil {
			t.Errorf("role %s has no permission on %s/%s: %v",
				definition.Key, definition.ResourceType, definition.ResourceName, err)
			continue
		}
		if len(scopes) != len(definition.Scopes) {
			t.Errorf("role %s was provisioned %v, definition says %v",
				definition.Key, scopes, definition.Scopes)
			continue
		}
		for i := range scopes {
			if scopes[i] != definition.Scopes[i] {
				t.Errorf("role %s was provisioned %v, definition says %v",
					definition.Key, scopes, definition.Scopes)
				break
			}
		}
	}
}
