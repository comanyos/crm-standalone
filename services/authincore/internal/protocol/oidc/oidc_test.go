package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/authincore/authincore/internal/authz"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

// --- fakes ---

type fakeClientStore struct {
	client *oidc.Client
	err    error
}

func (s *fakeClientStore) ByClientID(_ context.Context, _ uuid.UUID, _ string) (*oidc.Client, error) {
	return s.client, s.err
}

type fakeTokenStore struct {
	codes    map[string]*oidc.AuthCode
	refresh  map[string]*oidc.RefreshToken
	revoked  []string
	storeErr error
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{
		codes:   make(map[string]*oidc.AuthCode),
		refresh: make(map[string]*oidc.RefreshToken),
	}
}

func (s *fakeTokenStore) StoreAuthCode(_ context.Context, ac *oidc.AuthCode) error {
	if s.storeErr != nil {
		return s.storeErr
	}
	s.codes[ac.Code] = ac
	return nil
}
func (s *fakeTokenStore) ConsumeAuthCode(_ context.Context, code string) (*oidc.AuthCode, error) {
	ac, ok := s.codes[code]
	if !ok {
		return nil, domainerr.New(domainerr.CodeNotFound, "code not found")
	}
	delete(s.codes, code)
	return ac, nil
}
func (s *fakeTokenStore) StoreRefreshToken(_ context.Context, rt *oidc.RefreshToken) error {
	s.refresh[rt.Token] = rt
	return nil
}
func (s *fakeTokenStore) ConsumeRefreshToken(_ context.Context, tok string) (*oidc.RefreshToken, error) {
	rt, ok := s.refresh[tok]
	if !ok {
		return nil, domainerr.New(domainerr.CodeNotFound, "refresh token not found")
	}
	delete(s.refresh, tok)
	return rt, nil
}
func (s *fakeTokenStore) RevokeRefreshToken(_ context.Context, tok string) error {
	s.revoked = append(s.revoked, tok)
	return nil
}

type fakeConsentStore struct {
	requests map[string]*oidc.ConsentRequest
	grants   map[string][]string // clientID → scopes
}

func newFakeConsentStore() *fakeConsentStore {
	return &fakeConsentStore{
		requests: make(map[string]*oidc.ConsentRequest),
		grants:   make(map[string][]string),
	}
}
func (s *fakeConsentStore) StoreRequest(_ context.Context, r *oidc.ConsentRequest) error {
	s.requests[r.ID] = r
	return nil
}
func (s *fakeConsentStore) GetRequest(_ context.Context, id string) (*oidc.ConsentRequest, error) {
	r, ok := s.requests[id]
	if !ok {
		return nil, domainerr.New(domainerr.CodeNotFound, "not found")
	}
	return r, nil
}
func (s *fakeConsentStore) UpdateRequest(_ context.Context, r *oidc.ConsentRequest) error {
	if _, ok := s.requests[r.ID]; !ok {
		return domainerr.New(domainerr.CodeNotFound, "consent request not found")
	}
	s.requests[r.ID] = r
	return nil
}
func (s *fakeConsentStore) RememberedScopes(_ context.Context, _, _ uuid.UUID, clientID string) ([]string, error) {
	return s.grants[clientID], nil
}
func (s *fakeConsentStore) RememberGrant(_ context.Context, _, _ uuid.UUID, clientID string, scopes []string) error {
	s.grants[clientID] = scopes
	return nil
}

type fakeSessionReader struct {
	identityID uuid.UUID
	sessionID  uuid.UUID
	orgID      uuid.UUID
	err        error
}

// fixedRoleStore and fixedPermissionStore are intentionally tiny policy
// doubles for token-claim tests. They exercise the same ClaimsResolver used
// in production rather than injecting scopes directly into a signed token.
type fixedRoleStore struct{ roles []authz.Role }

func (s fixedRoleStore) RolesForIdentity(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ authz.RoleScope) ([]authz.Role, error) {
	return s.roles, nil
}

type fixedPermissionStore struct{ scopes map[string][]string }

func (s fixedPermissionStore) PermissionsForRoles(_ context.Context, _ uuid.UUID, roles []authz.Role) ([]authz.Permission, error) {
	var permissions []authz.Permission
	for _, role := range roles {
		permissions = append(permissions, authz.Permission{ResourceType: "crm", Scopes: s.scopes[role.Key]})
	}
	return permissions, nil
}

func (r *fakeSessionReader) ByToken(_ context.Context, _ string) (uuid.UUID, uuid.UUID, uuid.UUID, error) {
	return r.identityID, r.sessionID, r.orgID, r.err
}

// --- helpers ---

func newSigner(t *testing.T) *token.RS256Signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	s, err := token.NewRS256Signer(key, "test-key-1")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

func newTokenSvc(t *testing.T, signer *token.RS256Signer) *token.Service {
	t.Helper()
	return token.NewService(signer, "http://localhost", 15*time.Minute, 7*24*time.Hour)
}

func newProvider(t *testing.T, clients oidc.ClientStore, tokens oidc.TokenStore) *oidc.Provider {
	t.Helper()
	signer := newSigner(t)
	tokenSvc := newTokenSvc(t, signer)
	return oidc.NewProvider(oidc.Config{
		Issuer:          "http://localhost",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
		IDTokenTTL:      15 * time.Minute,
	}, clients, tokens, tokenSvc, signer)
}

func publicClient() *oidc.Client {
	return &oidc.Client{
		ID:           uuid.New(),
		ClientID:     "test-client",
		RedirectURIs: []string{"https://app.example.com/callback"},
		GrantTypes:   []string{"authorization_code"},
	}
}

// --- tests: WellKnown ---

func TestWellKnownHandler(t *testing.T) {
	p := newProvider(t, &fakeClientStore{}, newFakeTokenStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	p.WellKnownHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var doc oidc.WellKnownDocument
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Issuer != "http://localhost" {
		t.Errorf("issuer mismatch: %s", doc.Issuer)
	}
	if len(doc.ResponseTypesSupported) == 0 {
		t.Error("response_types_supported empty")
	}
}

// --- tests: Authorize ---

func TestAuthorizeHandler_NoSession_RedirectsToLogin(t *testing.T) {
	client := publicClient()
	p := newProvider(t,
		&fakeClientStore{client: client},
		newFakeTokenStore(),
	)
	// No session reader → resolveSession always fails → redirects to login.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=https://app.example.com/callback&scope=openid&state=xyz",
		nil)
	req.Header.Set("X-Org-ID", uuid.New().String())
	p.AuthorizeHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("expected 302 redirect, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	// Must target the hosted-UI SPA route, not the JSON flow API at
	// /self-service/login — a browser sent there renders raw JSON.
	if !strings.HasPrefix(loc, "/login?") {
		t.Errorf("expected redirect to /login, got %s", loc)
	}
	if strings.Contains(loc, "/self-service/") {
		t.Errorf("redirect must not point at the JSON flow API, got %s", loc)
	}

	// The hosted UI navigates to ?return_to after a successful login; that is
	// what resumes this authorize request. Without a complete authorize URL
	// here the user logs in and is stranded, never reaching the client.
	locURL, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("redirect is not a valid URL: %v", err)
	}
	returnTo := locURL.Query().Get("return_to")
	if returnTo == "" {
		t.Fatal("redirect is missing return_to; login would not resume the OAuth flow")
	}
	resumeURL, err := url.Parse(returnTo)
	if err != nil {
		t.Fatalf("return_to is not a valid URL: %v", err)
	}
	if resumeURL.Path != "/oauth2/authorize" {
		t.Errorf("return_to must resume authorize, got %s", resumeURL.Path)
	}
	rq := resumeURL.Query()
	for _, k := range []string{"response_type", "org_id", "client_id", "redirect_uri", "state"} {
		if rq.Get(k) == "" {
			t.Errorf("return_to authorize URL missing %q — resumed request would be rejected", k)
		}
	}
}

func TestAuthorizeHandler_InvalidClient_Error(t *testing.T) {
	p := newProvider(t,
		&fakeClientStore{err: domainerr.New(domainerr.CodeNotFound, "not found")},
		newFakeTokenStore(),
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/authorize?response_type=code&client_id=bad&redirect_uri=https://app.example.com/callback",
		nil)
	req.Header.Set("X-Org-ID", uuid.New().String())
	p.AuthorizeHandler(rec, req)
	// redirect_uri cannot be trusted before client validation, so the handler
	// returns 400 directly rather than redirecting with an error.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_client") {
		t.Errorf("expected invalid_client in body, got %s", rec.Body.String())
	}
}

func TestAuthorizeHandler_UnsupportedResponseType(t *testing.T) {
	p := newProvider(t, &fakeClientStore{client: publicClient()}, newFakeTokenStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/authorize?response_type=token&client_id=test-client&redirect_uri=https://app.example.com/callback",
		nil)
	req.Header.Set("X-Org-ID", uuid.New().String())
	p.AuthorizeHandler(rec, req)
	// Unsupported response_type is rejected before redirect_uri is validated,
	// so the handler returns 400 directly.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unsupported_response_type") {
		t.Errorf("expected unsupported_response_type in body, got %s", rec.Body.String())
	}
}

func TestAuthorizeHandler_WithSession_AllScopesRemembered_IssuesCode(t *testing.T) {
	identityID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	client := publicClient()

	tokenStore := newFakeTokenStore()
	signer := newSigner(t)
	tokenSvc := newTokenSvc(t, signer)

	consentStore := newFakeConsentStore()
	// Pre-remember all requested scopes.
	consentStore.grants["test-client"] = []string{"openid", "email"}

	sessionReader := &fakeSessionReader{identityID: identityID, sessionID: sessionID, orgID: orgID}

	p := oidc.NewProvider(oidc.Config{
		Issuer:          "http://localhost",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	}, &fakeClientStore{client: client}, tokenStore, tokenSvc, signer).
		WithConsent(consentStore).
		WithSessionReader(sessionReader)

	// Build a session JWT cookie so resolveSession can verify it.
	sessionToken, _ := signer.Sign(token.Claims{
		Subject:   identityID.String(),
		SessionID: sessionID.String(),
		OrgID:     orgID.String(),
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=https://app.example.com/callback&scope=openid+email&state=s1",
		nil)
	req.Header.Set("X-Org-ID", orgID.String())
	req.AddCookie(&http.Cookie{Name: "authincore_session", Value: sessionToken})
	p.AuthorizeHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d; body: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if u.Query().Get("code") == "" {
		t.Errorf("expected auth code in redirect, got %s", loc)
	}
	if u.Query().Get("state") != "s1" {
		t.Errorf("state not preserved: %s", loc)
	}
}

func TestAuthorizeHandler_WithSession_MissingConsent_RedirectsToConsentScreen(t *testing.T) {
	identityID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	client := publicClient()

	signer := newSigner(t)
	tokenSvc := newTokenSvc(t, signer)
	consentStore := newFakeConsentStore() // no remembered grants
	sessionReader := &fakeSessionReader{identityID: identityID, sessionID: sessionID, orgID: orgID}

	p := oidc.NewProvider(oidc.Config{Issuer: "http://localhost", AccessTokenTTL: 15 * time.Minute},
		&fakeClientStore{client: client}, newFakeTokenStore(), tokenSvc, signer).
		WithConsent(consentStore).
		WithSessionReader(sessionReader)

	sessionToken, _ := signer.Sign(token.Claims{
		Subject:   identityID.String(),
		SessionID: sessionID.String(),
		OrgID:     orgID.String(),
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=https://app.example.com/callback&scope=openid+email&state=csrf-token",
		nil)
	req.Header.Set("X-Org-ID", orgID.String())
	req.AddCookie(&http.Cookie{Name: "authincore_session", Value: sessionToken})
	p.AuthorizeHandler(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/oauth2/consent") {
		t.Errorf("expected redirect to consent screen, got %s", loc)
	}
	if !strings.Contains(loc, "consent_id=") {
		t.Errorf("expected consent_id in redirect, got %s", loc)
	}
}

// --- tests: Token endpoint ---

func TestTokenHandler_AuthCode_IssuedTokens(t *testing.T) {
	client := publicClient()
	tokenStore := newFakeTokenStore()
	signer := newSigner(t)
	tokenSvc := newTokenSvc(t, signer)
	orgID := uuid.New()
	identityID := uuid.New()

	p := oidc.NewProvider(oidc.Config{
		Issuer:          "http://localhost",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
		IDTokenTTL:      15 * time.Minute,
	}, &fakeClientStore{client: client}, tokenStore, tokenSvc, signer)

	// Pre-store an auth code.
	ctx := context.Background()
	code, err := p.IssueAuthCode(ctx, identityID, uuid.New(), orgID,
		"test-client", "https://app.example.com/callback", "st", "openid email", "", "")
	if err != nil {
		t.Fatalf("IssueAuthCode: %v", err)
	}

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {"https://app.example.com/callback"},
		"client_id":    {"test-client"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Org-ID", orgID.String())
	p.TokenHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["access_token"] == "" {
		t.Error("expected access_token")
	}
	if resp["id_token"] == "" {
		t.Error("expected id_token")
	}
}

func TestTokenHandler_AuthCode_RequestedScopeCannotEscalateRole(t *testing.T) {
	client := publicClient()
	tokenStore := newFakeTokenStore()
	signer := newSigner(t)
	tokenSvc := newTokenSvc(t, signer)
	orgID := uuid.New()
	identityID := uuid.New()

	resolver := authz.NewClaimsResolver(
		fixedRoleStore{roles: []authz.Role{{Key: "crm_viewer", OrgID: orgID}}},
		fixedPermissionStore{scopes: map[string][]string{"crm_viewer": {"crm:account:read"}}},
	)
	p := oidc.NewProvider(oidc.Config{Issuer: "http://localhost", AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 7 * 24 * time.Hour},
		&fakeClientStore{client: client}, tokenStore, tokenSvc, signer).
		WithClaimsResolver(resolver)

	code, err := p.IssueAuthCode(context.Background(), identityID, uuid.New(), orgID,
		"test-client", "https://app.example.com/callback", "st", "openid email profile crm:*", "", "")
	if err != nil {
		t.Fatalf("IssueAuthCode: %v", err)
	}

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"https://app.example.com/callback"}, "client_id": {"test-client"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Org-ID", orgID.String())
	p.TokenHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var response struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	claims, err := signer.Verify(response.AccessToken)
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	if !contains(claims.Scopes, "crm:account:read") {
		t.Fatalf("viewer permission absent from token: %v", claims.Scopes)
	}
	if contains(claims.Scopes, "crm:*") {
		t.Fatalf("requested crm:* leaked into viewer token: %v", claims.Scopes)
	}
	if contains(claims.Scopes, "crm:account:write") {
		t.Fatalf("viewer unexpectedly received write permission: %v", claims.Scopes)
	}
}

func contains(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func TestTokenHandler_AuthCode_InvalidCode(t *testing.T) {
	p := newProvider(t, &fakeClientStore{client: publicClient()}, newFakeTokenStore())
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {"bad-code"},
		"redirect_uri": {"https://app.example.com/callback"},
		"client_id":    {"test-client"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Org-ID", uuid.New().String())
	p.TokenHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestTokenHandler_UnsupportedGrantType(t *testing.T) {
	p := newProvider(t, &fakeClientStore{}, newFakeTokenStore())
	form := url.Values{"grant_type": {"implicit"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.TokenHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// --- tests: Revoke ---

func TestRevokeHandler_AlwaysReturns200(t *testing.T) {
	p := newProvider(t, &fakeClientStore{}, newFakeTokenStore())
	form := url.Values{"token": {"some-refresh-token"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.RevokeHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 per RFC 7009, got %d", rec.Code)
	}
}

// --- tests: Introspect ---

func TestIntrospectHandler_ValidToken_Active(t *testing.T) {
	signer := newSigner(t)
	tokenSvc := newTokenSvc(t, signer)
	p := oidc.NewProvider(oidc.Config{Issuer: "http://localhost", AccessTokenTTL: 15 * time.Minute},
		&fakeClientStore{}, newFakeTokenStore(), tokenSvc, signer)

	identityID := uuid.New()
	accessToken, _ := tokenSvc.MintAccessToken(context.Background(), identityID, uuid.New(), uuid.New(), nil, nil, "", 1)

	form := url.Values{"token": {accessToken}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/introspect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.IntrospectHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result["active"] != true {
		t.Errorf("expected active=true, got %v", result["active"])
	}
}

func TestIntrospectHandler_InvalidToken_Inactive(t *testing.T) {
	p := newProvider(t, &fakeClientStore{}, newFakeTokenStore())
	form := url.Values{"token": {"not-a-jwt"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth2/introspect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.IntrospectHandler(rec, req)

	var result map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result["active"] != false {
		t.Errorf("expected active=false for invalid token, got %v", result["active"])
	}
}
