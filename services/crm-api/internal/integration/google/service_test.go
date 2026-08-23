package google

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/googleoauth"
)

// The whole authorization flow, end to end against a stub provider.
//
// These are the tests that decide whether the feature is safe, because the
// flow's guarantees do not live in any single layer: the store makes a state
// one-time, the provider client does PKCE and sealing, and this package is
// what actually wires them together in the right order. A correct store and a
// correct client, connected wrongly, is still a replayable authorization.

const testTokenKey = "google-test-key-0123456789012345"

// stubProvider stands in for Google. It records what it was asked and answers
// from fields the test sets, so a test can make the provider misbehave —
// which is most of what is worth asserting here.
type stubProvider struct {
	tokenStatus   int
	tokenBody     string
	profileStatus int
	profileBody   string
	revokeStatus  int

	exchangeForm url.Values
	revokedToken string
	revokeCalls  int
}

func newStubProvider() *stubProvider {
	return &stubProvider{
		tokenStatus: http.StatusOK,
		tokenBody: `{"access_token":"access-1","refresh_token":"refresh-1","token_type":"Bearer",
			"scope":"https://www.googleapis.com/auth/gmail.readonly https://www.googleapis.com/auth/calendar.events",
			"expires_in":3600}`,
		profileStatus: http.StatusOK,
		profileBody:   `{"emailAddress":"person@example.com"}`,
		revokeStatus:  http.StatusOK,
	}
}

func (p *stubProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/token"):
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			p.exchangeForm, _ = url.ParseQuery(string(raw))
		}
		return respond(p.tokenStatus, p.tokenBody)
	case strings.HasSuffix(r.URL.Path, "/revoke"):
		p.revokeCalls++
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			p.revokedToken = form.Get("token")
		}
		return respond(p.revokeStatus, "")
	case strings.HasSuffix(r.URL.Path, "/profile"):
		return respond(p.profileStatus, p.profileBody)
	}
	return respond(http.StatusNotFound, `{"error":"unmodelled endpoint"}`)
}

func newTestService(t *testing.T, provider *stubProvider, webBaseURL string) (*Service, domain.Store) {
	t.Helper()
	client, err := googleoauth.New(googleoauth.Config{
		ClientID:     "client.apps.googleusercontent.com",
		ClientSecret: "test-secret",
		RedirectURL:  "http://localhost:19431/integrations/google/callback",
		TokenKey:     base64.RawStdEncoding.EncodeToString([]byte(testTokenKey)),
	}, &http.Client{Transport: provider})
	if err != nil {
		t.Fatal(err)
	}
	store := domain.NewMemStore()
	return New(store, client, webBaseURL), store
}

func testScope() domain.Scope { return domain.Scope{TenantID: "tenant-1", OrgID: "org-1"} }

// stateFrom pulls the one-time state out of an authorization URL, which is
// the only place a test can legitimately learn it — exactly as the browser
// does. The service deliberately does not return it separately.
func stateFrom(t *testing.T, authorizationURL string) string {
	t.Helper()
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorization URL carries no state")
	}
	return state
}

// TestConnectFlowStoresASealedCredential is the happy path, asserted at the
// level that matters: what ends up in the database.
func TestConnectFlowStoresASealedCredential(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "/settings/integrations")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.HasPrefix(authorizationURL, "https://accounts.google.com/") {
		t.Fatalf("authorization URL = %q", authorizationURL)
	}
	state := stateFrom(t, authorizationURL)

	result := svc.Complete(ctx, state, "auth-code", "")
	if !result.Connected {
		t.Fatalf("callback did not connect: reason %q", result.Reason)
	}
	if result.Email != "person@example.com" {
		t.Errorf("connected account = %q, want the address read back from the provider", result.Email)
	}
	if result.ReturnPath != "/settings/integrations" {
		t.Errorf("return path = %q, want the one recorded at Start", result.ReturnPath)
	}

	// PKCE actually travelled: the verifier reached the token endpoint.
	if provider.exchangeForm.Get("code_verifier") == "" {
		t.Error("the token exchange sent no PKCE verifier")
	}

	stored, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read stored connection: %v", err)
	}
	if stored.Status != domain.GoogleStatusConnected {
		t.Errorf("stored status = %q", stored.Status)
	}
	// The stored bytes must be sealed, not the token in the clear.
	if strings.Contains(string(stored.TokenCiphertext), "refresh-1") ||
		strings.Contains(string(stored.TokenCiphertext), "access-1") {
		t.Fatal("the stored credential contains a plaintext token")
	}
	// And it must be usable by the service that stored it.
	token, err := svc.AccessToken(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	if token != "access-1" {
		t.Errorf("access token = %q, want access-1", token)
	}
}

// TestStateCannotBeReplayed: the same callback arriving twice must connect
// once. This is the property an intercepted redirect attacks.
func TestStateCannotBeReplayed(t *testing.T) {
	provider := newStubProvider()
	svc, _ := newTestService(t, provider, "")
	ctx := context.Background()

	authorizationURL, err := svc.Start(ctx, testScope(), "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	state := stateFrom(t, authorizationURL)

	if first := svc.Complete(ctx, state, "auth-code", ""); !first.Connected {
		t.Fatalf("first callback: %q", first.Reason)
	}
	second := svc.Complete(ctx, state, "auth-code", "")
	if second.Connected {
		t.Fatal("the same OAuth callback connected twice")
	}
	if second.Reason != ReasonInvalidState {
		t.Errorf("replayed callback reason = %q, want %q", second.Reason, ReasonInvalidState)
	}
}

// TestAFailedAuthorizationStillBurnsItsState. A user who denies consent
// leaves a state behind; if it survives, the redirect can be replayed later
// with a code obtained some other way.
func TestAFailedAuthorizationStillBurnsItsState(t *testing.T) {
	provider := newStubProvider()
	svc, _ := newTestService(t, provider, "")
	ctx := context.Background()

	authorizationURL, err := svc.Start(ctx, testScope(), "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	state := stateFrom(t, authorizationURL)

	denied := svc.Complete(ctx, state, "", "access_denied")
	if denied.Connected || denied.Reason != ReasonAccessDenied {
		t.Fatalf("denied callback = %+v", denied)
	}
	// The state must now be spent, even though nothing was exchanged.
	retry := svc.Complete(ctx, state, "auth-code", "")
	if retry.Connected {
		t.Fatal("a state left over from a denied authorization was still redeemable")
	}
}

// TestUnknownStateIsRejectedWithoutTouchingTheProvider: a callback nobody
// asked for must not cause a token exchange.
func TestUnknownStateIsRejectedWithoutTouchingTheProvider(t *testing.T) {
	provider := newStubProvider()
	svc, _ := newTestService(t, provider, "")

	for _, state := range []string{"", "never-issued"} {
		result := svc.Complete(context.Background(), state, "auth-code", "")
		if result.Connected || result.Reason != ReasonInvalidState {
			t.Errorf("callback with state %q = %+v", state, result)
		}
	}
	if provider.exchangeForm != nil {
		t.Error("an unrecognized callback reached the token endpoint")
	}
}

// TestExchangeFailureLeavesNoConnection: a provider that rejects the code
// must not produce a half-built connection row.
func TestExchangeFailureLeavesNoConnection(t *testing.T) {
	provider := newStubProvider()
	provider.tokenStatus = http.StatusBadRequest
	provider.tokenBody = `{"error":"invalid_grant","error_description":"leak-me"}`
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", "")
	if result.Connected {
		t.Fatal("a rejected code produced a connection")
	}
	if result.Reason != ReasonExchangeFailed {
		t.Errorf("reason = %q, want %q", result.Reason, ReasonExchangeFailed)
	}
	if strings.Contains(result.Reason, "leak-me") {
		t.Error("the provider's error body reached the callback result")
	}
	if _, err := store.GetGoogleConnection(ctx, scope, "user-1"); err == nil {
		t.Error("a failed exchange left a connection behind")
	}
}

// TestAProfileFailureDoesNotCostTheUserTheirAuthorization. Reading the
// account address is a nicety; losing a consent grant over it is not.
func TestAProfileFailureDoesNotCostTheUserTheirAuthorization(t *testing.T) {
	provider := newStubProvider()
	provider.profileStatus = http.StatusForbidden
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("an unreadable profile lost the authorization: %q", result.Reason)
	}
	stored, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if stored.GoogleAccountEmail != "" {
		t.Errorf("account email = %q, want empty when the profile could not be read", stored.GoogleAccountEmail)
	}
}

// TestDisconnectRevokesBeforeErasing is the ordering that matters: erasing
// first would strand a live grant at Google with no refresh token left to
// revoke it with.
func TestDisconnectRevokesBeforeErasing(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}

	view, err := svc.Disconnect(ctx, scope, "user-1", "user-1", false)
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if provider.revokedToken != "refresh-1" {
		t.Errorf("revoked %q, want the refresh token — revoking only the access token "+
			"leaves the grant alive", provider.revokedToken)
	}
	if view.Status != domain.GoogleStatusDisconnected {
		t.Errorf("status = %q", view.Status)
	}
	stored, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if len(stored.TokenCiphertext) != 0 {
		t.Error("the disconnected connection still holds a sealed credential")
	}
	if stored.DisconnectedAt.IsZero() {
		t.Error("the tombstone records no disconnect time")
	}
}

// TestDisconnectFailsRatherThanStrandingAGrant: if Google cannot be reached,
// the local credential must survive so the user can try again.
func TestDisconnectFailsRatherThanStrandingAGrant(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}

	provider.revokeStatus = http.StatusInternalServerError
	if _, err := svc.Disconnect(ctx, scope, "user-1", "user-1", false); err == nil {
		t.Fatal("a failed revoke reported a successful disconnect")
	}
	stored, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if len(stored.TokenCiphertext) == 0 {
		t.Fatal("the credential was erased despite the revoke failing; " +
			"the grant at Google can now never be withdrawn")
	}
}

// TestPurgeRemovesTheRecordAndStillAudits.
func TestPurgeRemovesTheRecordAndStillAudits(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}
	connection, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}

	if _, err := svc.Disconnect(ctx, scope, "user-1", "user-1", true); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := store.GetGoogleConnection(ctx, scope, "user-1"); err == nil {
		t.Error("the purged connection is still readable")
	}

	events, err := store.OutboxEventsFor(ctx, scope, connection.ID)
	if err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	var sawAuthorized, sawPurged bool
	for _, event := range events {
		switch event.Envelope.Type {
		case domain.EventGoogleConnectionAuthorized:
			sawAuthorized = true
		case domain.EventGoogleConnectionPurged:
			sawPurged = true
		}
	}
	if !sawAuthorized {
		t.Error("connecting recorded no audit event")
	}
	if !sawPurged {
		t.Error("purging recorded no audit event; a purge must not be able to erase " +
			"the fact that it happened")
	}
}

// TestRefreshCarriesTheRefreshTokenForward is the bug this seam exists to
// prevent: Google omits refresh_token from most refresh responses, so a naive
// implementation stores an empty one and the connection dies an hour later.
func TestRefreshCarriesTheRefreshTokenForward(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}

	// Age the stored token past its expiry.
	connection, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.AccessTokenExpiresAt = time.Now().UTC().Add(-time.Hour)
	if _, err := store.SaveGoogleConnection(ctx, scope, connection); err != nil {
		t.Fatalf("age the token: %v", err)
	}

	// Google's refresh response, without a refresh token — the normal shape.
	provider.tokenBody = `{"access_token":"access-2","token_type":"Bearer","expires_in":3600}`
	token, err := svc.AccessToken(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if token != "access-2" {
		t.Errorf("token = %q, want the refreshed one", token)
	}

	// Age it again. If the refresh token was not carried forward, this second
	// refresh has nothing to refresh with.
	connection, err = store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.AccessTokenExpiresAt = time.Now().UTC().Add(-time.Hour)
	if _, err := store.SaveGoogleConnection(ctx, scope, connection); err != nil {
		t.Fatalf("age the token again: %v", err)
	}
	provider.tokenBody = `{"access_token":"access-3","token_type":"Bearer","expires_in":3600}`
	if _, err := svc.AccessToken(ctx, scope, "user-1"); err != nil {
		t.Fatalf("second refresh: %v — the refresh token was not carried forward", err)
	}
}

// TestARejectedRefreshAsksForReauthorization rather than failing silently
// forever: the user needs to be told to reconnect.
func TestARejectedRefreshAsksForReauthorization(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}
	connection, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	connection.AccessTokenExpiresAt = time.Now().UTC().Add(-time.Hour)
	if _, err := store.SaveGoogleConnection(ctx, scope, connection); err != nil {
		t.Fatalf("age the token: %v", err)
	}

	provider.tokenStatus = http.StatusBadRequest
	provider.tokenBody = `{"error":"invalid_grant"}`
	if _, err := svc.AccessToken(ctx, scope, "user-1"); err == nil {
		t.Fatal("a rejected refresh returned a token")
	}

	stored, err := store.GetGoogleConnection(ctx, scope, "user-1")
	if err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if stored.Status != domain.GoogleStatusReauthorizationRequired {
		t.Errorf("status = %q, want %q", stored.Status, domain.GoogleStatusReauthorizationRequired)
	}
	if len(stored.TokenCiphertext) != 0 {
		t.Error("a credential Google no longer honours is still stored")
	}
	if stored.LastError == "" {
		t.Error("no reason recorded for the reauthorization request")
	}
}

// TestStartRejectsAnOffSiteReturnPath. The return path is stored and later
// used to build a redirect, so it is validated on the way in.
func TestStartRejectsAnOffSiteReturnPath(t *testing.T) {
	svc, _ := newTestService(t, newStubProvider(), "")
	for _, path := range []string{"https://evil.example", "//evil.example", "/\\evil.example"} {
		if _, err := svc.Start(context.Background(), testScope(), "user-1", path); err == nil {
			t.Errorf("Start accepted the off-site return path %q", path)
		}
	}
}

// TestStartRequiresAnActor: a connection with no owner could never be
// disconnected by the person who made it.
func TestStartRequiresAnActor(t *testing.T) {
	svc, _ := newTestService(t, newStubProvider(), "")
	if _, err := svc.Start(context.Background(), testScope(), "  ", "/"); err == nil {
		t.Error("Start accepted an empty actor")
	}
}

// TestUnconfiguredServiceRefusesEverySurface. A CRM without Google
// credentials must say so rather than half-working.
func TestUnconfiguredServiceRefusesEverySurface(t *testing.T) {
	svc := New(domain.NewMemStore(), nil, "")
	ctx := context.Background()

	if svc.Configured() {
		t.Fatal("a service with no provider client reports itself configured")
	}
	if _, err := svc.Start(ctx, testScope(), "user-1", ""); err != ErrNotConfigured {
		t.Errorf("Start: %v, want ErrNotConfigured", err)
	}
	if result := svc.Complete(ctx, "state", "code", ""); result.Reason != ReasonNotConfigured {
		t.Errorf("Complete: %+v", result)
	}
	if _, err := svc.AccessToken(ctx, testScope(), "user-1"); err != ErrNotConfigured {
		t.Errorf("AccessToken: %v, want ErrNotConfigured", err)
	}
	// Status still works: "not connected" is the honest answer, and the UI
	// needs to render the page.
	view, err := svc.Status(ctx, testScope(), "user-1")
	if err != nil {
		t.Errorf("Status: %v", err)
	}
	if view.Status != "" {
		t.Errorf("status = %q, want empty", view.Status)
	}
}

// TestStatusReportsNoConnectionWithoutAnError: having never connected is the
// normal state, not a failure the UI has to special-case.
func TestStatusReportsNoConnectionWithoutAnError(t *testing.T) {
	svc, _ := newTestService(t, newStubProvider(), "")
	view, err := svc.Status(context.Background(), testScope(), "user-1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if view.Status != "" || view.OwnerID != "user-1" {
		t.Errorf("view = %+v, want an empty status for the calling owner", view)
	}
}

// TestOneUsersCallbackCannotConnectAnotherUsersMailbox. The owner comes from
// the state record written at Start, so a callback cannot be redirected onto
// a colleague's account.
func TestOneUsersCallbackCannotConnectAnotherUsersMailbox(t *testing.T) {
	provider := newStubProvider()
	svc, store := newTestService(t, provider, "")
	ctx := context.Background()
	scope := testScope()

	authorizationURL, err := svc.Start(ctx, scope, "user-1", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result := svc.Complete(ctx, stateFrom(t, authorizationURL), "auth-code", ""); !result.Connected {
		t.Fatalf("connect: %q", result.Reason)
	}

	if _, err := store.GetGoogleConnection(ctx, scope, "user-2"); err == nil {
		t.Error("completing user-1's authorization created a connection for user-2")
	}
	connections, err := store.ListGoogleConnections(ctx, scope)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(connections) != 1 || connections[0].OwnerID != "user-1" {
		t.Errorf("connections = %+v, want exactly user-1's", connections)
	}
}
