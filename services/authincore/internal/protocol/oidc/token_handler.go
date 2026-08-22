package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/authincore/authincore/internal/metrics"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

const (
	headerOrgID      = "X-Org-ID"
	errOrgIDRequired = "org_id required"
	errInvalidCreds  = "invalid client credentials"
	errMintTokens    = "could not mint tokens"
)

// --- Authorization endpoint ---

// AuthorizeHandler handles GET /oauth2/authorize (RFC 6749 §4.1).
//
// Full flow:
//  1. Validate client_id + redirect_uri.
//  2. Check for an existing session cookie — if present, skip login.
//  3. If no session, redirect to the self-service login UI.
//  4. After login the UI posts back here with consent_id (from a completed
//     ConsentRequest) OR the user is redirected here after submitting consent.
//  5. Check ConsentStore for remembered grants — skip consent screen if all
//     requested scopes were previously approved.
//  6. If consent is needed, create a ConsentRequest and redirect to /oauth2/consent.
//  7. Once consent is granted (consent_id present and approved), issue the auth code.
func (p *Provider) AuthorizeHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ap, orgID, client, err := p.parseAuthorizeRequest(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Public clients must supply state to prevent CSRF (Bug 25).
	if !isConfidentialClient(client) && ap.state == "" {
		redirectWithError(w, r, ap.redirectURI, ap.state, "invalid_request", "state parameter required for public clients")
		return
	}

	// Returning from consent screen.
	if consentID := q.Get("consent_id"); consentID != "" && p.consent != nil {
		p.handleConsentCallback(w, r, consentID, orgID, ap)
		return
	}

	identityID, sessionID, err := p.resolveSession(r)
	if err != nil {
		// "/login" is the hosted-UI SPA route. "/self-service/login" is the JSON
		// flow API that the SPA itself calls — redirecting a browser there
		// renders a raw JSON blob with no way to sign in.
		http.Redirect(w, r, buildLoginRedirect(ap, orgID), http.StatusFound)
		return
	}

	if needsConsent := p.checkConsentRequired(r.Context(), orgID, identityID, client, ap); needsConsent {
		p.redirectToConsent(w, r, orgID, identityID, client, ap, strings.Fields(ap.scope))
		return
	}

	p.issueCodeAndRedirect(w, r, identityID, sessionID, orgID, ap)
}

// parseAuthorizeRequest validates the required OAuth2 authorize parameters and resolves the client.
// Returns an error string safe to show directly (no redirect_uri used until validated).
func (p *Provider) parseAuthorizeRequest(ctx context.Context, r *http.Request) (authorizeParams, uuid.UUID, *Client, error) {
	q := r.URL.Query()
	responseType := q.Get("response_type")
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")

	if responseType != "code" {
		return authorizeParams{}, uuid.Nil, nil, fmt.Errorf("unsupported_response_type: only code flow is supported")
	}
	if clientID == "" || redirectURI == "" {
		return authorizeParams{}, uuid.Nil, nil, fmt.Errorf("client_id and redirect_uri are required")
	}
	orgIDStr := r.Header.Get(headerOrgID)
	if orgIDStr == "" {
		orgIDStr = q.Get("org_id")
	}
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		return authorizeParams{}, uuid.Nil, nil, fmt.Errorf(errOrgIDRequired)
	}
	client, err := p.clients.ByClientID(ctx, orgID, clientID)
	if err != nil {
		return authorizeParams{}, uuid.Nil, nil, fmt.Errorf("invalid_client: unknown client_id")
	}
	if !isAllowedRedirectURI(client.RedirectURIs, redirectURI) {
		return authorizeParams{}, uuid.Nil, nil, fmt.Errorf("invalid_request: redirect_uri not allowed")
	}
	ap := authorizeParams{
		clientID:            clientID,
		redirectURI:         redirectURI,
		state:               q.Get("state"),
		scope:               q.Get("scope"),
		codeChallenge:       q.Get("code_challenge"),
		codeChallengeMethod: q.Get("code_challenge_method"),
	}
	return ap, orgID, client, nil
}

func (p *Provider) checkConsentRequired(ctx context.Context, orgID, identityID uuid.UUID, client *Client, ap authorizeParams) bool {
	if p.consent == nil || isConfidentialClient(client) || client.ClientID == "platform-shell" {
		return false
	}
	remembered, _ := p.consent.RememberedScopes(ctx, orgID, identityID, client.ClientID)
	return !scopesSubset(strings.Fields(ap.scope), remembered)
}

func (p *Provider) issueCodeAndRedirect(w http.ResponseWriter, r *http.Request, identityID, sessionID, orgID uuid.UUID, ap authorizeParams) {
	code, err := p.IssueAuthCode(r.Context(), identityID, sessionID, orgID,
		ap.clientID, ap.redirectURI, ap.state, ap.scope, ap.codeChallenge, ap.codeChallengeMethod)
	if err != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "server_error", "failed to issue code")
		return
	}
	dest := ap.redirectURI + "?code=" + url.QueryEscape(code)
	if ap.state != "" {
		dest += "&state=" + url.QueryEscape(ap.state)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// authorizeParams carries the parsed OAuth2 authorize query parameters.
type authorizeParams struct {
	clientID            string
	redirectURI         string
	state               string
	scope               string
	codeChallenge       string
	codeChallengeMethod string
}

// handleConsentCallback is called when the user returns from /oauth2/consent
// with a consent_id query param. Validates the decision and issues the auth code.
func (p *Provider) handleConsentCallback(w http.ResponseWriter, r *http.Request,
	consentID string, orgID uuid.UUID, ap authorizeParams) {

	ctx := r.Context()
	req, err := p.consent.GetRequest(ctx, consentID)
	if err != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "access_denied", "consent request not found")
		return
	}
	if !req.Granted {
		redirectWithError(w, r, ap.redirectURI, ap.state, "access_denied", "user denied consent")
		return
	}
	if time.Now().After(req.ExpiresAt) {
		redirectWithError(w, r, ap.redirectURI, ap.state, "access_denied", "consent request expired")
		return
	}
	// Prevent replay: a consent_id stays in the store until it expires, so
	// mark it redeemed before issuing the code to close the reuse window.
	if req.RedeemedAt != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "access_denied", "consent already redeemed")
		return
	}

	// Verify the current session belongs to the identity that granted consent.
	identityID, sessionID, err := p.resolveSession(r)
	if err != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "access_denied", "session required")
		return
	}
	if identityID != req.IdentityID {
		redirectWithError(w, r, ap.redirectURI, ap.state, "access_denied", "session identity mismatch")
		return
	}

	// Atomically mark the consent as redeemed before issuing the auth code.
	now := time.Now().UTC()
	req.RedeemedAt = &now
	if err := p.consent.UpdateRequest(ctx, req); err != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "server_error", "failed to redeem consent")
		return
	}

	scope := strings.Join(req.Scopes, " ")
	code, err := p.IssueAuthCode(ctx, req.IdentityID, sessionID, orgID,
		ap.clientID, ap.redirectURI, ap.state, scope, ap.codeChallenge, ap.codeChallengeMethod)
	if err != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "server_error", "failed to issue code")
		return
	}
	dest := ap.redirectURI + "?code=" + url.QueryEscape(code)
	if ap.state != "" {
		dest += "&state=" + url.QueryEscape(ap.state)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// redirectToConsent creates a ConsentRequest and sends the browser to the consent screen.
func (p *Provider) redirectToConsent(w http.ResponseWriter, r *http.Request,
	orgID, identityID uuid.UUID, client *Client, ap authorizeParams, scopes []string) {

	consentID := generateConsentID()
	req := &ConsentRequest{
		ID:                  consentID,
		ClientID:            client.ClientID,
		ClientName:          client.ClientID,
		IdentityID:          identityID,
		OrgID:               orgID,
		Scopes:              scopes,
		RedirectURI:         ap.redirectURI,
		State:               ap.state,
		CodeChallenge:       ap.codeChallenge,
		CodeChallengeMethod: ap.codeChallengeMethod,
		ExpiresAt:           time.Now().UTC().Add(10 * time.Minute),
	}
	if err := p.consent.StoreRequest(r.Context(), req); err != nil {
		redirectWithError(w, r, ap.redirectURI, ap.state, "server_error", "consent store failed")
		return
	}
	http.Redirect(w, r, "/oauth2/consent?consent_id="+consentID, http.StatusFound)
}

// resolveSession reads the authincore_session cookie, verifies the JWT signature,
// and DB-confirms the session is still active. Returns (identityID, sessionID).
func (p *Provider) resolveSession(r *http.Request) (uuid.UUID, uuid.UUID, error) {
	var cookie *http.Cookie
	var err error
	for _, name := range []string{"__Host-authincore_session", "authincore_session", "ory_session"} {
		cookie, err = r.Cookie(name)
		if err == nil {
			break
		}
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("no session cookie")
	}
	if p.sessions == nil || p.signer == nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("session reader not configured")
	}
	claims, err := p.signer.Verify(cookie.Value)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid session token: %w", err)
	}
	// DB-verify the session is still active (not revoked or expired).
	identityID, sessionID, _, err := p.sessions.ByToken(r.Context(), claims.SessionID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("session inactive: %w", err)
	}
	return identityID, sessionID, nil
}

// buildAuthorizeQuery assembles the query string to pass authorize params through login.
func buildAuthorizeQuery(ap authorizeParams) string {
	v := url.Values{
		"login_challenge": {"oidc"},
		"client_id":       {ap.clientID},
		"redirect_uri":    {ap.redirectURI},
		"scope":           {ap.scope},
		"state":           {ap.state},
	}
	if ap.codeChallenge != "" {
		v.Set("code_challenge", ap.codeChallenge)
		v.Set("code_challenge_method", ap.codeChallengeMethod)
	}
	return v.Encode()
}

// buildLoginRedirect returns the hosted-UI login URL for an unauthenticated
// authorize request. The hosted UI reads ?return_to and navigates there once
// login succeeds, which is what resumes the interrupted OAuth flow; without it
// the user lands on a bare login page and the authorize request is lost.
//
// return_to must be a complete authorize URL: response_type and org_id are not
// part of buildAuthorizeQuery, but the resumed request is rejected without them.
func buildLoginRedirect(ap authorizeParams, orgID uuid.UUID) string {
	resume := url.Values{}
	for k, vs := range mustParseQuery(buildAuthorizeQuery(ap)) {
		resume[k] = vs
	}
	resume.Set("response_type", "code")
	resume.Set("org_id", orgID.String())

	return "/login?" + url.Values{
		"return_to": {"/oauth2/authorize?" + resume.Encode()},
	}.Encode()
}

// mustParseQuery parses a query string we just built ourselves, so a parse
// error is impossible; on the theoretical error it yields an empty set rather
// than panicking in a request path.
func mustParseQuery(q string) url.Values {
	v, err := url.ParseQuery(q)
	if err != nil {
		return url.Values{}
	}
	return v
}

// orgIDFromTokenRequest resolves the tenant for a token-endpoint call. The
// X-Org-ID header is preferred, falling back to an org_id form parameter:
// standard OAuth clients post form fields and cannot be relied on to send a
// custom header, and /oauth2/authorize already accepts the same fallback.
func orgIDFromTokenRequest(r *http.Request) string {
	if v := r.Header.Get(headerOrgID); v != "" {
		return v
	}
	return r.FormValue("org_id")
}

// scopesSubset returns true when every requested scope is in the granted set.
func scopesSubset(requested, granted []string) bool {
	set := make(map[string]bool, len(granted))
	for _, s := range granted {
		set[s] = true
	}
	for _, s := range requested {
		if !set[s] {
			return false
		}
	}
	return true
}

// isConfidentialClient returns true for server-side clients that don't need
// user-facing consent (they act on their own behalf, not on behalf of a user).
func isConfidentialClient(c *Client) bool {
	return c.ClientSecret != "" && c.ApplicationType == "web"
}

func generateConsentID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// IssueAuthCode is called by the flow engine after successful authentication.
// It stores an auth code and redirects the browser back to the client.
func (p *Provider) IssueAuthCode(ctx context.Context, identityID, sessionID, orgID uuid.UUID, clientID, redirectURI, state, scope, codeChallenge, codeChallengeMethod string) (string, error) {
	code, err := generateCode()
	if err != nil {
		return "", fmt.Errorf("generate auth code: %w", err)
	}
	scopes := strings.Fields(scope)

	ac := &AuthCode{
		Code:                code,
		ClientID:            clientID,
		IdentityID:          identityID,
		OrgID:               orgID,
		SessionID:           sessionID,
		RedirectURI:         redirectURI,
		Scopes:              scopes,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		ExpiresAt:           time.Now().UTC().Add(5 * time.Minute),
	}
	if err := p.tokens.StoreAuthCode(ctx, ac); err != nil {
		return "", fmt.Errorf("store auth code: %w", err)
	}
	return code, nil
}

// --- Token endpoint ---

// TokenHandler handles POST /oauth2/token
func (p *Provider) TokenHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request", "cannot parse form")
		return
	}

	grantType := r.FormValue("grant_type")
	switch grantType {
	case "authorization_code":
		p.handleAuthCodeGrant(w, r)
	case "refresh_token":
		p.handleRefreshTokenGrant(w, r)
	case "client_credentials":
		p.handleClientCredentialsGrant(w, r)
	default:
		tokenError(w, "unsupported_grant_type", fmt.Sprintf("grant_type %q not supported", grantType))
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

func (p *Provider) handleAuthCodeGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.FormValue("code")
	redirectURI := r.FormValue("redirect_uri")
	clientID, clientSecret := extractClientCredentials(r)

	if code == "" || redirectURI == "" || clientID == "" {
		tokenError(w, "invalid_request", "code, redirect_uri, client_id are required")
		return
	}

	orgID, err := uuid.Parse(orgIDFromTokenRequest(r))
	if err != nil {
		tokenError(w, "invalid_request", errOrgIDRequired)
		return
	}

	client, err := p.clients.ByClientID(ctx, orgID, clientID)
	if err != nil {
		tokenError(w, "invalid_client", "unknown client")
		return
	}
	if client.ClientSecret != "" && client.ClientSecret != clientSecret {
		tokenError(w, "invalid_client", errInvalidCreds)
		return
	}

	ac, err := p.tokens.ConsumeAuthCode(ctx, code)
	if err != nil {
		tokenError(w, "invalid_grant", "auth code not found or expired")
		return
	}
	if ac.ClientID != clientID {
		tokenError(w, "invalid_grant", "client_id mismatch")
		return
	}
	if ac.RedirectURI != redirectURI {
		tokenError(w, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if time.Now().UTC().After(ac.ExpiresAt) {
		tokenError(w, "invalid_grant", "auth code expired")
		return
	}

	if err := verifyPKCE(r.FormValue("code_verifier"), ac.CodeChallenge, ac.CodeChallengeMethod); err != nil {
		tokenError(w, "invalid_grant", err.Error())
		return
	}

	resp, err := p.mintTokens(ctx, ac.IdentityID, ac.SessionID, ac.OrgID, ac.Scopes, client)
	if err != nil {
		tokenError(w, "server_error", errMintTokens)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (p *Provider) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rawToken := r.FormValue("refresh_token")
	clientID, clientSecret := extractClientCredentials(r)

	if rawToken == "" || clientID == "" {
		tokenError(w, "invalid_request", "refresh_token and client_id required")
		return
	}

	orgID, err := uuid.Parse(orgIDFromTokenRequest(r))
	if err != nil {
		tokenError(w, "invalid_request", errOrgIDRequired)
		return
	}

	client, err := p.clients.ByClientID(ctx, orgID, clientID)
	if err != nil {
		tokenError(w, "invalid_client", "unknown client")
		return
	}
	if client.ClientSecret != "" && client.ClientSecret != clientSecret {
		tokenError(w, "invalid_client", errInvalidCreds)
		return
	}

	rt, err := p.tokens.ConsumeRefreshToken(ctx, rawToken)
	if err != nil {
		tokenError(w, "invalid_grant", "refresh token not found or already used")
		return
	}
	if rt.ClientID != clientID {
		tokenError(w, "invalid_grant", "client_id mismatch")
		return
	}
	if time.Now().UTC().After(rt.ExpiresAt) {
		tokenError(w, "invalid_grant", "refresh token expired")
		return
	}

	resp, err := p.mintTokensWithRotation(ctx, rt.IdentityID, rt.SessionID, rt.OrgID, rt.Scopes, client)
	if err != nil {
		tokenError(w, "server_error", errMintTokens)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mintTokensWithRotation issues a new access token and a fresh single-use refresh token,
// replacing the consumed token (RFC 6749 §10.4 rotation).
func (p *Provider) mintTokensWithRotation(ctx context.Context, identityID, sessionID, orgID uuid.UUID, scopes []string, client *Client) (*tokenResponse, error) {
	resp, err := p.mintTokens(ctx, identityID, sessionID, orgID, scopes, client)
	if err != nil {
		return nil, err
	}
	// mintTokens already stored a new refresh token; the response includes it.
	return resp, nil
}

func (p *Provider) handleClientCredentialsGrant(w http.ResponseWriter, r *http.Request) {
	// Machine-to-machine: issue an access token without a user session.
	clientID, clientSecret := extractClientCredentials(r)
	if clientID == "" || clientSecret == "" {
		tokenError(w, "invalid_client", "client_id and client_secret required")
		return
	}
	orgID, err := uuid.Parse(orgIDFromTokenRequest(r))
	if err != nil {
		tokenError(w, "invalid_request", errOrgIDRequired)
		return
	}
	client, err := p.clients.ByClientID(r.Context(), orgID, clientID)
	if err != nil || client.ClientSecret != clientSecret {
		tokenError(w, "invalid_client", errInvalidCreds)
		return
	}
	// Issue access token scoped to the client (no identity, no refresh token).
	resp, err := p.mintTokens(r.Context(), uuid.Nil, uuid.Nil, orgID, strings.Fields(r.FormValue("scope")), client)
	if err != nil {
		tokenError(w, "server_error", errMintTokens)
		return
	}
	resp.RefreshToken = "" // no refresh token for client_credentials
	writeJSON(w, http.StatusOK, resp)
}

// UserInfoHandler handles GET /oauth2/userinfo
func (p *Provider) UserInfoHandler(w http.ResponseWriter, r *http.Request) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
		return
	}
	claims, err := p.tokenSvc.Verify(bearer)
	if err != nil {
		http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
		return
	}
	info := map[string]any{
		"sub":   claims.Subject,
		"email": claims.Email,
		"sid":   claims.SessionID,
	}
	writeJSON(w, http.StatusOK, info)
}

// JWKSHandler handles GET /oauth2/keys
func (p *Provider) JWKSHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, p.signer.JWKS())
}

// --- helpers ---

func (p *Provider) mintTokens(ctx context.Context, identityID, sessionID, orgID uuid.UUID, scopes []string, client *Client) (*tokenResponse, error) {
	// client_credentials has no user behind it and arrives with identityID unset.
	// Resolving roles for uuid.Nil would query for an identity that cannot exist;
	// such a token carries only the scopes the client was granted.
	var roles []string
	if identityID != uuid.Nil {
		identityRoles, identityScopes, err := p.claims.Resolve(ctx, orgID, identityID)
		if err != nil {
			return nil, fmt.Errorf("resolve token claims: %w", err)
		}
		roles = identityRoles
		// The browser controls the scopes requested at /authorize. Preserve
		// only standard OIDC protocol scopes from that request, then derive all
		// resource authority from the server-side role model. In particular,
		// never allow a request for crm:* to turn a viewer token into an admin
		// token. See protocolScopes and TestTokenHandler_AuthCode_RequestedScopeCannotEscalateRole.
		scopes = unionScopes(protocolScopes(scopes), identityScopes)
	}

	accessToken, err := p.tokenSvc.MintAccessToken(ctx, identityID, orgID, sessionID, roles, scopes, "", 1)
	if err != nil {
		return nil, err
	}

	refreshTokenStr, expiresAt, err := p.tokenSvc.MintRefreshToken(ctx)
	if err != nil {
		return nil, err
	}

	clientID := ""
	if client != nil {
		clientID = client.ClientID
	}

	if err := p.tokens.StoreRefreshToken(ctx, &RefreshToken{
		Token:      refreshTokenStr,
		IdentityID: identityID,
		SessionID:  sessionID,
		OrgID:      orgID,
		ClientID:   clientID,
		Scopes:     scopes,
		ExpiresAt:  expiresAt,
	}); err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}

	resp := &tokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(p.cfg.AccessTokenTTL.Seconds()),
		RefreshToken: refreshTokenStr,
		Scope:        strings.Join(scopes, " "),
	}

	// Issue an ID token when the openid scope is requested (Bug 15).
	if hasScope(scopes, "openid") && identityID != uuid.Nil {
		idToken, err := p.mintIDToken(ctx, identityID, sessionID, orgID, clientID, scopes)
		if err != nil {
			return nil, fmt.Errorf("mint id_token: %w", err)
		}
		resp.IDToken = idToken
	}

	return resp, nil
}

func (p *Provider) mintIDToken(_ context.Context, identityID, sessionID, orgID uuid.UUID, clientID string, scopes []string) (string, error) {
	now := time.Now().UTC()
	aud := clientID
	if aud == "" {
		aud = p.cfg.Issuer
	}
	claims := token.Claims{
		Subject:   identityID.String(),
		Issuer:    p.cfg.Issuer,
		Audience:  []string{aud},
		IssuedAt:  now,
		ExpiresAt: now.Add(p.cfg.IDTokenTTL),
		JWTID:     uuid.New().String(),
		SessionID: sessionID.String(),
		OrgID:     orgID.String(),
	}
	_ = scopes // email/profile claims require a userinfo lookup; deferred to a future iteration
	return p.signer.Sign(claims)
}

func hasScope(scopes []string, target string) bool {
	for _, s := range scopes {
		if s == target {
			return true
		}
	}
	return false
}

func verifyPKCE(verifier, challenge, method string) error {
	if challenge == "" {
		return nil // PKCE not required for confidential clients
	}
	if verifier == "" {
		return errors.New("code_verifier required")
	}
	switch method {
	case "S256":
		return verifySHA256(verifier, challenge)
	default:
		// "plain" is intentionally rejected: it provides no security over HTTPS
		// interception and was removed from the PKCE best-current-practice (BCP).
		return fmt.Errorf("unsupported code_challenge_method %q: only S256 is accepted", method)
	}
}

func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, errCode, desc string) {
	if redirectURI == "" {
		http.Error(w, fmt.Sprintf("%s: %s", errCode, desc), http.StatusBadRequest)
		return
	}
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", errCode)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func tokenError(w http.ResponseWriter, errCode, desc string) {
	metrics.OIDCValidationFailures.WithLabelValues(errCode).Inc()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             errCode,
		"error_description": desc,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func extractClientCredentials(r *http.Request) (clientID, clientSecret string) {
	// Try HTTP Basic first (RFC 6749 §2.3.1).
	if id, secret, ok := r.BasicAuth(); ok {
		return id, secret
	}
	// Fall back to form body.
	return r.FormValue("client_id"), r.FormValue("client_secret")
}

func isAllowedRedirectURI(allowed []string, requested string) bool {
	for _, u := range allowed {
		if u == requested {
			return true
		}
	}
	return false
}

func generateCode() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
