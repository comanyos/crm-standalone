package server

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
)

// The OAuth callback route is the only unauthenticated, browser-facing
// surface this service exposes. What these tests hold it to is narrow and
// specific: it must not be turnable into an open redirect, it must not
// reflect anything from the request into the response, and it must not
// connect anything on the strength of a state nobody issued.

// googleTestConfig is a deployment with Google configured. The key is 32
// bytes of nothing; no real credential appears in this repository.
func googleTestConfig(webBaseURL string) config.Config {
	return config.Config{
		GoogleOAuthClientID:     "client.apps.googleusercontent.com",
		GoogleOAuthClientSecret: "test-secret",
		GoogleOAuthRedirectURL:  "http://localhost:19431/integrations/google/callback",
		GoogleOAuthTokenKey:     base64.RawStdEncoding.EncodeToString([]byte("server-test-key-012345678901234x")),
		WebAppBaseURL:           webBaseURL,
	}
}

func googleTestServer(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	srv, err := New(cfg, domain.NewMemStore(), auth.Disabled())
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(srv.Close)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)
	return httpSrv
}

// noRedirect keeps the client from following the callback's 303, so the test
// can inspect the Location header rather than whatever is behind it.
func noRedirectClient(srv *httptest.Server) *http.Client {
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

// TestCallbackRejectsAStateItNeverIssued. This is the request an attacker can
// actually make: a bare GET to a public URL.
func TestCallbackRejectsAStateItNeverIssued(t *testing.T) {
	srv := googleTestServer(t, googleTestConfig(""))

	resp, err := noRedirectClient(srv).Get(srv.URL + googleCallbackPath +
		"?state=forged&code=stolen-or-invented")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "invalid_state") {
		t.Errorf("body does not report the bounded reason: %s", body)
	}
}

// TestCallbackDoesNotReflectRequestInput is the injection check. Every
// parameter here is attacker-controlled, and none of it may appear in the
// response — not escaped, not at all.
func TestCallbackDoesNotReflectRequestInput(t *testing.T) {
	srv := googleTestServer(t, googleTestConfig(""))

	const marker = "cAnaRy0<script>alert(1)</script>"
	query := url.Values{
		"state": {marker},
		"code":  {marker},
		"error": {marker},
	}
	resp, err := noRedirectClient(srv).Get(srv.URL + googleCallbackPath + "?" + query.Encode())
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), "cAnaRy0") {
		t.Errorf("the callback reflected a request parameter into its response: %s", body)
	}
	if strings.Contains(string(body), "<script>") {
		t.Errorf("the callback rendered markup from the request: %s", body)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("the callback result page carries no Content-Security-Policy")
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q; an authorization outcome must not be cached", got)
	}
}

// TestCallbackRedirectsOnlyToTheConfiguredOrigin. The return path is stored,
// but the origin is configuration — so even a stored path cannot send a user
// off-site.
func TestCallbackRedirectsOnlyToTheConfiguredOrigin(t *testing.T) {
	srv := googleTestServer(t, googleTestConfig("https://crm.example.test"))

	resp, err := noRedirectClient(srv).Get(srv.URL + googleCallbackPath + "?state=forged")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	target, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse Location %q: %v", location, err)
	}
	if target.Scheme != "https" || target.Host != "crm.example.test" {
		t.Errorf("Location = %q, want the configured web application origin", location)
	}
	if target.Query().Get("google") != "error" {
		t.Errorf("Location does not report the outcome: %q", location)
	}
	if target.Query().Get("google_reason") != "invalid_state" {
		t.Errorf("Location reason = %q", target.Query().Get("google_reason"))
	}
}

// TestCallbackIsNotAnOpenRedirect: the query cannot steer the destination,
// whatever it contains.
func TestCallbackIsNotAnOpenRedirect(t *testing.T) {
	srv := googleTestServer(t, googleTestConfig("https://crm.example.test"))
	client := noRedirectClient(srv)

	for _, hostile := range []string{
		"?state=x&return_path=https://evil.example",
		"?state=x&return_to=//evil.example",
		"?state=https://evil.example",
		"?state=x&redirect_uri=https://evil.example",
	} {
		resp, err := client.Get(srv.URL + googleCallbackPath + hostile)
		if err != nil {
			t.Fatalf("callback %s: %v", hostile, err)
		}
		location := resp.Header.Get("Location")
		resp.Body.Close()

		if location == "" {
			continue
		}
		target, err := url.Parse(location)
		if err != nil {
			t.Fatalf("parse Location %q: %v", location, err)
		}
		if target.Host != "crm.example.test" {
			t.Errorf("%s steered the callback to %q", hostile, location)
		}
	}
}

// TestCallbackIsUnauthenticatedButNotUnprotected states the route's contract
// plainly: it answers without a token — it has to, Google sends the browser
// here — and answering is not the same as connecting anything.
func TestCallbackIsUnauthenticatedButNotUnprotected(t *testing.T) {
	srv := googleTestServer(t, googleTestConfig(""))

	resp, err := noRedirectClient(srv).Get(srv.URL + googleCallbackPath)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("the callback demands a bearer token; Google's redirect can never carry one")
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a callback with nothing in it", resp.StatusCode)
	}
}

// TestCallbackPathMatchesTheConfiguredRedirect. Google compares the whole
// redirect URI, so a mismatch between the route and GOOGLE_OAUTH_REDIRECT_URL
// fails at the provider with an error the user cannot act on.
func TestCallbackPathMatchesTheConfiguredRedirect(t *testing.T) {
	cfg := googleTestConfig("")
	redirect, err := url.Parse(cfg.GoogleOAuthRedirectURL)
	if err != nil {
		t.Fatalf("parse configured redirect: %v", err)
	}
	if redirect.Path != googleCallbackPath {
		t.Errorf("the served callback path is %q but GOOGLE_OAUTH_REDIRECT_URL points at %q",
			googleCallbackPath, redirect.Path)
	}
}

// TestServerRefusesPartialGoogleConfiguration. A client id with no sealing
// key would give users a Connect button that cannot store what it receives.
func TestServerRefusesPartialGoogleConfiguration(t *testing.T) {
	cfg := googleTestConfig("")
	cfg.GoogleOAuthTokenKey = ""

	if _, err := New(cfg, domain.NewMemStore(), auth.Disabled()); err == nil {
		t.Fatal("a server with half a Google configuration started")
	}
}

// TestServerStartsWithoutGoogleConfigured: the integration is optional, and
// the rest of the CRM must not depend on it.
func TestServerStartsWithoutGoogleConfigured(t *testing.T) {
	srv, err := New(config.Config{}, domain.NewMemStore(), auth.Disabled())
	if err != nil {
		t.Fatalf("build server without Google: %v", err)
	}
	defer srv.Close()

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	resp, err := noRedirectClient(httpSrv).Get(httpSrv.URL + googleCallbackPath + "?state=x")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not_configured") {
		t.Errorf("an unconfigured deployment did not say so: %s", body)
	}
}
