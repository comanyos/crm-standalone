package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/gen/crm/v1/crmv1connect"
	platformv1 "github.com/santoshguru/crm/gen/platform/v1"
	"github.com/santoshguru/crm/gen/platform/v1/platformv1connect"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
)

// End-to-end scope enforcement (task.md §1.4): a real HTTP server, real
// signed tokens verified against a real JWKS, the real interceptor, and the
// real handlers over a live store.
//
// The unit tests above reason about the policy as data. This file is the one
// that would catch the policy being correct but not actually wired — an
// interceptor registered on one handler and not the other, say, which every
// data-level test would still pass.

const enforcementKID = "policy-test-key"

// jwksFor serves the public half of key and returns the JWKS URL.
func jwksFor(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	eBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(eBuf, uint32(pub.E))
	i := 0
	for i < len(eBuf)-1 && eBuf[i] == 0 {
		i++
	}
	doc := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": enforcementKID,
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(eBuf[i:]),
	}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// liveServer wires a real CRM server behind a real authenticator and returns
// clients for both registered services plus a token minter for arbitrary
// scope sets.
func liveServer(t *testing.T) (crmv1connect.CRMServiceClient, platformv1connect.ModuleServiceClient, func(org string, scopes []string) string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	authn, err := auth.FromConfig(context.Background(), jwksFor(t, &key.PublicKey), "authincore", "")
	if err != nil {
		t.Fatalf("build authenticator: %v", err)
	}

	srv, err := New(config.Config{}, domain.NewMemStore(), authn)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(srv.Close)

	http := httptest.NewServer(srv.Handler())
	t.Cleanup(http.Close)

	mint := func(org string, scopes []string) string {
		t.Helper()
		claims := jwt.MapClaims{
			"sub": "user-" + org, "iss": "authincore", "org_id": org,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}
		if scopes != nil {
			raw := make([]any, 0, len(scopes))
			for _, s := range scopes {
				raw = append(raw, s)
			}
			claims["scopes"] = raw
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = enforcementKID
		signed, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}

	return crmv1connect.NewCRMServiceClient(http.Client(), http.URL),
		platformv1connect.NewModuleServiceClient(http.Client(), http.URL),
		mint
}

// withToken attaches a bearer token to a request.
func withToken[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

// TestViewerCannotWriteThroughTheRealServer is the lower-privilege behaviour
// task.md §1.3's acceptance criteria also asked for: a read-only role is
// refused every mutation, over HTTP, by the deployed configuration.
func TestViewerCannotWriteThroughTheRealServer(t *testing.T) {
	client, _, mint := liveServer(t)
	ctx := context.Background()

	admin := mint("org-1", []string{"crm:*"})
	viewer := mint("org-1", []string{
		"crm:account:read", "crm:contact:read", "crm:lead:read",
		"crm:opportunity:read", "crm:pipeline:read", "crm:activity:read",
		"crm:summary:read",
	})

	created, err := client.CreateAccount(ctx, withToken(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}, admin))
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}
	id := created.Msg.GetAccount().GetId()

	// The viewer may read it.
	if _, err := client.GetAccount(ctx, withToken(&crmv1.GetAccountRequest{Id: id}, viewer)); err != nil {
		t.Fatalf("viewer read: %v", err)
	}

	// And may not change or remove it.
	_, err = client.UpdateAccount(ctx, withToken(&crmv1.UpdateAccountRequest{
		Account: &crmv1.Account{Id: id, Name: "Renamed", OwnerId: "owner-1"},
	}, viewer))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Errorf("viewer update code = %v, want PermissionDenied", got)
	}
	_, err = client.DeleteAccount(ctx, withToken(&crmv1.DeleteAccountRequest{Id: id}, viewer))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Errorf("viewer delete code = %v, want PermissionDenied", got)
	}

	// The record is untouched: a denied write must not have partially applied.
	after, err := client.GetAccount(ctx, withToken(&crmv1.GetAccountRequest{Id: id}, admin))
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got := after.Msg.GetAccount().GetName(); got != "Acme" {
		t.Errorf("account name = %q after denied writes, want %q", got, "Acme")
	}
}

// TestScopeDenialIsNotAnExistenceOracle checks the shape of the refusal, not
// just its code. The scope check runs before the handler, so a caller without
// the scope gets an identical answer whether or not the id names a real
// record — otherwise "PermissionDenied here, NotFound there" would let a
// viewer enumerate ids they may not read.
func TestScopeDenialIsNotAnExistenceOracle(t *testing.T) {
	client, _, mint := liveServer(t)
	ctx := context.Background()

	admin := mint("org-1", []string{"crm:*"})
	created, err := client.CreateAccount(ctx, withToken(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}, admin))
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}
	realID := created.Msg.GetAccount().GetId()

	// A principal with no account scope at all.
	blind := mint("org-1", []string{"crm:summary:read"})

	var codes []connect.Code
	var messages []string
	for _, id := range []string{realID, "definitely-not-a-real-id"} {
		_, err := client.GetAccount(ctx, withToken(&crmv1.GetAccountRequest{Id: id}, blind))
		if err == nil {
			t.Fatalf("a principal without crm:account:read read account %q", id)
		}
		codes = append(codes, connect.CodeOf(err))
		messages = append(messages, err.Error())
	}
	if codes[0] != connect.CodePermissionDenied || codes[1] != connect.CodePermissionDenied {
		t.Errorf("codes = %v, want both PermissionDenied", codes)
	}
	if messages[0] != messages[1] {
		t.Errorf("the refusal differs between a real and a fabricated id:\n  real: %s\n  fake: %s\n"+
			"that difference is an existence oracle", messages[0], messages[1])
	}
}

// TestMissingScopeIsDeniedForEveryWriteProcedure sweeps the whole catalog
// rather than sampling it: for each procedure, a principal holding every
// scope except the one it requires must be refused.
func TestMissingScopeIsDeniedForEveryWriteProcedure(t *testing.T) {
	client, _, mint := liveServer(t)
	ctx := context.Background()

	// One representative call per write scope. The point is the interceptor,
	// so the arguments only need to be well-formed enough to reach it.
	calls := map[string]func(token string) error{
		"crm:account:write": func(tok string) error {
			_, err := client.DeleteAccount(ctx, withToken(&crmv1.DeleteAccountRequest{Id: "x"}, tok))
			return err
		},
		"crm:contact:write": func(tok string) error {
			_, err := client.DeleteContact(ctx, withToken(&crmv1.DeleteContactRequest{Id: "x"}, tok))
			return err
		},
		"crm:lead:write": func(tok string) error {
			_, err := client.DeleteLead(ctx, withToken(&crmv1.DeleteLeadRequest{Id: "x"}, tok))
			return err
		},
		"crm:opportunity:write": func(tok string) error {
			_, err := client.DeleteOpportunity(ctx, withToken(&crmv1.DeleteOpportunityRequest{Id: "x"}, tok))
			return err
		},
		"crm:activity:write": func(tok string) error {
			_, err := client.DeleteActivity(ctx, withToken(&crmv1.DeleteActivityRequest{Id: "x"}, tok))
			return err
		},
		"crm:pipeline:write": func(tok string) error {
			_, err := client.CreatePipeline(ctx, withToken(&crmv1.CreatePipelineRequest{
				Pipeline: &crmv1.Pipeline{Name: "P"},
			}, tok))
			return err
		},
		"crm:task:write": func(tok string) error {
			// CompleteTask rather than DeleteTask: completion is the
			// transition a read-only role is most likely to be handed a
			// button for by mistake.
			_, err := client.CompleteTask(ctx, withToken(&crmv1.CompleteTaskRequest{Id: "x"}, tok))
			return err
		},
		"crm:note:write": func(tok string) error {
			_, err := client.DeleteNote(ctx, withToken(&crmv1.DeleteNoteRequest{Id: "x"}, tok))
			return err
		},
		"crm:custom_field:write": func(tok string) error {
			_, err := client.ArchiveCustomFieldDefinition(ctx, withToken(&crmv1.ArchiveCustomFieldDefinitionRequest{Id: "x"}, tok))
			return err
		},
		"crm:custom_object:write": func(tok string) error {
			_, err := client.CreateCustomObjectDefinition(ctx, withToken(&crmv1.CreateCustomObjectDefinitionRequest{Definition: &crmv1.CustomObjectDefinition{ApiName: "asset", Label: "Asset", PluralLabel: "Assets"}}, tok))
			return err
		},
		"crm:integration_admin:write": func(tok string) error {
			// Erasure rather than suppression: deleting a customer's
			// correspondence history is the call that must not be reachable
			// by a principal without administrative authority.
			_, err := client.EraseSyncedContentForContact(ctx, withToken(
				&crmv1.EraseSyncedContentForContactRequest{ContactId: "x"}, tok))
			return err
		},
		"crm:workflow:write": func(tok string) error {
			// SetWorkflowRuleStatus rather than CreateWorkflowRule:
			// activating a rule is the call that starts it acting on every
			// record its trigger matches, so it is the one that must not be
			// reachable without authoring authority.
			_, err := client.SetWorkflowRuleStatus(ctx, withToken(
				&crmv1.SetWorkflowRuleStatusRequest{Id: "x", Status: "active"}, tok))
			return err
		},
		"crm:notification:write": func(tok string) error {
			_, err := client.MarkNotificationRead(ctx, withToken(
				&crmv1.MarkNotificationReadRequest{Id: "x"}, tok))
			return err
		},
		"crm:integration:write": func(tok string) error {
			// StartGoogleConnection rather than Disconnect: beginning an
			// authorization is the call that would hand a token to a
			// principal who should not have one, and the scope check must
			// reject it before any state is written.
			_, err := client.StartGoogleConnection(ctx, withToken(&crmv1.StartGoogleConnectionRequest{}, tok))
			return err
		},
		"crm:tag:write": func(tok string) error {
			_, err := client.CreateTag(ctx, withToken(&crmv1.CreateTagRequest{Name: "x"}, tok))
			return err
		},
		"crm:saved_view:write": func(tok string) error {
			_, err := client.CreateSavedView(ctx, withToken(&crmv1.CreateSavedViewRequest{ObjectType: "account", Name: "x"}, tok))
			return err
		},
		"crm:consent:write": func(tok string) error {
			// Granting rather than withdrawing, because granting is the
			// direction that adds authority: a principal who could assert
			// that a third party opted in could re-permit somebody the
			// organization had been told to stop contacting.
			_, err := client.RecordMessagingConsent(ctx, withToken(&crmv1.RecordMessagingConsentRequest{
				ChannelKind: "email", Address: "x@example.com", State: "granted",
			}, tok))
			return err
		},
	}

	// Every write scope this service demands needs a probe here.
	demanded := map[string]bool{}
	for _, scope := range crmScopes {
		if len(scope) > 6 && scope[len(scope)-6:] == ":write" {
			demanded[scope] = true
		}
	}
	for scope := range demanded {
		if _, ok := calls[scope]; !ok {
			t.Errorf("no denial probe for write scope %q", scope)
		}
	}

	for withheld, call := range calls {
		t.Run(withheld, func(t *testing.T) {
			var granted []string
			for _, scope := range []string{
				"crm:account:read", "crm:account:write",
				"crm:contact:read", "crm:contact:write",
				"crm:lead:read", "crm:lead:write",
				"crm:opportunity:read", "crm:opportunity:write",
				"crm:pipeline:read", "crm:pipeline:write",
				"crm:activity:read", "crm:activity:write",
				"crm:custom_field:read", "crm:custom_field:write",
				"crm:custom_object:read", "crm:custom_object:write",
				"crm:integration:read", "crm:integration:write",
				"crm:integration_admin:read", "crm:integration_admin:write",
				"crm:summary:read",
			} {
				if scope != withheld {
					granted = append(granted, scope)
				}
			}
			err := call(mint("org-1", granted))
			if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
				t.Errorf("withholding %q still allowed the call: code = %v, want PermissionDenied",
					withheld, got)
			}
		})
	}
}

// TestNoTokenIsUnauthenticatedNotDenied keeps the two failures distinct: a
// caller who sent no credentials should be told to authenticate, not that
// they lack permission.
func TestNoTokenIsUnauthenticatedNotDenied(t *testing.T) {
	client, _, _ := liveServer(t)
	_, err := client.GetSummary(context.Background(), connect.NewRequest(&crmv1.GetSummaryRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", got)
	}
}

// TestUnscopedProceduresWorkForAScopelessPrincipal confirms the waiver list
// is real end to end: a principal holding no CRM scope at all can still reach
// the module manifest and health, which is why the shell can render before it
// knows what the user may do — and cannot reach anything else.
func TestUnscopedProceduresWorkForAScopelessPrincipal(t *testing.T) {
	crm, module, mint := liveServer(t)
	ctx := context.Background()
	scopeless := mint("org-1", nil)

	if _, err := module.GetManifest(ctx, withToken(&platformv1.GetManifestRequest{}, scopeless)); err != nil {
		t.Errorf("GetManifest is unscoped but failed for a scopeless principal: %v", err)
	}
	if _, err := module.GetHealth(ctx, withToken(&platformv1.GetHealthRequest{}, scopeless)); err != nil {
		t.Errorf("GetHealth is unscoped but failed for a scopeless principal: %v", err)
	}
	if _, err := module.ListActions(ctx, withToken(&platformv1.ListActionsRequest{}, scopeless)); err != nil {
		t.Errorf("ListActions is unscoped but failed for a scopeless principal: %v", err)
	}

	// The waiver stops there: no CRM data is reachable without a CRM scope.
	_, err := crm.GetSummary(ctx, withToken(&crmv1.GetSummaryRequest{}, scopeless))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Errorf("a scopeless principal read the summary: code = %v, want PermissionDenied", got)
	}
	_, err = crm.ListAccounts(ctx, withToken(&crmv1.ListAccountsRequest{}, scopeless))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Errorf("a scopeless principal listed accounts: code = %v, want PermissionDenied", got)
	}
}

// TestRoleChangesTakeEffectOnlyAtTheNextToken pins the session boundary
// (task.md §1.4).
//
// Authority is carried in the access token, and this service verifies tokens
// offline against JWKS rather than asking AuthinCore per request. So granting
// or revoking a role does not change what a client can do until that client
// presents a token minted after the change — its existing token keeps exactly
// the authority it was signed with, until it expires.
//
// That is a deliberate trade (a network round trip per request is the
// alternative), but it is the kind of behaviour an operator discovers at the
// worst moment: revoking a role and watching the user carry on working. The
// window is the access-token lifetime, and this test is where that is written
// down rather than assumed.
func TestRoleChangesTakeEffectOnlyAtTheNextToken(t *testing.T) {
	client, _, mint := liveServer(t)
	ctx := context.Background()

	admin := mint("org-1", []string{"crm:*"})
	created, err := client.CreateAccount(ctx, withToken(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Acme", OwnerId: "owner-1"},
	}, admin))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := created.Msg.GetAccount().GetId()

	// The same person, before and after a role change. Both tokens are valid
	// and unexpired; only their scope sets differ.
	beforeRevocation := mint("org-1", []string{"crm:account:read", "crm:account:write"})
	afterRevocation := mint("org-1", []string{"crm:account:read"})

	// The newly minted, reduced token is refused the write immediately.
	_, err = client.UpdateAccount(ctx, withToken(&crmv1.UpdateAccountRequest{
		Account: &crmv1.Account{Id: id, Name: "Renamed by new token", OwnerId: "owner-1"},
	}, afterRevocation))
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Errorf("the post-change token still had write authority: code = %v", got)
	}

	// The token issued before the change still carries it. This is the
	// documented window, not a defect — but it must be a window, not a
	// permanent grant, which is why auth/jwks.go requires an exp claim.
	if _, err := client.UpdateAccount(ctx, withToken(&crmv1.UpdateAccountRequest{
		Account: &crmv1.Account{Id: id, Name: "Renamed by old token", OwnerId: "owner-1"},
	}, beforeRevocation)); err != nil {
		t.Fatalf("the pre-change token unexpectedly lost its authority: %v", err)
	}

	// The window closes at expiry, which is enforced rather than assumed:
	// internal/auth requires an exp claim, so no token can carry stale
	// authority indefinitely (TestDeactivatedOrganizationIsBoundedByTokenExpiry).
}

// TestReadyzReportsUnavailableWithoutSigningKeys covers the readiness
// endpoint (task.md §1.2).
//
// /healthz and /readyz answer different questions and an orchestrator uses
// them differently. Liveness says the process is up; restarting on an
// upstream outage turns a recoverable blip into a crash loop. Readiness says
// this instance can do its job — and it cannot, if it holds no signing keys,
// because then every authenticated request fails in a way that looks to the
// user like their own credentials are wrong.
func TestReadyzReportsUnavailableWithoutSigningKeys(t *testing.T) {
	// A JWKS endpoint that never answers with keys.
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(jwks.Close)

	authn, err := auth.FromConfig(context.Background(), jwks.URL, "authincore", "")
	if err != nil {
		t.Fatalf("the authenticator refused to start degraded: %v", err)
	}
	srv, err := New(config.Config{}, domain.NewMemStore(), authn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Liveness stays green: the process is fine, its dependency is not.
	live, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = live.Body.Close() }()
	if live.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d while only the identity provider is down; want 200 — "+
			"a liveness probe that fails here would crash-loop the service", live.StatusCode)
	}

	// Readiness is not.
	ready, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = ready.Body.Close() }()
	if ready.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d with no signing keys; want 503", ready.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(ready.Body).Decode(&body); err != nil {
		t.Fatalf("decode /readyz body: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Errorf("/readyz status = %q, want \"not_ready\"", body["status"])
	}
	// The reason names the subsystem and nothing else: a readiness endpoint is
	// usually reachable without credentials.
	if strings.Contains(body["reason"], jwks.URL) {
		t.Errorf("/readyz leaks the JWKS URL to an unauthenticated caller: %q", body["reason"])
	}
}

// TestReadyzReportsReadyWithSigningKeys is the positive case, so the test
// above cannot pass merely because /readyz always fails.
func TestReadyzReportsReadyWithSigningKeys(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	authn, err := auth.FromConfig(context.Background(), jwksFor(t, &key.PublicKey), "authincore", "")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(config.Config{}, domain.NewMemStore(), authn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz = %d with a working JWKS; want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("/readyz status = %q, want \"ready\"", body["status"])
	}
}
