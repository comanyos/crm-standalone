package googleoauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// These exercise the provider HTTP surface against a local stub. The point is
// not to re-test net/http: it is to pin the three behaviours the rest of the
// system depends on and cannot see — that a token exchange sends PKCE, that a
// provider error body never escapes into an error string, and that revoking
// an already-dead token reports success.

// stubClient points a Client's endpoints at a test server.
func stubClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := New(testConfig(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.tokenEndpoint = server.URL + "/token"
	c.revokeEndpoint = server.URL + "/revoke"
	c.profileEndpoint = server.URL + "/profile"
	return c
}

func TestExchangeSendsPKCEVerifierAndReturnsTokens(t *testing.T) {
	var gotForm url.Values
	c := stubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","token_type":"Bearer",
			"scope":"https://www.googleapis.com/auth/gmail.readonly","expires_in":3600}`))
	}))

	tokens, err := c.Exchange(context.Background(), "auth-code", "pkce-verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if gotForm.Get("code_verifier") != "pkce-verifier" {
		t.Error("exchange did not send the PKCE verifier; the challenge sent at Begin would prove nothing")
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("code") != "auth-code" {
		t.Errorf("unexpected exchange form: %v", gotForm)
	}
	if tokens.AccessToken != "at" || tokens.RefreshToken != "rt" {
		t.Errorf("unexpected token set: %+v", tokens)
	}
	if !tokens.Expiry.After(time.Now().UTC().Add(59 * time.Minute)) {
		t.Errorf("expiry = %v, want roughly an hour out", tokens.Expiry)
	}
}

// TestTokenErrorsDoNotLeakProviderBodies is the one that matters for logs.
// Google's token error bodies can carry authorization material, and an error
// value in this codebase ends up in a log line.
func TestTokenErrorsDoNotLeakProviderBodies(t *testing.T) {
	const secretish = "the-refresh-token-that-must-not-be-logged"
	c := stubClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"` + secretish + `"}`))
	}))

	_, err := c.Exchange(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("expected an error from a 400 token response")
	}
	if strings.Contains(err.Error(), secretish) {
		t.Errorf("token error carries the provider response body: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("token error should still name the status code, got %v", err)
	}
}

func TestRefreshKeepsWorkingWhenGoogleOmitsARefreshToken(t *testing.T) {
	c := stubClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Google routinely omits refresh_token when refreshing.
		_, _ = w.Write([]byte(`{"access_token":"at2","token_type":"Bearer","expires_in":3600}`))
	}))

	tokens, err := c.Refresh(context.Background(), "rt")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if tokens.AccessToken != "at2" {
		t.Errorf("access token = %q, want at2", tokens.AccessToken)
	}
	if tokens.RefreshToken != "" {
		t.Errorf("refresh token = %q; the caller is responsible for retaining the prior one", tokens.RefreshToken)
	}
}

// TestRevokeTreatsAnAlreadyDeadTokenAsSuccess: a user who revoked access from
// Google's own account page must still be able to press Disconnect here.
func TestRevokeTreatsAnAlreadyDeadTokenAsSuccess(t *testing.T) {
	status := http.StatusOK
	var sentToken string
	c := stubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sentToken = r.PostForm.Get("token")
		w.WriteHeader(status)
	}))

	if err := c.Revoke(context.Background(), "rt"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if sentToken != "rt" {
		t.Errorf("revoke sent token %q, want rt", sentToken)
	}

	status = http.StatusBadRequest
	if err := c.Revoke(context.Background(), "rt"); err != nil {
		t.Errorf("revoke of an already-invalid token: %v, want nil", err)
	}

	status = http.StatusInternalServerError
	if err := c.Revoke(context.Background(), "rt"); err == nil {
		t.Error("a provider outage during revoke must not be reported as a successful disconnect")
	}
}

func TestAccountEmailReadsTheConsentedAccount(t *testing.T) {
	var gotAuth string
	c := stubClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"emailAddress":"person@example.com","messagesTotal":12}`))
	}))

	email, err := c.AccountEmail(context.Background(), "at")
	if err != nil {
		t.Fatalf("account email: %v", err)
	}
	if email != "person@example.com" {
		t.Errorf("email = %q, want person@example.com", email)
	}
	if gotAuth != "Bearer at" {
		t.Errorf("Authorization = %q, want a bearer access token", gotAuth)
	}
}

// TestAssociatedDataDistinguishesTenantsAndConnections guards the binding
// every stored ciphertext depends on.
func TestAssociatedDataDistinguishesTenantsAndConnections(t *testing.T) {
	base := AssociatedData("tenant-1", "conn-1")
	for _, other := range [][]byte{
		AssociatedData("tenant-2", "conn-1"),
		AssociatedData("tenant-1", "conn-2"),
		AssociatedData("tenant-1:conn", "1"), // no separator ambiguity
		StateAssociatedData("tenant-1", "conn-1"),
	} {
		if string(base) == string(other) {
			t.Errorf("AssociatedData collides: %q", base)
		}
	}

	c, err := New(testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Encrypt(TokenSet{AccessToken: "at"}, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decrypt(sealed, AssociatedData("tenant-2", "conn-1")); err == nil {
		t.Error("a ciphertext copied into another tenant opened successfully")
	}
	if _, err := c.Decrypt(sealed, AssociatedData("tenant-1", "conn-2")); err == nil {
		t.Error("a ciphertext copied onto another connection opened successfully")
	}
}

// TestStateAndTokenBindingsAreDistinct: a sealed PKCE verifier must not open
// as a token set under a binding built from the same two strings.
func TestStateAndTokenBindingsAreDistinct(t *testing.T) {
	c, err := New(testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Encrypt(TokenSet{AccessToken: "verifier-stand-in"},
		StateAssociatedData("tenant-1", "abc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decrypt(sealed, AssociatedData("tenant-1", "abc")); err == nil {
		t.Error("a state binding and a connection binding produce the same associated data")
	}
}

// TestScopesIsACopy: a caller must not be able to widen what this integration
// asks for by mutating the returned slice.
func TestScopesIsACopy(t *testing.T) {
	got := Scopes()
	if len(got) == 0 {
		t.Fatal("Scopes returned nothing")
	}
	got[0] = "https://www.googleapis.com/auth/drive"
	if scopes[0] == "https://www.googleapis.com/auth/drive" {
		t.Error("Scopes returns the package's own slice; a caller can widen the consent request")
	}
}
