package googleoauth

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		ClientID:     "client.apps.googleusercontent.com",
		ClientSecret: "not-a-real-secret",
		RedirectURL:  "http://localhost:19431/integrations/google/callback",
		TokenKey:     base64.RawStdEncoding.EncodeToString([]byte("01234567890123456789012345678901")),
	}
}

func TestBeginUsesPKCEAndApprovedScopes(t *testing.T) {
	c, err := New(testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	authorizeURL, pending, err := c.Begin(time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if pending.State == "" || pending.CodeVerifier == "" || !pending.ExpiresAt.Equal(time.Date(2026, 8, 23, 0, 10, 0, 0, time.UTC)) {
		t.Fatalf("unexpected pending authorization: %+v", pending)
	}
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("state") != pending.State || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("missing PKCE state or challenge: %s", authorizeURL)
	}
	for _, scope := range scopes {
		if !strings.Contains(q.Get("scope"), scope) {
			t.Fatalf("missing approved scope %q in %q", scope, q.Get("scope"))
		}
	}
}

func TestEncryptBindsCiphertextToConnection(t *testing.T) {
	c, err := New(testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := TokenSet{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().UTC().Round(0)}
	sealed, err := c.Encrypt(want, []byte("tenant-1:connection-1"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(sealed, []byte("tenant-1:connection-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("unexpected decrypted tokens: %+v", got)
	}
	if _, err := c.Decrypt(sealed, []byte("tenant-2:connection-1")); err == nil {
		t.Fatal("expected cross-tenant associated data to fail")
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	cfg := testConfig()
	cfg.TokenKey = "short"
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("expected malformed token key to be rejected")
	}
	cfg = testConfig()
	cfg.RedirectURL = "/callback"
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("expected relative redirect URL to be rejected")
	}
}
