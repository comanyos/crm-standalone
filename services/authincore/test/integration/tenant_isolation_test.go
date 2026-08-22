//go:build integration

// Package integration contains integration tests that require a live Postgres
// instance. Run with:
//
//	DATABASE_URL=postgres://... go test -tags=integration ./test/integration/... -v
//
// Tests skip automatically when DATABASE_URL is not set.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	apihttp "github.com/authincore/authincore/internal/api/http"
	"github.com/authincore/authincore/internal/authz"
	"github.com/authincore/authincore/internal/blueprint"
	"github.com/authincore/authincore/internal/command"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/eventstore"
	espg "github.com/authincore/authincore/internal/eventstore/postgres"
	"github.com/authincore/authincore/internal/fga"
	"github.com/authincore/authincore/internal/flow"
	oidcstrategy "github.com/authincore/authincore/internal/flow/strategy/oidc"
	pwdstrategy "github.com/authincore/authincore/internal/flow/strategy/password"
	"github.com/authincore/authincore/internal/invitation"
	"github.com/authincore/authincore/internal/query"
	"github.com/authincore/authincore/internal/scim"
	"github.com/authincore/authincore/internal/serviceaccount"
	"github.com/authincore/authincore/internal/storage/database"
	pg "github.com/authincore/authincore/internal/storage/postgres"
	"github.com/authincore/authincore/internal/token"
	"github.com/authincore/authincore/internal/webhook"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test setup
// ---------------------------------------------------------------------------

func connectPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	pool, err := database.Connect(context.Background(), database.Config{DSN: dsn, MaxConns: 5})
	require.NoError(t, err, "connect to test Postgres")
	t.Cleanup(func() { pool.Close() })
	return pool
}

type testStack struct {
	pool      *pgxpool.Pool
	cmds      *command.Commands
	queries   *query.Queries
	scimUsers *pg.SCIMUserStore
}

type failingPusher struct{}

func (failingPusher) Push(context.Context, ...*eventstore.Event) error {
	return errors.New("injected event failure")
}

func newStack(t *testing.T) *testStack {
	t.Helper()
	pool := connectPool(t)

	identityRepo := pg.NewIdentityRepo(pool)
	sessionRepo := pg.NewSessionRepo(pool)

	// Compose Pusher + Querier into the Eventstore interface.
	es := struct {
		eventstore.Pusher
		eventstore.Querier
	}{
		Pusher:  espg.NewPusher(pool),
		Querier: espg.NewQuerier(pool),
	}

	cmds := command.New(pool, es, identityRepo, sessionRepo)
	queries := query.New(pool)

	return &testStack{
		pool:      pool,
		cmds:      cmds,
		queries:   queries,
		scimUsers: pg.NewSCIMUserStore(pool),
	}
}

func seedOrg(t *testing.T, ctx context.Context, cmds *command.Commands, name string) uuid.UUID {
	t.Helper()
	org, err := cmds.CreateOrg(ctx, command.CreateOrgCommand{
		InstanceID:    uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Name:          name,
		PrimaryDomain: name + ".example.test",
	})
	require.NoError(t, err, "seed org %q", name)
	return org.ID
}

// ---------------------------------------------------------------------------
// Identity isolation
// ---------------------------------------------------------------------------

func TestTenantIsolation_IdentityNotVisibleCrossOrg(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)

	orgA := seedOrg(t, ctx, s.cmds, "org-a-"+uuid.NewString())
	orgB := seedOrg(t, ctx, s.cmds, "org-b-"+uuid.NewString())

	traits, _ := json.Marshal(map[string]string{"email": "alice@orga.com"})
	identityA, err := s.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID:    orgA,
		SchemaID: "default",
		Traits:   traits,
	})
	require.NoError(t, err)

	// Org A can read its own identity.
	_, err = s.queries.GetIdentity(ctx, identityA.ID, orgA)
	require.NoError(t, err, "identity must be readable from its own org")

	// Org B must not see Org A's identity.
	_, err = s.queries.GetIdentity(ctx, identityA.ID, orgB)
	require.Error(t, err, "identity must NOT be visible from a different org")
	assert.True(t, domainerr.IsNotFound(err), "expected not-found, got: %v", err)
}

// ---------------------------------------------------------------------------
// SCIM user isolation
// ---------------------------------------------------------------------------

func TestTenantIsolation_SCIMUserNotVisibleCrossOrg(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)

	orgA := uuid.New()
	orgB := uuid.New()

	_, err := s.scimUsers.Create(ctx, orgA, &scim.User{
		UserName: "scim-" + uuid.NewString() + "@orga.com",
		Active:   true,
	})
	require.NoError(t, err)

	users, total, err := s.scimUsers.List(ctx, orgB, nil, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, users, "Org B must not see Org A SCIM users")
	assert.Equal(t, 0, total)
}

func TestTenantIsolation_SCIMBulkCreate_OtherOrgSeesEmpty(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)

	orgA := uuid.New()
	orgB := uuid.New()

	for i := 0; i < 3; i++ {
		_, err := s.scimUsers.Create(ctx, orgA, &scim.User{
			UserName: uuid.NewString() + "@orga.com",
			Active:   true,
		})
		require.NoError(t, err)
	}

	// Org B listing must be empty.
	users, total, err := s.scimUsers.List(ctx, orgB, nil, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, users)
	assert.Equal(t, 0, total)
}

// ---------------------------------------------------------------------------
// Session isolation
// ---------------------------------------------------------------------------

func TestTenantIsolation_SessionBelongsToOrgA(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)

	orgA := seedOrg(t, ctx, s.cmds, "org-a-"+uuid.NewString())

	traits, _ := json.Marshal(map[string]string{"email": "sess@orga.com"})
	identityA, err := s.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID:    orgA,
		SchemaID: "default",
		Traits:   traits,
	})
	require.NoError(t, err)

	sess, err := s.cmds.CreateSession(ctx, command.CreateSessionCommand{
		IdentityID: identityA.ID,
		OrgID:      orgA,
	})
	require.NoError(t, err)

	// Session reads must be scoped to the caller's organization.
	_, err = s.queries.GetSession(ctx, sess.ID, orgA)
	require.NoError(t, err, "session must be accessible by ID")

	orgB := seedOrg(t, ctx, s.cmds, "org-b-"+uuid.NewString())
	_, err = s.queries.GetSession(ctx, sess.ID, orgB)
	require.Error(t, err, "session must NOT be visible from a different org")
	assert.True(t, domainerr.IsNotFound(err), "expected not-found, got: %v", err)

	fakeBIdentityID := uuid.New()
	sessionsB, _, err := s.queries.ListSessions(ctx, fakeBIdentityID, orgB, true, 100, "")
	require.NoError(t, err)
	assert.Empty(t, sessionsB, "listing sessions for nonexistent identity must return empty")
}

// ---------------------------------------------------------------------------
// Cross-org lookup: must return not-found, not a data leak
// ---------------------------------------------------------------------------

func TestTenantIsolation_CrossOrgLookup_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)

	orgA := seedOrg(t, ctx, s.cmds, "org-a-"+uuid.NewString())
	orgB := seedOrg(t, ctx, s.cmds, "org-b-"+uuid.NewString())

	traits, _ := json.Marshal(map[string]string{"email": "cross@orga.com"})
	identityA, err := s.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID:    orgA,
		SchemaID: "default",
		Traits:   traits,
	})
	require.NoError(t, err)

	// Querying with Org B's ID must not return Org A's identity.
	_, err = s.queries.GetIdentity(ctx, identityA.ID, orgB)
	require.Error(t, err)
	assert.True(t, domainerr.IsNotFound(err),
		"cross-org query must return not-found (not data-leak), got: %v", err)
}

func TestAuthorization_FirstIdentityBecomesOwner(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgID := seedOrg(t, ctx, s.cmds, "owner-org-"+uuid.NewString())

	first, err := s.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID: orgID, SchemaID: "default",
		Traits: json.RawMessage(`{"email":"owner@example.test"}`),
	})
	require.NoError(t, err)
	second, err := s.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID: orgID, SchemaID: "default",
		Traits: json.RawMessage(`{"email":"member@example.test"}`),
	})
	require.NoError(t, err)

	enforcer := authz.NewEnforcer(pg.NewAuthzRoleStore(s.pool), pg.NewAuthzPermissionStore(s.pool))
	ownerDecision, err := enforcer.Check(ctx, authz.CheckRequest{
		OrgID: orgID, IdentityID: first.ID, Resource: "identity", Scope: "delete",
	})
	require.NoError(t, err)
	assert.True(t, ownerDecision.Allowed, "first identity must receive owner access")

	memberDecision, err := enforcer.Check(ctx, authz.CheckRequest{
		OrgID: orgID, IdentityID: second.ID, Resource: "identity", Scope: "delete",
	})
	require.NoError(t, err)
	assert.False(t, memberDecision.Allowed, "later identities must not inherit owner access")
}

func TestAuthenticationJourney_RegisterLoginTokenLogout(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgID := seedOrg(t, ctx, s.cmds, "auth-journey-"+uuid.NewString())
	credentials := pg.NewCredentialStore(s.pool)
	password := pwdstrategy.New(pwdstrategy.NewArgon2idHasher(), credentials, s.cmds, nil)
	manager := flow.NewManager(flow.NewPGRepository(s.pool), password)

	registration, err := manager.Create(ctx, domflow.TypeRegistration, orgID, "")
	require.NoError(t, err)
	registered, err := manager.Submit(ctx, registration.ID, "password", json.RawMessage(`{
		"traits":{"email":"journey@example.test"},
		"password":"Correct-Horse-Battery-Staple-42",
		"schema_id":"default"
	}`))
	require.NoError(t, err)
	require.True(t, registered.Done)
	require.NotNil(t, registered.IdentityID)

	login, err := manager.Create(ctx, domflow.TypeLogin, orgID, "")
	require.NoError(t, err)
	loggedIn, err := manager.Submit(ctx, login.ID, "password", json.RawMessage(`{
		"identifier":"journey@example.test",
		"password":"Correct-Horse-Battery-Staple-42"
	}`))
	require.NoError(t, err)
	require.True(t, loggedIn.Done)
	require.NotNil(t, loggedIn.IdentityID)
	require.Equal(t, *registered.IdentityID, *loggedIn.IdentityID)

	session, err := s.cmds.CreateSession(ctx, command.CreateSessionCommand{
		IdentityID: *loggedIn.IdentityID, OrgID: orgID,
	})
	require.NoError(t, err)
	signer, err := token.NewRS256Signer(nil, "integration-key")
	require.NoError(t, err)
	tokens := token.NewService(signer, "https://auth.integration.test", 15*time.Minute, 24*time.Hour)
	accessToken, err := tokens.MintAccessToken(ctx, session.IdentityID, orgID, session.ID,
		[]string{"owner"}, []string{"identity:read"}, "journey@example.test", 1)
	require.NoError(t, err)
	claims, err := tokens.Verify(accessToken)
	require.NoError(t, err)
	assert.Equal(t, session.IdentityID.String(), claims.Subject)
	assert.Equal(t, orgID.String(), claims.OrgID)
	assert.Equal(t, session.ID.String(), claims.SessionID)

	refreshToken, expiresAt, err := tokens.MintRefreshToken(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, refreshToken)
	assert.True(t, expiresAt.After(time.Now()))

	require.NoError(t, s.cmds.RevokeSession(ctx, session.ID, orgID))
	revoked, err := s.queries.GetSession(ctx, session.ID, orgID)
	require.NoError(t, err)
	assert.Equal(t, 2, int(revoked.State))
}

func TestTenantIsolation_ProjectAndApplication(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgA := seedOrg(t, ctx, s.cmds, "apps-a-"+uuid.NewString())
	orgB := seedOrg(t, ctx, s.cmds, "apps-b-"+uuid.NewString())
	project, err := s.cmds.CreateProject(ctx, command.CreateProjectCommand{OrgID: orgA, Name: "A"})
	require.NoError(t, err)
	app, err := s.cmds.CreateApp(ctx, command.CreateAppCommand{
		ProjectID: project.ID, OrgID: orgA, Name: "App A", Type: 1,
	})
	require.NoError(t, err)
	require.Equal(t, orgA, app.OrgID)

	_, err = s.cmds.CreateApp(ctx, command.CreateAppCommand{
		ProjectID: project.ID, OrgID: orgB, Name: "Cross-tenant app", Type: 1,
	})
	require.Error(t, err, "cross-tenant project must not accept an application")
	assert.True(t, domainerr.IsNotFound(err), "expected not-found, got: %v", err)

	apps, err := s.queries.ListApps(ctx, project.ID, orgB)
	require.NoError(t, err)
	assert.Empty(t, apps, "cross-tenant application listing must be empty")
}

func TestTenantIsolation_AdminResources(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgA := seedOrg(t, ctx, s.cmds, "resources-a-"+uuid.NewString())
	orgB := seedOrg(t, ctx, s.cmds, "resources-b-"+uuid.NewString())
	identityA, err := s.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID: orgA, SchemaID: "default", Traits: json.RawMessage(`{"email":"resource-owner@example.test"}`),
	})
	require.NoError(t, err)

	t.Run("roles", func(t *testing.T) {
		store := pg.NewAdminRoleStore(s.pool)
		roleA := &apihttp.AdminRole{ID: uuid.New(), OrgID: orgA, Key: "auditor-" + uuid.NewString(), DisplayName: "Auditor"}
		require.NoError(t, store.CreateRole(ctx, roleA))
		require.Error(t, store.AssignRole(ctx, orgB, identityA.ID, roleA.ID), "cross-org role assignment must fail")
		rolesB, err := store.ListRoles(ctx, orgB)
		require.NoError(t, err)
		for _, role := range rolesB {
			assert.NotEqual(t, roleA.ID, role.ID)
		}
	})

	t.Run("oidc providers", func(t *testing.T) {
		store := pg.NewOIDCProviderStore(s.pool)
		provider := &oidcstrategy.Provider{ID: "provider-" + uuid.NewString(), Name: "Org A", ClientID: "client-a"}
		require.NoError(t, store.Create(ctx, orgA, provider))
		providersB, err := store.List(ctx, orgB)
		require.NoError(t, err)
		for _, item := range providersB {
			assert.NotEqual(t, provider.ID, item.ID)
		}
		_, err = store.ByID(ctx, orgB, provider.ID)
		require.Error(t, err)
	})

	t.Run("webhooks", func(t *testing.T) {
		store := pg.NewWebhookStore(s.pool)
		hook := &webhook.Webhook{OrgID: orgA, URL: "https://example.test/hook", Events: []string{"identity.created"}, Enabled: true, ConnectTimeoutMS: 1000, ReadTimeoutMS: 1000}
		require.NoError(t, store.Create(ctx, hook))
		hooksB, err := store.List(ctx, orgB)
		require.NoError(t, err)
		for _, item := range hooksB {
			assert.NotEqual(t, hook.ID, item.ID)
		}
		require.NoError(t, store.Delete(ctx, orgB, hook.ID))
		hooksA, err := store.List(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, hooksA, 1)
	})

	t.Run("service accounts", func(t *testing.T) {
		store := pg.NewServiceAccountStore(s.pool)
		signer, err := token.NewRS256Signer(nil, "integration-service-account")
		require.NoError(t, err)
		svc := serviceaccount.New(store, signer, "https://auth.integration.test", time.Hour)
		created, err := svc.Create(ctx, orgA, "worker", []string{"jobs:write"})
		require.NoError(t, err)
		_, err = svc.IssueToken(ctx, orgB, created.ServiceAccount.ClientID, created.ClientSecret)
		assert.True(t, domainerr.IsUnauthenticated(err), "cross-org service account token must be rejected")
		accountsB, err := store.List(ctx, orgB)
		require.NoError(t, err)
		assert.Empty(t, accountsB)
	})

	t.Run("invitations", func(t *testing.T) {
		store := pg.NewInvitationStore(s.pool)
		inv := &invitation.Invitation{ID: uuid.New(), OrgID: orgA, Email: "invite@example.test", Role: "member", Token: uuid.NewString(), InviterID: identityA.ID, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now()}
		require.NoError(t, store.Insert(ctx, inv))
		invitationsB, err := store.ListByOrg(ctx, orgB)
		require.NoError(t, err)
		assert.Empty(t, invitationsB)
		require.NoError(t, store.Delete(ctx, orgB, inv.ID))
		invitationsA, err := store.ListByOrg(ctx, orgA)
		require.NoError(t, err)
		require.Len(t, invitationsA, 1)
	})

	t.Run("fga tuples", func(t *testing.T) {
		store := pg.NewFGAStore(s.pool)
		tuple := fga.Tuple{ObjectType: "resource", ObjectID: uuid.NewString(), Relation: "viewer", SubjectType: "user", SubjectID: identityA.ID.String()}
		require.NoError(t, store.Write(ctx, orgA, tuple))
		tuplesB, err := store.DirectTuples(ctx, orgB, tuple.ObjectType, tuple.ObjectID, "", "", "")
		require.NoError(t, err)
		assert.Empty(t, tuplesB)
		require.NoError(t, store.Delete(ctx, orgB, tuple))
		tuplesA, err := store.DirectTuples(ctx, orgA, tuple.ObjectType, tuple.ObjectID, "", "", "")
		require.NoError(t, err)
		require.Len(t, tuplesA, 1)
	})
}

func TestCommandAtomicity_EventFailureRollsBackRelationalWrite(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgID := seedOrg(t, ctx, s.cmds, "atomicity-"+uuid.NewString())
	es := struct {
		eventstore.Pusher
		eventstore.Querier
	}{Pusher: failingPusher{}, Querier: espg.NewQuerier(s.pool)}
	cmds := command.New(s.pool, es, pg.NewIdentityRepo(s.pool), pg.NewSessionRepo(s.pool))
	created, err := cmds.CreateIdentity(ctx, command.CreateIdentityCommand{OrgID: orgID, SchemaID: "default", Traits: json.RawMessage(`{"email":"rollback@example.test"}`)})
	require.Error(t, err)
	require.Nil(t, created)
	var count int
	require.NoError(t, s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM identities WHERE org_id=$1 AND traits @> '{"email":"rollback@example.test"}'::jsonb`, orgID).Scan(&count))
	assert.Zero(t, count, "relational write must roll back when event append fails")
}

func TestBlueprintApply_IsIdempotentAndBackedByRealResources(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgID := seedOrg(t, ctx, s.cmds, "blueprint-"+uuid.NewString())
	name := "integration-blueprint-" + uuid.NewString()
	webhookURL := "https://hooks.example.test/" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM blueprints WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM webhooks WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM applications WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM identity_schemas WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM orgs WHERE id=$1`, orgID)
	})

	doc := `version: "1"
metadata:
  name: ` + name + `
entries:
  - model: authincore/identity-schema
    id: user-v1
    state: present
    attrs:
      schema: '{"type":"object","properties":{"email":{"type":"string"}}}'
  - model: authincore/project
    id: main
    state: present
    attrs:
      name: Main
  - model: authincore/application
    id: web
    state: present
    attrs:
      name: Web
      project_id: main
      type: oidc
      oidc_config:
        redirect_uris: ["https://app.example.test/callback"]
  - model: authincore/webhook
    id: audit
    state: present
    attrs:
      url: ` + webhookURL + `
      events: ["identity.created"]
      enabled: true
`

	applier := blueprint.NewApplier(pg.NewBlueprintStore(s.pool), slog.Default(), pg.NewBlueprintReconcilers(s.pool)...)
	_, first, err := applier.Apply(ctx, orgID, doc, false)
	require.NoError(t, err)
	require.Len(t, first.ToCreate, 4)

	_, second, err := applier.Apply(ctx, orgID, doc, false)
	require.NoError(t, err)
	require.Empty(t, second.ToCreate)
	require.Empty(t, second.ToUpdate)
	require.Len(t, second.Unchanged, 4)

	var resources int
	require.NoError(t, s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM identity_schemas WHERE org_id=$1 AND id='user-v1') +
		(SELECT count(*) FROM projects WHERE org_id=$1 AND name='Main') +
		(SELECT count(*) FROM applications WHERE org_id=$1 AND name='Web') +
		(SELECT count(*) FROM webhooks WHERE org_id=$1 AND url=$2)`, orgID, webhookURL).Scan(&resources))
	require.Equal(t, 4, resources)
}

func TestBlueprintApply_RollsBackAllResourcesOnFailure(t *testing.T) {
	ctx := context.Background()
	s := newStack(t)
	orgID := seedOrg(t, ctx, s.cmds, "blueprint-rollback-"+uuid.NewString())
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM blueprints WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM projects WHERE org_id=$1`, orgID)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM orgs WHERE id=$1`, orgID)
	})
	doc := `version: "1"
metadata:
  name: rollback-test
entries:
  - model: authincore/project
    id: must-not-persist
    attrs:
      name: Must Not Persist
  - model: authincore/webhook
    id: invalid
    attrs:
      url: http://insecure.example.test/hook
      events: ["identity.created"]
`
	applier := blueprint.NewApplier(pg.NewBlueprintStore(s.pool), slog.Default(), pg.NewBlueprintReconcilers(s.pool)...)
	_, _, err := applier.Apply(ctx, orgID, doc, false)
	require.Error(t, err)
	var count int
	require.NoError(t, s.pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE org_id=$1 AND name='Must Not Persist'`, orgID).Scan(&count))
	require.Zero(t, count)
}
