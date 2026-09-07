package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/calendar"
	"github.com/santoshguru/crm/internal/integration/gmail"
	"github.com/santoshguru/crm/internal/integration/google"
	"github.com/santoshguru/crm/internal/integration/googleoauth"
	"github.com/santoshguru/crm/internal/integration/privacy"
	"github.com/santoshguru/crm/internal/secrets"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ctxAs returns a context carrying an authenticated principal for the given
// tenant, the way the connect interceptor (internal/auth/auth.go) would
// after verifying a real JWT.
func ctxAs(tenantID, orgID string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Subject: "test-user", TenantID: tenantID, OrgID: orgID,
		Scopes: []string{"crm:*"},
	})
}

func newTestService() *CRMService {
	s, _ := newTestServiceWithStore()
	return s
}

// newTestServiceWithStore is newTestService for the tests that also need to
// reach the store directly — the Google connection ones, which have to seed a
// connection that no RPC can create without a real browser round-trip.
//
// The Google integration is wired with a stub provider rather than left nil,
// because a nil integration makes every Google isolation probe pass
// vacuously: an RPC that answers "not configured" leaks nothing no matter how
// badly it is written.
func newTestServiceWithStore() (*CRMService, domain.Store) {
	store := domain.NewMemStore()
	googleSvc := google.New(store, testGoogleClient(), "")
	// A syncer wired to a Gmail that answers nothing. The Gmail RPC tests
	// substitute their own; this keeps the field non-nil so the RPC reports a
	// real outcome instead of "not configured".
	transport := &http.Client{Transport: stubGoogleTransport{}}
	// No retention window: these tests are about the RPC surface, and a
	// sweep firing inside them would delete their own fixtures.
	privacySvc := privacy.New(store, 0, nil)
	syncer := gmail.NewSyncer(store, gmail.New(transport), googleSvc, privacySvc, nil)
	calendarSyncer := calendar.NewSyncer(store, calendar.New(transport), googleSvc, privacySvc, nil)
	// A real sealer, so the lead-capture RPCs behave here as they do in a
	// configured deployment rather than reporting "not enabled" — the tests
	// that care about the unconfigured state build their own service.
	sealer, err := secrets.New(testCaptureKey)
	if err != nil {
		panic("api tests: build capture sealer: " + err.Error())
	}
	return NewCRMService(config.Config{}, store, googleSvc, syncer, calendarSyncer,
		privacySvc, sealer, nil), store
}

// testCaptureKey seals lead-capture secrets in this package's tests. Base64
// of 32 bytes of nothing in particular; no real key is ever committed.
const testCaptureKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// testGoogleTokenKey is the sealing key this package's Google tests share, so
// a fixture can seal a credential the service can later open. It is 32 bytes
// of nothing in particular; no real key is ever committed.
const testGoogleTokenKey = "test-key-01234567890123456789012"

// testGoogleClient builds a provider client that cannot reach the network.
//
// The transport is a stub rather than a real one because a unit test must not
// depend on accounts.google.com being reachable, and — more to the point — a
// disconnect test that really called Google's revoke endpoint would be
// sending traffic to a third party from CI. Anything this stub is not
// explicitly taught to answer fails loudly, so an accidental real call shows
// up as a test failure rather than as silence.
func testGoogleClient() *googleoauth.Client {
	client, err := googleoauth.New(googleoauth.Config{
		ClientID:     "test-client.apps.googleusercontent.com",
		ClientSecret: "test-secret-not-a-real-one",
		RedirectURL:  "http://localhost:19431/integrations/google/callback",
		TokenKey:     base64.RawStdEncoding.EncodeToString([]byte(testGoogleTokenKey)),
	}, &http.Client{Transport: stubGoogleTransport{}})
	if err != nil {
		panic("test google oauth client: " + err.Error())
	}
	return client
}

type stubGoogleTransport struct{}

func (stubGoogleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := "{}"
	status := http.StatusOK
	switch {
	case strings.HasSuffix(r.URL.Path, "/revoke"):
		// Google answers a successful revoke with an empty 200.
	case strings.HasSuffix(r.URL.Path, "/profile"):
		body = `{"emailAddress":"stub@example.com"}`
	default:
		status = http.StatusNotFound
		body = `{"error":"the test stub was asked for an endpoint it does not model"}`
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    r,
	}, nil
}

// sealTestTokens produces a credential the service's own client can open,
// bound to the tenant and owner exactly as the real flow binds it.
func sealTestTokens(t *testing.T, tenantID, ownerID string) []byte {
	t.Helper()
	sealed, err := testGoogleClient().Encrypt(googleoauth.TokenSet{
		AccessToken:  "stub-access-token",
		RefreshToken: "stub-refresh-token",
		Expiry:       time.Now().UTC().Add(time.Hour),
	}, googleoauth.AssociatedData(tenantID, ownerID))
	if err != nil {
		t.Fatalf("seal test tokens: %v", err)
	}
	return sealed
}

func connectCode(t *testing.T, err error) connect.Code {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *connect.Error", err)
	}
	return ce.Code()
}

func TestCreateAccount_RequiresAuthenticatedTenant(t *testing.T) {
	s := newTestService()

	_, err := s.CreateAccount(context.Background(), connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme"},
	}))
	if err == nil {
		t.Fatal("expected an error for a request with no principal")
	}
	if got := connectCode(t, err); got != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", got)
	}

	noTenant := auth.WithPrincipal(context.Background(), &auth.Principal{Subject: "u"})
	_, err = s.CreateAccount(noTenant, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme"},
	}))
	if err == nil {
		t.Fatal("expected an error for a principal with no tenant")
	}
	if got := connectCode(t, err); got != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", got)
	}
}

func TestAccountCRUD_RoundTrips(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	created, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme Corp", Tier: "gold", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.Msg.GetAccount().GetId()
	if id == "" {
		t.Fatal("created account has no id")
	}

	got, err := s.GetAccount(ctx, connect.NewRequest(&crmv1.GetAccountRequest{Id: id}))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Msg.GetAccount().GetName() != "Acme Corp" {
		t.Fatalf("got name %q, want Acme Corp", got.Msg.GetAccount().GetName())
	}

	updateMsg := got.Msg.GetAccount()
	updateMsg.Name = "Acme Corporation"
	updated, err := s.UpdateAccount(ctx, connect.NewRequest(&crmv1.UpdateAccountRequest{Account: updateMsg}))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Msg.GetAccount().GetName() != "Acme Corporation" {
		t.Fatalf("got name %q after update, want Acme Corporation", updated.Msg.GetAccount().GetName())
	}

	if _, err := s.DeleteAccount(ctx, connect.NewRequest(&crmv1.DeleteAccountRequest{Id: id})); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.GetAccount(ctx, connect.NewRequest(&crmv1.GetAccountRequest{Id: id}))
	if got := connectCode(t, err); got != connect.CodeNotFound {
		t.Fatalf("get after delete: code = %v, want NotFound", got)
	}
}

func TestCreateAccount_InvalidArgumentOnBadInput(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	_, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: ""},
	}))
	if err == nil {
		t.Fatal("expected an error for a blank account name")
	}
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestAccountTenantIsolation(t *testing.T) {
	s := newTestService()
	ctxA := ctxAs("tenant-a", "org-a")
	ctxB := ctxAs("tenant-b", "org-b")

	created, err := s.CreateAccount(ctxA, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Tenant A Only", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := created.Msg.GetAccount().GetId()

	_, err = s.GetAccount(ctxB, connect.NewRequest(&crmv1.GetAccountRequest{Id: id}))
	if got := connectCode(t, err); got != connect.CodeNotFound {
		t.Fatalf("cross-tenant get: code = %v, want NotFound", got)
	}

	list, err := s.ListAccounts(ctxB, connect.NewRequest(&crmv1.ListAccountsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Msg.GetAccounts()) != 0 {
		t.Fatalf("tenant B sees %d accounts, want 0", len(list.Msg.GetAccounts()))
	}
}

func TestListAccounts_FilterSortAndInvalidSortRejected(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	for _, n := range []string{"Zebra Corp", "Acme Corp", "Acme Subsidiary"} {
		if _, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
			Account: &crmv1.Account{Name: n, OwnerId: "owner-1"},
		})); err != nil {
			t.Fatalf("seed %q: %v", n, err)
		}
	}

	resp, err := s.ListAccounts(ctx, connect.NewRequest(&crmv1.ListAccountsRequest{NameContains: "acme"}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Msg.GetAccounts()) != 2 {
		t.Fatalf("got %d accounts, want 2", len(resp.Msg.GetAccounts()))
	}

	_, err = s.ListAccounts(ctx, connect.NewRequest(&crmv1.ListAccountsRequest{Sort: "bogus"}))
	if err == nil {
		t.Fatal("expected an error for an unknown sort value")
	}
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestSearchRecords_IsTenantScopedAndRejectsShortQueries(t *testing.T) {
	s := newTestService()
	ctxA := ctxAs("tenant-a", "org-a")
	ctxB := ctxAs("tenant-b", "org-b")
	if _, err := s.CreateAccount(ctxA, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme Holdings", OwnerId: "owner-1"},
	})); err != nil {
		t.Fatalf("create account: %v", err)
	}

	result, err := s.SearchRecords(ctxA, connect.NewRequest(&crmv1.SearchRecordsRequest{Query: "acme", PageSize: 20}))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Msg.GetRecords()) != 1 || result.Msg.GetRecords()[0].GetTitle() != "Acme Holdings" {
		t.Fatalf("search records = %+v, want Acme Holdings", result.Msg.GetRecords())
	}
	foreign, err := s.SearchRecords(ctxB, connect.NewRequest(&crmv1.SearchRecordsRequest{Query: "acme", PageSize: 20}))
	if err != nil {
		t.Fatalf("search other tenant: %v", err)
	}
	if len(foreign.Msg.GetRecords()) != 0 {
		t.Fatalf("other tenant received %d search records", len(foreign.Msg.GetRecords()))
	}
	_, err = s.SearchRecords(ctxA, connect.NewRequest(&crmv1.SearchRecordsRequest{Query: "a"}))
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Fatalf("short query code = %v, want InvalidArgument", got)
	}
}

func TestListAccounts_PaginationTokenRoundTrips(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	const total = 12
	for i := range total {
		if _, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
			Account: &crmv1.Account{Name: fmt.Sprintf("acct-%02d", i), OwnerId: "owner-1"},
		})); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	token := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("pagination did not terminate")
		}
		resp, err := s.ListAccounts(ctx, connect.NewRequest(&crmv1.ListAccountsRequest{PageSize: 5, PageToken: token}))
		if err != nil {
			t.Fatalf("list page: %v", err)
		}
		for _, a := range resp.Msg.GetAccounts() {
			if seen[a.GetId()] {
				t.Fatalf("account %s returned twice across pages", a.GetId())
			}
			seen[a.GetId()] = true
		}
		if resp.Msg.GetNextPageToken() == "" {
			break
		}
		token = resp.Msg.GetNextPageToken()
	}
	if len(seen) != total {
		t.Fatalf("paged through %d accounts, want %d", len(seen), total)
	}
}

func TestUpdateAccount_OptimisticConcurrencyConflict(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	created, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Original", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	acct := created.Msg.GetAccount()

	// A stale UpdatedAt (as if read before someone else's concurrent write)
	// must be rejected rather than silently overwriting.
	firstEdit := &crmv1.Account{Id: acct.GetId(), Name: "First writer", OwnerId: "owner-1"}
	firstUpdated, err := s.UpdateAccount(ctx, connect.NewRequest(&crmv1.UpdateAccountRequest{Account: firstEdit}))
	if err != nil {
		t.Fatalf("first update: %v", err)
	}

	// An hour before the true current UpdatedAt is unambiguously stale
	// regardless of clock resolution — unlike reusing acct.GetCreatedAt(),
	// which could (rarely, under heavy scheduling contention) collide with
	// the first update's timestamp and make this test flaky.
	staleAt := timestamppb.New(firstUpdated.Msg.GetAccount().GetUpdatedAt().AsTime().Add(-time.Hour))
	staleEdit := &crmv1.Account{Id: acct.GetId(), Name: "Second writer", OwnerId: "owner-1", UpdatedAt: staleAt}
	_, err = s.UpdateAccount(ctx, connect.NewRequest(&crmv1.UpdateAccountRequest{Account: staleEdit}))
	if err == nil {
		t.Fatal("expected a conflict for a stale UpdatedAt")
	}
	if got := connectCode(t, err); got != connect.CodeAborted {
		t.Fatalf("code = %v, want Aborted", got)
	}
}

func TestContactCRUD_RoundTrips(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	acct, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acct", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	created, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{Email: "alice@example.com", AccountId: acct.Msg.GetAccount().GetId()},
	}))
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	id := created.Msg.GetContact().GetId()

	got, err := s.GetContact(ctx, connect.NewRequest(&crmv1.GetContactRequest{Id: id}))
	if err != nil {
		t.Fatalf("get contact: %v", err)
	}
	if got.Msg.GetContact().GetEmail() != "alice@example.com" {
		t.Fatalf("got email %q", got.Msg.GetContact().GetEmail())
	}

	if _, err := s.DeleteContact(ctx, connect.NewRequest(&crmv1.DeleteContactRequest{Id: id})); err != nil {
		t.Fatalf("delete contact: %v", err)
	}
	_, err = s.GetContact(ctx, connect.NewRequest(&crmv1.GetContactRequest{Id: id}))
	if got := connectCode(t, err); got != connect.CodeNotFound {
		t.Fatalf("get after delete: code = %v, want NotFound", got)
	}
}

func TestCreateContact_RejectsDanglingAccountID(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	_, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{Email: "alice@example.com", AccountId: "does-not-exist"},
	}))
	if err == nil {
		t.Fatal("expected an error for a dangling account_id")
	}
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateContact_FlagsDuplicateEmailWithoutBlocking(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	acct, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acct", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	accountID := acct.Msg.GetAccount().GetId()

	first, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{Email: "dup@example.com", AccountId: accountID},
	}))
	if err != nil {
		t.Fatalf("create first contact: %v", err)
	}

	second, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{Email: "dup@example.com", AccountId: accountID},
	}))
	if err != nil {
		t.Fatalf("create duplicate-email contact: %v", err)
	}
	if second.Msg.GetContact().GetDuplicateOfContactId() != first.Msg.GetContact().GetId() {
		t.Fatalf("DuplicateOfContactId = %q, want %q",
			second.Msg.GetContact().GetDuplicateOfContactId(), first.Msg.GetContact().GetId())
	}
}

// TestOpportunityClosingWithoutClientDate verifies the wire behaviour used by
// the web app: it submits a close reason and terminal stage but leaves
// close_date unset so the domain can stamp the authoritative closing time.
func TestOpportunityClosingWithoutClientDate(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	account, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	created, err := s.CreateOpportunity(ctx, connect.NewRequest(&crmv1.CreateOpportunityRequest{
		Opportunity: &crmv1.Opportunity{Name: "Deal", AccountId: account.Msg.GetAccount().GetId()},
	}))
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}

	deal := created.Msg.GetOpportunity()
	for _, stage := range []string{"qualification", "proposal", "negotiation"} {
		deal.Stage = stage
		advanced, err := s.UpdateOpportunity(ctx, connect.NewRequest(&crmv1.UpdateOpportunityRequest{Opportunity: deal}))
		if err != nil {
			t.Fatalf("advance to %s: %v", stage, err)
		}
		deal = advanced.Msg.GetOpportunity()
	}

	deal.Stage = "closed_won"
	deal.CloseReason = "Selected after final review"
	deal.CloseDate = nil
	closed, err := s.UpdateOpportunity(ctx, connect.NewRequest(&crmv1.UpdateOpportunityRequest{Opportunity: deal}))
	if err != nil {
		t.Fatalf("close opportunity without client date: %v", err)
	}
	if closed.Msg.GetOpportunity().GetCloseDate() == nil || closed.Msg.GetOpportunity().GetCloseDate().AsTime().IsZero() {
		t.Fatal("closed opportunity must have a server-stamped close date")
	}
}

func TestMoveOpportunityToPipeline(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")
	account, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	created, err := s.CreateOpportunity(ctx, connect.NewRequest(&crmv1.CreateOpportunityRequest{
		Opportunity: &crmv1.Opportunity{Name: "Deal", AccountId: account.Msg.GetAccount().GetId(), Amount: 750},
	}))
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	destination, err := s.CreatePipeline(ctx, connect.NewRequest(&crmv1.CreatePipelineRequest{
		Pipeline: &crmv1.Pipeline{Name: "Partner sales"},
	}))
	if err != nil {
		t.Fatalf("create destination pipeline: %v", err)
	}
	target, err := s.CreatePipelineStage(ctx, connect.NewRequest(&crmv1.CreatePipelineStageRequest{
		Stage: &crmv1.PipelineStage{PipelineId: destination.Msg.GetPipeline().GetId(), Name: "Discovery", Position: 0, Probability: 10, TerminalType: "open"},
	}))
	if err != nil {
		t.Fatalf("create destination stage: %v", err)
	}

	deal := created.Msg.GetOpportunity()
	moved, err := s.MoveOpportunityToPipeline(ctx, connect.NewRequest(&crmv1.MoveOpportunityToPipelineRequest{
		OpportunityId: deal.GetId(), PipelineId: destination.Msg.GetPipeline().GetId(), StageId: target.Msg.GetStage().GetId(), UpdatedAt: deal.GetUpdatedAt(),
	}))
	if err != nil {
		t.Fatalf("move opportunity: %v", err)
	}
	if got := moved.Msg.GetOpportunity(); got.GetPipelineId() != destination.Msg.GetPipeline().GetId() || got.GetStageId() != target.Msg.GetStage().GetId() || got.GetStage() != "Discovery" || got.GetAmount() != 750 || got.GetUpdatedAt() == nil {
		t.Fatalf("moved opportunity = %+v, want destination and preserved fields", got)
	}
	history, err := s.ListOpportunityStageHistory(ctx, connect.NewRequest(&crmv1.ListOpportunityStageHistoryRequest{OpportunityId: deal.GetId()}))
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history.Msg.GetChanges()) != 1 || history.Msg.GetChanges()[0].GetToStage() != "Discovery" {
		t.Fatalf("history = %+v, want one move to Discovery", history.Msg.GetChanges())
	}
}

func TestListOpportunityStageHistoryIsTenantScoped(t *testing.T) {
	s := newTestService()
	ctxA := ctxAs("tenant-a", "org-a")
	ctxB := ctxAs("tenant-b", "org-b")
	account, err := s.CreateAccount(ctxA, connect.NewRequest(&crmv1.CreateAccountRequest{Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"}}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	created, err := s.CreateOpportunity(ctxA, connect.NewRequest(&crmv1.CreateOpportunityRequest{Opportunity: &crmv1.Opportunity{Name: "Deal", AccountId: account.Msg.GetAccount().GetId()}}))
	if err != nil {
		t.Fatalf("create opportunity: %v", err)
	}
	deal := created.Msg.GetOpportunity()
	deal.Stage = "qualification"
	if _, err := s.UpdateOpportunity(ctxA, connect.NewRequest(&crmv1.UpdateOpportunityRequest{Opportunity: deal})); err != nil {
		t.Fatalf("advance opportunity: %v", err)
	}
	history, err := s.ListOpportunityStageHistory(ctxA, connect.NewRequest(&crmv1.ListOpportunityStageHistoryRequest{OpportunityId: deal.GetId()}))
	if err != nil {
		t.Fatalf("list own history: %v", err)
	}
	if len(history.Msg.GetChanges()) != 1 || history.Msg.GetChanges()[0].GetToStage() != "qualification" {
		t.Fatalf("unexpected history: %+v", history.Msg.GetChanges())
	}
	foreign, err := s.ListOpportunityStageHistory(ctxB, connect.NewRequest(&crmv1.ListOpportunityStageHistoryRequest{OpportunityId: deal.GetId()}))
	if err != nil {
		t.Fatalf("list foreign history: %v", err)
	}
	if len(foreign.Msg.GetChanges()) != 0 {
		t.Fatalf("foreign tenant read %d stage changes", len(foreign.Msg.GetChanges()))
	}
}

func TestPipelineAPI_IsTenantScoped(t *testing.T) {
	s := newTestService()
	ctxA := ctxAs("tenant-a", "org-a")
	ctxB := ctxAs("tenant-b", "org-b")

	created, err := s.CreatePipeline(ctxA, connect.NewRequest(&crmv1.CreatePipelineRequest{
		Pipeline: &crmv1.Pipeline{Name: "Enterprise sales"},
	}))
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	pipelineID := created.Msg.GetPipeline().GetId()
	if _, err := s.CreatePipelineStage(ctxA, connect.NewRequest(&crmv1.CreatePipelineStageRequest{
		Stage: &crmv1.PipelineStage{PipelineId: pipelineID, Name: "Discovery", Position: 0, Probability: 10},
	})); err != nil {
		t.Fatalf("create pipeline stage: %v", err)
	}

	list, err := s.ListPipelines(ctxB, connect.NewRequest(&crmv1.ListPipelinesRequest{}))
	if err != nil {
		t.Fatalf("list tenant B pipelines: %v", err)
	}
	if len(list.Msg.GetPipelines()) != 0 {
		t.Fatalf("tenant B sees %d pipelines, want 0", len(list.Msg.GetPipelines()))
	}
	_, err = s.ListPipelineStages(ctxB, connect.NewRequest(&crmv1.ListPipelineStagesRequest{PipelineId: pipelineID}))
	if got := connectCode(t, err); got != connect.CodeNotFound {
		t.Fatalf("cross-tenant stages code = %v, want NotFound", got)
	}
}

func TestListContacts_AccountFilterAndInvalidSortRejected(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-a", "org-a")

	acctA, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{Account: &crmv1.Account{Name: "A", OwnerId: "owner-1"}}))
	if err != nil {
		t.Fatal(err)
	}
	acctB, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{Account: &crmv1.Account{Name: "B", OwnerId: "owner-1"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{Email: "a@x.com", AccountId: acctA.Msg.GetAccount().GetId()},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{Email: "b@x.com", AccountId: acctB.Msg.GetAccount().GetId()},
	})); err != nil {
		t.Fatal(err)
	}

	resp, err := s.ListContacts(ctx, connect.NewRequest(&crmv1.ListContactsRequest{AccountId: acctA.Msg.GetAccount().GetId()}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Msg.GetContacts()) != 1 || resp.Msg.GetContacts()[0].GetEmail() != "a@x.com" {
		t.Fatalf("account filter: got %+v", resp.Msg.GetContacts())
	}

	_, err = s.ListContacts(ctx, connect.NewRequest(&crmv1.ListContactsRequest{Sort: "bogus"}))
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestGetSummary_CountsAreTenantScoped(t *testing.T) {
	s := newTestService()
	ctxA := ctxAs("tenant-a", "org-a")
	ctxB := ctxAs("tenant-b", "org-b")

	if _, err := s.CreateAccount(ctxA, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "A1", OwnerId: "owner-1"},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(ctxA, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "A2", OwnerId: "owner-1"},
	})); err != nil {
		t.Fatal(err)
	}

	sumA, err := s.GetSummary(ctxA, connect.NewRequest(&crmv1.GetSummaryRequest{}))
	if err != nil {
		t.Fatalf("summary A: %v", err)
	}
	if sumA.Msg.GetTotal() != 2 {
		t.Fatalf("tenant A total = %d, want 2", sumA.Msg.GetTotal())
	}

	sumB, err := s.GetSummary(ctxB, connect.NewRequest(&crmv1.GetSummaryRequest{}))
	if err != nil {
		t.Fatalf("summary B: %v", err)
	}
	if sumB.Msg.GetTotal() != 0 {
		t.Fatalf("tenant B total = %d, want 0", sumB.Msg.GetTotal())
	}
}
