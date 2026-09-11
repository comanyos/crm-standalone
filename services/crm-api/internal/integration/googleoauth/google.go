// Package googleoauth contains the provider-specific cryptography and HTTP
// pieces for a Google CRM connection. It deliberately does not persist a
// connection: the caller must keep PendingAuthorization server-side, tied to
// the authenticated tenant and actor, before redirecting the browser.
package googleoauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint         = "https://oauth2.googleapis.com/token" // #nosec G101 -- public provider endpoint, not a token
	revokeEndpoint        = "https://oauth2.googleapis.com/revoke"
	// profileEndpoint is Gmail's users.getProfile. It is what identifies the
	// account the user actually consented with, and it works under
	// gmail.readonly — so reading it costs no extra scope. Asking for
	// openid/email purely to learn the address would widen consent for no
	// functional gain.
	profileEndpoint = "https://gmail.googleapis.com/gmail/v1/users/me/profile"
	pendingLifetime = 10 * time.Minute
)

var scopes = []string{
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/gmail.send",
	"https://www.googleapis.com/auth/calendar.events",
}

// Config is injected from runtime configuration. Secrets must be fetched from
// the process environment or deployment secret store, never from a browser
// request, source file, log, or database migration.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// TokenKey is a base64-encoded, 32-byte random value used for AES-256-GCM.
	TokenKey string
}

// Client builds authorization URLs, exchanges OAuth codes, and encrypts token
// payloads for durable storage.
type Client struct {
	clientID     string
	clientSecret string
	redirectURL  string
	aead         cipher.AEAD
	httpClient   *http.Client

	// The provider endpoints, copied from the constants above at
	// construction. They are fields rather than direct references so the
	// package's own tests can point them at a local server; nothing outside
	// this package can change them.
	authorizationEndpoint string
	tokenEndpoint         string
	revokeEndpoint        string
	profileEndpoint       string
}

// PendingAuthorization contains the values that must be stored on the server
// with a tenant ID, actor ID, and expiry before redirecting a browser. Neither
// state nor code verifier may be returned to the frontend application.
type PendingAuthorization struct {
	State        string
	CodeVerifier string
	ExpiresAt    time.Time
}

// TokenSet is the minimal provider credential response. It must be encrypted
// before persistence and must never be returned through a browser-facing API.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	Expiry       time.Time
}

// New validates provider configuration before any OAuth request is possible.
func New(cfg Config, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" ||
		strings.TrimSpace(cfg.RedirectURL) == "" || strings.TrimSpace(cfg.TokenKey) == "" {
		return nil, errors.New("googleoauth: client ID, client secret, redirect URL, and token key are required")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || (redirect.Scheme != "https" && redirect.Scheme != "http") || redirect.Host == "" {
		return nil, errors.New("googleoauth: redirect URL must be an absolute http(s) URL")
	}
	key, err := decodeKey(cfg.TokenKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("googleoauth: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("googleoauth: create GCM: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		clientID:              cfg.ClientID,
		clientSecret:          cfg.ClientSecret,
		redirectURL:           cfg.RedirectURL,
		aead:                  aead,
		httpClient:            httpClient,
		authorizationEndpoint: authorizationEndpoint,
		tokenEndpoint:         tokenEndpoint,
		revokeEndpoint:        revokeEndpoint,
		profileEndpoint:       profileEndpoint,
	}, nil
}

// Scopes returns the consent this integration asks for, so the connection UI
// can tell a user what they are about to grant without restating the list and
// drifting from what is actually requested.
func Scopes() []string { return append([]string(nil), scopes...) }

// AssociatedData is the AEAD binding for one tenant's connection.
//
// It binds to (tenant, owner) rather than to the connection's surrogate id,
// because (tenant, owner) *is* the connection's identity — the schema has a
// unique index on exactly that pair. It is also the only identity known
// before the first insert, so sealing does not have to wait for a generated
// id or, worse, re-seal after one.
//
// This exists as a function so callers cannot derive the binding slightly
// differently from one another. Encrypt and Decrypt authenticate the value
// without storing it, so a ciphertext moved to another tenant or another
// owner fails to open rather than decrypting into a usable credential — which
// only holds while every call site builds it the same way.
func AssociatedData(tenantID, ownerID string) []byte {
	return []byte("google-connection:" + tenantID + ":" + ownerID)
}

// StateAssociatedData is the binding for a pending authorization's sealed
// PKCE verifier. It is a distinct prefix from AssociatedData so a verifier
// ciphertext can never be opened as a token set, or the reverse.
func StateAssociatedData(tenantID, state string) []byte {
	return []byte("google-oauth-state:" + tenantID + ":" + state)
}

// Begin makes a one-time PKCE challenge and a CSRF state. The caller owns the
// resulting pending record and must reject expired, missing, or already-used
// state on callback.
func (c *Client) Begin(now time.Time) (string, PendingAuthorization, error) {
	state, err := randomURLValue(32)
	if err != nil {
		return "", PendingAuthorization{}, fmt.Errorf("googleoauth: generate state: %w", err)
	}
	verifier, err := randomURLValue(64)
	if err != nil {
		return "", PendingAuthorization{}, fmt.Errorf("googleoauth: generate PKCE verifier: %w", err)
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {c.clientID},
		"redirect_uri":          {c.redirectURL},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
	}
	return c.authorizationEndpoint + "?" + query.Encode(), PendingAuthorization{
		State: state, CodeVerifier: verifier, ExpiresAt: now.UTC().Add(pendingLifetime),
	}, nil
}

// Exchange exchanges a callback authorization code after the caller has
// atomically consumed and validated the matching PendingAuthorization record.
func (c *Client) Exchange(ctx context.Context, code, codeVerifier string) (TokenSet, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(codeVerifier) == "" {
		return TokenSet{}, errors.New("googleoauth: authorization code and PKCE verifier are required")
	}
	form := url.Values{
		"code":          {code},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"redirect_uri":  {c.redirectURL},
		"grant_type":    {"authorization_code"},
		"code_verifier": {codeVerifier},
	}
	return c.tokenRequest(ctx, form)
}

// Refresh obtains a new access token. Google may omit refresh_token from this
// response; callers must retain the prior encrypted refresh token in that case.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return TokenSet{}, errors.New("googleoauth: refresh token is required")
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	return c.tokenRequest(ctx, form)
}

// Revoke withdraws a grant at Google.
//
// The refresh token is the right thing to send: revoking it invalidates the
// whole grant, including every access token issued from it, so a disconnect
// actually ends access rather than waiting out the current hour.
//
// A token Google has already forgotten comes back as 400. That is reported as
// success, because it describes the state the caller wanted: the credential
// does not work any more. Treating it as failure would leave a user unable to
// disconnect a connection that was revoked from Google's own account page.
func (c *Client) Revoke(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("googleoauth: token is required")
	}
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revokeEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("googleoauth: build revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("googleoauth: revoke request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body close is best-effort after a bounded read
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		return nil
	default:
		// The body is not included: revoke error bodies echo the token.
		return fmt.Errorf("googleoauth: revoke endpoint returned HTTP %d", resp.StatusCode)
	}
}

// AccountEmail reads back which Google account an access token belongs to.
//
// The address is not taken from the user, and not from an id_token this
// service would then have to verify: it is read from the provider under the
// grant just issued. Someone who authorized the wrong account can then see
// that they did, instead of finding out when the wrong mailbox syncs.
func (c *Client) AccountEmail(ctx context.Context, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("googleoauth: access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.profileEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("googleoauth: build profile request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("googleoauth: profile request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body close is best-effort after a bounded read
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("googleoauth: profile endpoint returned HTTP %d", resp.StatusCode)
	}
	var wire struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire); err != nil {
		return "", fmt.Errorf("googleoauth: decode profile response: %w", err)
	}
	return wire.EmailAddress, nil
}

// Encrypt serializes a provider token set and seals it with AES-256-GCM. The
// associated data must be a stable tenant+connection binding so ciphertexts
// cannot be replayed across tenants or connections.
func (c *Client) Encrypt(tokens TokenSet, associatedData []byte) ([]byte, error) {
	// #nosec G117 -- the token structure is serialized only to seal it with AEAD immediately below.
	plain, err := json.Marshal(tokens)
	if err != nil {
		return nil, fmt.Errorf("googleoauth: encode tokens: %w", err)
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("googleoauth: generate encryption nonce: %w", err)
	}
	return append(nonce, c.aead.Seal(nil, nonce, plain, associatedData)...), nil
}

// Decrypt opens a token ciphertext produced by Encrypt using the same tenant
// and connection associated data.
func (c *Client) Decrypt(ciphertext, associatedData []byte) (TokenSet, error) {
	if len(ciphertext) < c.aead.NonceSize() {
		return TokenSet{}, errors.New("googleoauth: malformed token ciphertext")
	}
	nonce, sealed := ciphertext[:c.aead.NonceSize()], ciphertext[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, sealed, associatedData)
	if err != nil {
		return TokenSet{}, errors.New("googleoauth: token ciphertext authentication failed")
	}
	var tokens TokenSet
	if err := json.Unmarshal(plain, &tokens); err != nil {
		return TokenSet{}, fmt.Errorf("googleoauth: decode tokens: %w", err)
	}
	return tokens, nil
}

func (c *Client) tokenRequest(ctx context.Context, form url.Values) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, fmt.Errorf("googleoauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TokenSet{}, fmt.Errorf("googleoauth: token request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body close is best-effort after a bounded read
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Provider response bodies can include diagnostics. Do not return them:
		// callers must not accidentally log provider tokens or authorization data.
		return TokenSet{}, fmt.Errorf("googleoauth: token endpoint returned HTTP %d", resp.StatusCode)
	}
	var wire struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire); err != nil {
		return TokenSet{}, fmt.Errorf("googleoauth: decode token response: %w", err)
	}
	if wire.AccessToken == "" || wire.ExpiresIn <= 0 {
		return TokenSet{}, errors.New("googleoauth: token response missing access token or expiry")
	}
	return TokenSet{AccessToken: wire.AccessToken, RefreshToken: wire.RefreshToken, TokenType: wire.TokenType, Scope: wire.Scope, Expiry: time.Now().UTC().Add(time.Duration(wire.ExpiresIn) * time.Second)}, nil
}

func randomURLValue(bytes int) (string, error) {
	v := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, v); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(v), nil
}

func decodeKey(value string) ([]byte, error) {
	key, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		key, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("googleoauth: token key must be base64-encoded 32 random bytes")
	}
	return key, nil
}
