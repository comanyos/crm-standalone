package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/gen/crm/v1/crmv1connect"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/pgtest"
)

// M3's revenue workflows end to end: a real HTTP server, opaque test
// sessions, the real scope interceptor, and — unlike
// policy_enforcement_test.go, which runs the same server over MemStore — a
// real PostgreSQL store connected as crm_app, the role row-level security
// binds (migrations/0003_rls_enforce.sql).
//
// That combination is the point. Scope enforcement over MemStore proves the
// interceptor is wired; tenant isolation in the store proves the policies
// filter. Only running both together proves that the authenticated session
// actually reaches is the thing RLS actually constrains — which is what
// docs/ROADMAP.md M3 asks for and what the earlier tiers each half-answer.

func superuserDSN() string { return pgtest.DSN() }

// appRoleDSN rewrites the DSN to connect as crm_app, which does not bypass
// row-level security. Connecting as the superuser instead would make every
// isolation assertion below pass on the Go tenant predicates alone and prove
// nothing about the policies.
func appRoleDSN(t *testing.T) string {
	t.Helper()
	dsn := superuserDSN()
	at := strings.Index(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 {
		pgtest.Unavailable(t, "cannot derive a crm_app DSN from %q", dsn)
	}
	return dsn[:scheme+3] + "crm_app:crm_app" + dsn[at:]
}

// migrationLockKey is the key internal/domain's Postgres tier takes
// exclusively while it applies migrations (pgstore_test.go).
//
// That package owns the schema; this one only uses it. But its DDL takes
// ACCESS EXCLUSIVE locks on the very tables the tests below query, and
// `go test ./...` runs the two packages at the same time, so an unguarded
// overlap deadlocks at random. Holding the same lock in *shared* mode for the
// length of each test here is what keeps the two apart: several readers may
// hold it together, and the writer waits for all of them.
const migrationLockKey = 0x63726D5F6D696701

// holdSchemaLock takes the shared half of the migration lock for the duration
// of one test. The connection has to outlive the call — an advisory lock is
// session-scoped — so it is released from the test's cleanup, not here.
func holdSchemaLock(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, superuserDSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		pgtest.Unavailable(t, "skipping live Postgres test (cannot reach %s — run `make test-db-up`): %v", superuserDSN(), err)
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

// requireSchema fails fast, with the command that fixes it, when the database
// is reachable but has never had the CRM migrations applied. The disposable
// instance `make test-db-up` starts applies them from its initdb mount, and
// internal/domain's tier re-applies them; this package deliberately runs no
// DDL of its own, so that the schema only ever has one writer.
func requireSchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, superuserDSN())
	if err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot connect): %v", err)
	}
	defer pool.Close()

	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'opportunity_stage_history' AND relkind = 'r')
		    AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app')`).Scan(&ok); err != nil {
		pgtest.Unavailable(t, "skipping live Postgres test (cannot read the catalog): %v", err)
	}
	if !ok {
		pgtest.Unavailable(t, "the CRM schema and the crm_app role are not present — run `make test-db-up`")
	}
}

// livePgServer is liveServer's PostgreSQL twin: the same real server and
// interceptor over a store that is subject to RLS.
func livePgServer(t *testing.T) (crmv1connect.CRMServiceClient, func(org string, scopes []string) string) {
	t.Helper()
	holdSchemaLock(t)
	requireSchema(t)

	store, err := domain.NewPgStore(context.Background(), appRoleDSN(t))
	if err != nil {
		pgtest.Unavailable(t, "cannot open a crm_app store (migration 0003 needs a superuser connection): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	authn, mint := testSessions(t)

	srv, err := New(config.Config{}, store, authn)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(srv.Close)

	http := httptest.NewServer(srv.Handler())
	t.Cleanup(http.Close)

	return crmv1connect.NewCRMServiceClient(http.Client(), http.URL), mint
}

// revenueOrg returns an organization identifier no other test shares. The
// authenticated membership's organization is the tenant, and the test
// database is reused across runs, so a fixed name would inherit previous
// runs' rows.
func revenueOrg(label string) string {
	return fmt.Sprintf("org-rev-%s-%d", label, time.Now().UnixNano())
}

// readerScopes / writerScopes are the two shapes the revenue screens need,
// spelled out rather than using the crm:* wildcard, so a scope that quietly
// stops being required is visible here.
var (
	readerScopes = []string{
		"crm:account:read", "crm:contact:read", "crm:lead:read",
		"crm:opportunity:read", "crm:pipeline:read",
	}
	writerScopes = append([]string{
		"crm:account:write", "crm:contact:write", "crm:lead:write",
		"crm:opportunity:write", "crm:pipeline:write",
	}, readerScopes...)
)

// seedRevenue runs one organization's revenue workflow over HTTP and returns
// the ids it produced.
type revenueFixture struct {
	leadID        string
	convertedLead string
	contactID     string
	accountID     string
	opportunityID string
}

func seedRevenue(ctx context.Context, t *testing.T, c crmv1connect.CRMServiceClient, token, label string) revenueFixture {
	t.Helper()
	var f revenueFixture

	lead, err := c.CreateLead(ctx, withToken(&crmv1.CreateLeadRequest{
		Lead: &crmv1.Lead{
			Company: label + " Prospect", Source: "referral",
			ContactEmail: "buyer@" + label + ".example", OwnerId: "owner-" + label,
		},
	}, token))
	if err != nil {
		t.Fatalf("[%s] create lead: %v", label, err)
	}
	current := lead.Msg.GetLead()
	f.leadID = current.GetId()
	if current.GetStatus() != "new" {
		t.Fatalf("[%s] new lead status = %q, want new", label, current.GetStatus())
	}

	for _, status := range []string{"working", "qualified"} {
		current.Status = status
		next, err := c.UpdateLead(ctx, withToken(&crmv1.UpdateLeadRequest{Lead: current}, token))
		if err != nil {
			t.Fatalf("[%s] advance lead to %s: %v", label, status, err)
		}
		current = next.Msg.GetLead()
	}

	converted, err := c.ConvertLead(ctx, withToken(&crmv1.ConvertLeadRequest{
		LeadId: f.leadID, CreateOpportunity: true, OpportunityAmount: 4200,
	}, token))
	if err != nil {
		t.Fatalf("[%s] convert lead: %v", label, err)
	}
	f.convertedLead = converted.Msg.GetLead().GetId()
	f.contactID = converted.Msg.GetContact().GetId()
	f.accountID = converted.Msg.GetAccount().GetId()
	f.opportunityID = converted.Msg.GetOpportunity().GetId()

	// One real stage advance, so the stage history has an entry to hide.
	deal := converted.Msg.GetOpportunity()
	deal.Stage = ""
	deal.StageId = "qualification"
	if _, err := c.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: deal}, token)); err != nil {
		t.Fatalf("[%s] advance opportunity: %v", label, err)
	}
	return f
}

// TestRevenueWorkflowOverPostgres walks the whole workflow through the RPC
// surface against the RLS-bound store, and checks the answers the UI depends
// on: the conversion links resolve, the stage history is readable and
// accurate, and the terminal-stage close rules are enforced at this layer too
// rather than only in the domain package.
func TestRevenueWorkflowOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()

	org := revenueOrg("workflow")
	token := mint(org, writerScopes)
	f := seedRevenue(ctx, t, client, token, "workflow")

	// The conversion links resolve to real records over the wire.
	lead, err := client.GetLead(ctx, withToken(&crmv1.GetLeadRequest{Id: f.leadID}, token))
	if err != nil {
		t.Fatalf("get converted lead: %v", err)
	}
	if lead.Msg.GetLead().GetStatus() != "converted" {
		t.Errorf("lead status = %q, want converted", lead.Msg.GetLead().GetStatus())
	}
	if lead.Msg.GetLead().GetConvertedContactId() != f.contactID ||
		lead.Msg.GetLead().GetConvertedAccountId() != f.accountID ||
		lead.Msg.GetLead().GetConvertedOpportunityId() != f.opportunityID {
		t.Errorf("lead conversion links = %+v, want %s/%s/%s",
			lead.Msg.GetLead(), f.contactID, f.accountID, f.opportunityID)
	}
	contact, err := client.GetContact(ctx, withToken(&crmv1.GetContactRequest{Id: f.contactID}, token))
	if err != nil {
		t.Fatalf("converted contact does not resolve: %v", err)
	}
	if contact.Msg.GetContact().GetAccountId() != f.accountID {
		t.Errorf("converted contact is attached to %q, want the converted account %q",
			contact.Msg.GetContact().GetAccountId(), f.accountID)
	}
	if _, err := client.GetAccount(ctx, withToken(&crmv1.GetAccountRequest{Id: f.accountID}, token)); err != nil {
		t.Fatalf("converted account does not resolve: %v", err)
	}

	// Retrying the conversion is a no-op that returns the same ids — the
	// behaviour a client retrying a request it never saw the answer to
	// depends on.
	retry, err := client.ConvertLead(ctx, withToken(&crmv1.ConvertLeadRequest{
		LeadId: f.leadID, CreateOpportunity: true, OpportunityAmount: 99999,
	}, token))
	if err != nil {
		t.Fatalf("retry conversion: %v", err)
	}
	if retry.Msg.GetOpportunity().GetId() != f.opportunityID || retry.Msg.GetContact().GetId() != f.contactID {
		t.Errorf("retry produced different records: %+v", retry.Msg)
	}

	// Walk the deal to a terminal stage, checking the close rules at the RPC
	// boundary.
	deal, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: f.opportunityID}, token))
	if err != nil {
		t.Fatalf("get opportunity: %v", err)
	}
	current := deal.Msg.GetOpportunity()
	for _, stage := range []string{"proposal", "negotiation"} {
		current.Stage = ""
		current.StageId = stage
		next, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: current}, token))
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		current = next.Msg.GetOpportunity()
	}

	closing := current
	closing.Stage = ""
	closing.StageId = "closed_won"
	closing.CloseReason = ""
	if _, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: closing}, token)); err == nil {
		t.Error("closing without a reason succeeded; the terminal stage requires one")
	} else if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("close without a reason: code = %v, want InvalidArgument", got)
	}

	closing.CloseReason = "budget approved"
	won, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: closing}, token))
	if err != nil {
		t.Fatalf("close won: %v", err)
	}
	if won.Msg.GetOpportunity().GetCloseDate() == nil || won.Msg.GetOpportunity().GetCloseDate().AsTime().IsZero() {
		t.Error("close_date was not stamped by the closing transition")
	}

	// Terminal is terminal over the wire too.
	reopen := won.Msg.GetOpportunity()
	reopen.Stage = ""
	reopen.StageId = "negotiation"
	if _, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: reopen}, token)); err == nil {
		t.Error("a closed deal reopened")
	}

	// The stage history is the audit record the detail view renders.
	history, err := client.ListOpportunityStageHistory(ctx, withToken(&crmv1.ListOpportunityStageHistoryRequest{
		OpportunityId: f.opportunityID,
	}, token))
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	changes := history.Msg.GetChanges()
	want := [][2]string{
		{"prospecting", "qualification"},
		{"qualification", "proposal"},
		{"proposal", "negotiation"},
		{"negotiation", "closed_won"},
	}
	if len(changes) != len(want) {
		t.Fatalf("stage history has %d entries, want %d: %+v", len(changes), len(want), changes)
	}
	for i, w := range want {
		if changes[i].GetFromStage() != w[0] || changes[i].GetToStage() != w[1] {
			t.Errorf("history[%d] = %s -> %s, want %s -> %s",
				i, changes[i].GetFromStage(), changes[i].GetToStage(), w[0], w[1])
		}
	}

	if _, err := client.DeleteOpportunity(ctx, withToken(&crmv1.DeleteOpportunityRequest{Id: f.opportunityID}, token)); err != nil {
		t.Fatalf("delete opportunity: %v", err)
	}
	_, err = client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: f.opportunityID}, token))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("get after delete: code = %v, want NotFound", got)
	}
}

// TestRevenueWorkflowOptimisticConcurrencyOverPostgres proves the conflict
// the web client already sends updated_at for. Both the leads and the
// opportunities form round-trip the version they opened
// (apps/web/src/features/{leads,opportunities}/*FormPage.tsx); until the
// server compared it, that value was accepted and discarded, and a second
// editor's save silently overwrote the first.
func TestRevenueWorkflowOptimisticConcurrencyOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()

	org := revenueOrg("occ")
	token := mint(org, writerScopes)

	created, err := client.CreateLead(ctx, withToken(&crmv1.CreateLeadRequest{
		Lead: &crmv1.Lead{Company: "Contested", ContactEmail: "c@contested.example"},
	}, token))
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	opened := created.Msg.GetLead()

	first := &crmv1.Lead{
		Id: opened.GetId(), Company: opened.GetCompany(), ContactEmail: opened.GetContactEmail(),
		Status: opened.GetStatus(), Score: 10, UpdatedAt: opened.GetUpdatedAt(),
	}
	if _, err := client.UpdateLead(ctx, withToken(&crmv1.UpdateLeadRequest{Lead: first}, token)); err != nil {
		t.Fatalf("first save: %v", err)
	}

	stale := &crmv1.Lead{
		Id: opened.GetId(), Company: opened.GetCompany(), ContactEmail: opened.GetContactEmail(),
		Status: opened.GetStatus(), Score: 20, UpdatedAt: opened.GetUpdatedAt(),
	}
	_, err = client.UpdateLead(ctx, withToken(&crmv1.UpdateLeadRequest{Lead: stale}, token))
	if got := connect.CodeOf(err); got != connect.CodeAborted {
		t.Fatalf("stale lead save: code = %v (%v), want Aborted", got, err)
	}
	after, err := client.GetLead(ctx, withToken(&crmv1.GetLeadRequest{Id: opened.GetId()}, token))
	if err != nil {
		t.Fatalf("re-read lead: %v", err)
	}
	if after.Msg.GetLead().GetScore() != 10 {
		t.Errorf("lead score = %d, want 10 — the refused save wrote anyway", after.Msg.GetLead().GetScore())
	}

	// Same for opportunities, on the write that matters most: a stage move.
	account, err := client.CreateAccount(ctx, withToken(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Contested Holdings", OwnerId: "owner-1"},
	}, token))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	deal, err := client.CreateOpportunity(ctx, withToken(&crmv1.CreateOpportunityRequest{
		Opportunity: &crmv1.Opportunity{Name: "Contested deal", AccountId: account.Msg.GetAccount().GetId()},
	}, token))
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	openedDeal := deal.Msg.GetOpportunity()

	renamed := &crmv1.Opportunity{
		Id: openedDeal.GetId(), Name: "Renamed", AccountId: openedDeal.GetAccountId(),
		StageId: openedDeal.GetStageId(), UpdatedAt: openedDeal.GetUpdatedAt(),
	}
	if _, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: renamed}, token)); err != nil {
		t.Fatalf("first opportunity save: %v", err)
	}

	staleAdvance := &crmv1.Opportunity{
		Id: openedDeal.GetId(), Name: openedDeal.GetName(), AccountId: openedDeal.GetAccountId(),
		StageId: "qualification", UpdatedAt: openedDeal.GetUpdatedAt(),
	}
	_, err = client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: staleAdvance}, token))
	if got := connect.CodeOf(err); got != connect.CodeAborted {
		t.Fatalf("stale opportunity save: code = %v (%v), want Aborted", got, err)
	}
	history, err := client.ListOpportunityStageHistory(ctx, withToken(&crmv1.ListOpportunityStageHistoryRequest{
		OpportunityId: openedDeal.GetId(),
	}, token))
	if err != nil {
		t.Fatalf("list stage history: %v", err)
	}
	if len(history.Msg.GetChanges()) != 0 {
		t.Errorf("a refused save recorded %d stage changes, want 0", len(history.Msg.GetChanges()))
	}
}

// TestRevenueWorkflowScopeEnforcementOverPostgres checks authorization for
// every revenue procedure against the live store: a principal holding every
// revenue scope except the one a procedure demands is refused, and the same
// call with the scope succeeds. Sweeping both directions is what catches a
// procedure mapped to a scope nobody holds — denial alone would look correct.
func TestRevenueWorkflowScopeEnforcementOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()

	org := revenueOrg("scopes")
	admin := mint(org, writerScopes)
	f := seedRevenue(ctx, t, client, admin, "scopes")

	// without returns a token holding every revenue scope except one.
	without := func(scope string) string {
		kept := make([]string, 0, len(writerScopes))
		for _, s := range writerScopes {
			if s != scope {
				kept = append(kept, s)
			}
		}
		return mint(org, kept)
	}

	cases := []struct {
		name  string
		scope string
		call  func(token string) error
	}{
		{"GetLead", "crm:lead:read", func(tok string) error {
			_, err := client.GetLead(ctx, withToken(&crmv1.GetLeadRequest{Id: f.leadID}, tok))
			return err
		}},
		{"ListLeads", "crm:lead:read", func(tok string) error {
			_, err := client.ListLeads(ctx, withToken(&crmv1.ListLeadsRequest{}, tok))
			return err
		}},
		{"CreateLead", "crm:lead:write", func(tok string) error {
			_, err := client.CreateLead(ctx, withToken(&crmv1.CreateLeadRequest{
				Lead: &crmv1.Lead{Company: "Scope probe"},
			}, tok))
			return err
		}},
		{"ConvertLead", "crm:lead:write", func(tok string) error {
			// Already converted, so the authorized call is the idempotent
			// no-op rather than a second conversion.
			_, err := client.ConvertLead(ctx, withToken(&crmv1.ConvertLeadRequest{LeadId: f.leadID}, tok))
			return err
		}},
		{"GetOpportunity", "crm:opportunity:read", func(tok string) error {
			_, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: f.opportunityID}, tok))
			return err
		}},
		{"ListOpportunities", "crm:opportunity:read", func(tok string) error {
			_, err := client.ListOpportunities(ctx, withToken(&crmv1.ListOpportunitiesRequest{}, tok))
			return err
		}},
		{"ListOpportunityStageHistory", "crm:opportunity:read", func(tok string) error {
			_, err := client.ListOpportunityStageHistory(ctx, withToken(&crmv1.ListOpportunityStageHistoryRequest{
				OpportunityId: f.opportunityID,
			}, tok))
			return err
		}},
		{"CreateOpportunity", "crm:opportunity:write", func(tok string) error {
			_, err := client.CreateOpportunity(ctx, withToken(&crmv1.CreateOpportunityRequest{
				Opportunity: &crmv1.Opportunity{Name: "Scope probe", AccountId: f.accountID},
			}, tok))
			return err
		}},
		{"ListPipelines", "crm:pipeline:read", func(tok string) error {
			_, err := client.ListPipelines(ctx, withToken(&crmv1.ListPipelinesRequest{}, tok))
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(without(tc.scope)); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Errorf("without %s: code = %v (%v), want PermissionDenied", tc.scope, connect.CodeOf(err), err)
			}
			if err := tc.call(admin); err != nil {
				t.Errorf("with %s: %v — the procedure demands a scope its own role cannot use", tc.scope, err)
			}
		})
	}

	// Deleting is checked last, since it removes what the sweep above reads.
	if err := func() error {
		_, err := client.DeleteOpportunity(ctx, withToken(&crmv1.DeleteOpportunityRequest{Id: f.opportunityID}, without("crm:opportunity:write")))
		return err
	}(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("DeleteOpportunity without crm:opportunity:write: code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	if _, err := client.DeleteLead(ctx, withToken(&crmv1.DeleteLeadRequest{Id: f.leadID}, without("crm:lead:write"))); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("DeleteLead without crm:lead:write: code = %v, want PermissionDenied", connect.CodeOf(err))
	}

	// A read-only principal is refused every revenue mutation, and the
	// records survive.
	viewer := mint(org, readerScopes)
	deal, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: f.opportunityID}, viewer))
	if err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	stageBefore := deal.Msg.GetOpportunity().GetStageId()
	advance := deal.Msg.GetOpportunity()
	advance.Stage = ""
	advance.StageId = "proposal"
	if _, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: advance}, viewer)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer advanced a deal: code = %v", connect.CodeOf(err))
	}
	if _, err := client.ConvertLead(ctx, withToken(&crmv1.ConvertLeadRequest{LeadId: f.leadID}, viewer)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer converted a lead: code = %v", connect.CodeOf(err))
	}
	after, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: f.opportunityID}, admin))
	if err != nil {
		t.Fatalf("re-read after denied writes: %v", err)
	}
	if after.Msg.GetOpportunity().GetStageId() != stageBefore {
		t.Errorf("a denied write moved the deal from %q to %q",
			stageBefore, after.Msg.GetOpportunity().GetStageId())
	}
}

// TestRevenueWorkflowTenantIsolationOverPostgres is the two-organization
// proof for the revenue surface, over HTTP, against RLS.
//
// Both principals are fully authorized inside their own organization, so a
// refusal below is the tenant boundary and nothing else. Every answer must be
// NotFound: PermissionDenied would confirm that the id names a real record
// somewhere, which is the disclosure the boundary exists to prevent.
func TestRevenueWorkflowTenantIsolationOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()

	orgA, orgB := revenueOrg("iso-a"), revenueOrg("iso-b")
	tokenA, tokenB := mint(orgA, writerScopes), mint(orgB, writerScopes)

	a := seedRevenue(ctx, t, client, tokenA, "isoa")
	b := seedRevenue(ctx, t, client, tokenB, "isob")

	// --- Reads ---------------------------------------------------------
	reads := map[string]func(token string) error{
		"GetLead": func(tok string) error {
			_, err := client.GetLead(ctx, withToken(&crmv1.GetLeadRequest{Id: a.leadID}, tok))
			return err
		},
		"GetOpportunity": func(tok string) error {
			_, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: a.opportunityID}, tok))
			return err
		},
		"GetContact": func(tok string) error {
			_, err := client.GetContact(ctx, withToken(&crmv1.GetContactRequest{Id: a.contactID}, tok))
			return err
		},
		"GetAccount": func(tok string) error {
			_, err := client.GetAccount(ctx, withToken(&crmv1.GetAccountRequest{Id: a.accountID}, tok))
			return err
		},
	}
	for name, call := range reads {
		if got := connect.CodeOf(call(tokenB)); got != connect.CodeNotFound {
			t.Errorf("[B] %s on A's record: code = %v, want NotFound", name, got)
		}
		if err := call(tokenA); err != nil {
			t.Errorf("[A] %s on its own record: %v", name, err)
		}
	}

	// A stage history that exists but is not B's reads as empty, not as a
	// refusal — the same tenant-safe shape.
	history, err := client.ListOpportunityStageHistory(ctx, withToken(&crmv1.ListOpportunityStageHistoryRequest{
		OpportunityId: a.opportunityID,
	}, tokenB))
	if err != nil {
		t.Fatalf("[B] stage history of A's deal: %v", err)
	}
	if len(history.Msg.GetChanges()) != 0 {
		t.Errorf("[B] read %d of A's stage changes, want 0", len(history.Msg.GetChanges()))
	}

	// --- Collections ---------------------------------------------------
	leads, err := client.ListLeads(ctx, withToken(&crmv1.ListLeadsRequest{}, tokenB))
	if err != nil {
		t.Fatalf("[B] list leads: %v", err)
	}
	for _, l := range leads.Msg.GetLeads() {
		if l.GetId() == a.leadID {
			t.Errorf("[B] ListLeads returned A's lead")
		}
	}
	if len(leads.Msg.GetLeads()) != 1 || leads.Msg.GetLeads()[0].GetId() != b.leadID {
		t.Errorf("[B] ListLeads = %d rows, want only its own", len(leads.Msg.GetLeads()))
	}
	opps, err := client.ListOpportunities(ctx, withToken(&crmv1.ListOpportunitiesRequest{}, tokenB))
	if err != nil {
		t.Fatalf("[B] list opportunities: %v", err)
	}
	if len(opps.Msg.GetOpportunities()) != 1 || opps.Msg.GetOpportunities()[0].GetId() != b.opportunityID {
		t.Errorf("[B] ListOpportunities = %d rows, want only its own", len(opps.Msg.GetOpportunities()))
	}

	// --- Mutations ------------------------------------------------------
	mutations := map[string]func() error{
		"UpdateLead": func() error {
			_, err := client.UpdateLead(ctx, withToken(&crmv1.UpdateLeadRequest{
				Lead: &crmv1.Lead{Id: a.leadID, Company: "Owned by B", Status: "converted"},
			}, tokenB))
			return err
		},
		"DeleteLead": func() error {
			_, err := client.DeleteLead(ctx, withToken(&crmv1.DeleteLeadRequest{Id: a.leadID}, tokenB))
			return err
		},
		"ConvertLead": func() error {
			_, err := client.ConvertLead(ctx, withToken(&crmv1.ConvertLeadRequest{LeadId: a.leadID}, tokenB))
			return err
		},
		"UpdateOpportunity": func() error {
			_, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{
				Opportunity: &crmv1.Opportunity{Id: a.opportunityID, Name: "Owned by B", AccountId: b.accountID},
			}, tokenB))
			return err
		},
		"DeleteOpportunity": func() error {
			_, err := client.DeleteOpportunity(ctx, withToken(&crmv1.DeleteOpportunityRequest{Id: a.opportunityID}, tokenB))
			return err
		},
	}
	for name, call := range mutations {
		if got := connect.CodeOf(call()); got != connect.CodeNotFound {
			t.Errorf("[B] %s on A's record: code = %v, want NotFound", name, got)
		}
	}

	// B cannot borrow A's account for its own deal either: the reference is
	// resolved inside the caller's tenant, so A's account simply does not
	// exist here.
	own, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: b.opportunityID}, tokenB))
	if err != nil {
		t.Fatalf("[B] get own opportunity: %v", err)
	}
	borrow := own.Msg.GetOpportunity()
	borrow.AccountId = a.accountID
	if _, err := client.UpdateOpportunity(ctx, withToken(&crmv1.UpdateOpportunityRequest{Opportunity: borrow}, tokenB)); err == nil {
		t.Error("[B] attached its own deal to A's account")
	}

	// --- A is untouched -------------------------------------------------
	lead, err := client.GetLead(ctx, withToken(&crmv1.GetLeadRequest{Id: a.leadID}, tokenA))
	if err != nil {
		t.Fatalf("[A] re-read lead: %v", err)
	}
	if lead.Msg.GetLead().GetCompany() == "Owned by B" {
		t.Error("[A] lead was renamed by B")
	}
	deal, err := client.GetOpportunity(ctx, withToken(&crmv1.GetOpportunityRequest{Id: a.opportunityID}, tokenA))
	if err != nil {
		t.Fatalf("[A] re-read opportunity: %v", err)
	}
	if deal.Msg.GetOpportunity().GetName() == "Owned by B" {
		t.Error("[A] opportunity was renamed by B")
	}
	if deal.Msg.GetOpportunity().GetStageId() != "qualification" {
		t.Errorf("[A] opportunity stage = %q, want qualification", deal.Msg.GetOpportunity().GetStageId())
	}
}
