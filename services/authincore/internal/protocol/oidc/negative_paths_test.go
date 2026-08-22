package oidc_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/google/uuid"
)

// OIDC negative paths (task.md §1.2).
//
// oidc_test.go already covers the malformed and unknown cases — a bad client,
// an unsupported response type, an invalid code, a tampered or expired token.
// The ones here are different in kind: each is a request that is entirely
// well-formed, made by a caller holding real material, and must still be
// refused. They are the paths an attacker actually has access to.

// postToken submits a token request and returns the recorder.
func postToken(t *testing.T, p *oidc.Provider, orgID uuid.UUID, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Org-ID", orgID.String())
	p.TokenHandler(rec, req)
	return rec
}

// tokenErrorCode reads the OAuth error code from a failed token response.
func tokenErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode token error: %v", err)
	}
	code, _ := body["error"].(string)
	return code
}

// newCodeProvider builds a provider plus a freshly issued authorization code.
func newCodeProvider(t *testing.T, challenge, method string) (*oidc.Provider, uuid.UUID, string) {
	t.Helper()
	orgID := uuid.New()
	p := newProvider(t, &fakeClientStore{client: publicClient()}, newFakeTokenStore())
	code, err := p.IssueAuthCode(context.Background(), uuid.New(), uuid.New(), orgID,
		"test-client", "https://app.example.com/callback", "st", "openid email",
		challenge, method)
	if err != nil {
		t.Fatalf("IssueAuthCode: %v", err)
	}
	return p, orgID, code
}

// TestAuthorizationCodeCannotBeReplayed is the one that matters most on this
// list. An authorization code travels through the user's browser, so it lands
// in history, in Referer headers, and in any proxy log on the way. Single use
// is what makes that survivable: whoever gets there second gets nothing.
func TestAuthorizationCodeCannotBeReplayed(t *testing.T) {
	p, orgID, code := newCodeProvider(t, "", "")

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {"https://app.example.com/callback"},
		"client_id":    {"test-client"},
	}

	first := postToken(t, p, orgID, form)
	if first.Code != http.StatusOK {
		t.Fatalf("the first exchange failed (%d): %s", first.Code, first.Body.String())
	}

	second := postToken(t, p, orgID, form)
	if second.Code == http.StatusOK {
		t.Fatal("the same authorization code was exchanged twice; a code leaked through " +
			"browser history or a proxy log would be usable by whoever found it")
	}
	if got := tokenErrorCode(t, second); got != "invalid_grant" {
		t.Errorf("replayed code error = %q, want invalid_grant", got)
	}
}

// TestAuthorizationCodeRequiresTheRedirectURIItWasIssuedFor covers the
// substitution attack: a code issued for one registered callback must not be
// redeemable against another.
func TestAuthorizationCodeRequiresTheRedirectURIItWasIssuedFor(t *testing.T) {
	p, orgID, code := newCodeProvider(t, "", "")

	rec := postToken(t, p, orgID, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {"https://attacker.example.com/callback"},
		"client_id":    {"test-client"},
	})
	if rec.Code == http.StatusOK {
		t.Fatal("a code issued for one redirect URI was redeemed against another")
	}
	if got := tokenErrorCode(t, rec); got != "invalid_grant" && got != "invalid_request" {
		t.Errorf("redirect-URI mismatch error = %q, want invalid_grant or invalid_request", got)
	}
}

// TestPKCEVerifierMustMatchTheChallenge is what makes a public client safe. A
// browser application cannot hold a secret, so PKCE is the only thing tying
// the code exchange to the client that began the flow. A wrong verifier — or
// a missing one, against a code issued with a challenge — must fail.
func TestPKCEVerifierMustMatchTheChallenge(t *testing.T) {
	// The S256 challenge is derived here the way a client derives it, rather
	// than by calling into the package under test — a helper shared with the
	// verifier would agree with itself even if both were wrong.
	const verifier = "the-real-verifier-value-used-at-authorize-endpoint-01"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	t.Run("wrong verifier", func(t *testing.T) {
		p, orgID, code := newCodeProvider(t, challenge, "S256")
		rec := postToken(t, p, orgID, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {"https://app.example.com/callback"},
			"client_id":     {"test-client"},
			"code_verifier": {"a-completely-different-verifier-value-here-02"},
		})
		if rec.Code == http.StatusOK {
			t.Fatal("a code exchange succeeded with the wrong PKCE verifier")
		}
	})

	t.Run("missing verifier", func(t *testing.T) {
		p, orgID, code := newCodeProvider(t, challenge, "S256")
		rec := postToken(t, p, orgID, url.Values{
			"grant_type":   {"authorization_code"},
			"code":         {code},
			"redirect_uri": {"https://app.example.com/callback"},
			"client_id":    {"test-client"},
		})
		if rec.Code == http.StatusOK {
			t.Fatal("a code issued with a PKCE challenge was exchanged with no verifier at all")
		}
	})

	t.Run("correct verifier still works", func(t *testing.T) {
		p, orgID, code := newCodeProvider(t, challenge, "S256")
		rec := postToken(t, p, orgID, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {"https://app.example.com/callback"},
			"client_id":     {"test-client"},
			"code_verifier": {verifier},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("the correct verifier was rejected (%d): %s — the two tests above "+
				"would then pass for the wrong reason", rec.Code, rec.Body.String())
		}
	})
}

// TestRefreshTokenIsSingleUse covers rotation (RFC 6749 §10.4). A refresh
// token is long-lived, so replay detection is what limits the damage when one
// leaks: the second use of the same token must fail.
func TestRefreshTokenIsSingleUse(t *testing.T) {
	p, orgID, code := newCodeProvider(t, "", "")

	exchanged := postToken(t, p, orgID, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {"https://app.example.com/callback"},
		"client_id":    {"test-client"},
	})
	if exchanged.Code != http.StatusOK {
		t.Fatalf("code exchange failed (%d): %s", exchanged.Code, exchanged.Body.String())
	}
	var issued map[string]any
	if err := json.NewDecoder(exchanged.Body).Decode(&issued); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refresh, _ := issued["refresh_token"].(string)
	if refresh == "" {
		t.Skip("this flow issued no refresh token; nothing to replay")
	}

	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {"test-client"},
	}

	first := postToken(t, p, orgID, refreshForm)
	if first.Code != http.StatusOK {
		t.Fatalf("the first refresh failed (%d): %s", first.Code, first.Body.String())
	}

	second := postToken(t, p, orgID, refreshForm)
	if second.Code == http.StatusOK {
		t.Fatal("the same refresh token was redeemed twice; a leaked refresh token would " +
			"grant an attacker a session indefinitely alongside the real user")
	}
	if got := tokenErrorCode(t, second); got != "invalid_grant" {
		t.Errorf("replayed refresh token error = %q, want invalid_grant", got)
	}
}

// TestRefreshTokenCannotBeUsedByAnotherClient stops a token issued to the
// browser application from being redeemed by anything else that can reach the
// token endpoint.
func TestRefreshTokenCannotBeUsedByAnotherClient(t *testing.T) {
	p, orgID, code := newCodeProvider(t, "", "")

	exchanged := postToken(t, p, orgID, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {"https://app.example.com/callback"},
		"client_id":    {"test-client"},
	})
	if exchanged.Code != http.StatusOK {
		t.Fatalf("code exchange failed: %s", exchanged.Body.String())
	}
	var issued map[string]any
	if err := json.NewDecoder(exchanged.Body).Decode(&issued); err != nil {
		t.Fatalf("decode: %v", err)
	}
	refresh, _ := issued["refresh_token"].(string)
	if refresh == "" {
		t.Skip("this flow issued no refresh token")
	}

	rec := postToken(t, p, orgID, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {"some-other-client"},
	})
	if rec.Code == http.StatusOK {
		t.Error("a refresh token issued to one client was redeemed by another")
	}
}
