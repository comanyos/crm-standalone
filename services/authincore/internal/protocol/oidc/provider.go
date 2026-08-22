// Package oidc implements the OIDC/OAuth2 provider endpoints.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/authincore/authincore/internal/authz"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

// Config holds per-instance OIDC provider settings.
type Config struct {
	Issuer          string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	IDTokenTTL      time.Duration
}

// ClientStore resolves OIDC client (application) configs.
type ClientStore interface {
	ByClientID(ctx context.Context, orgID uuid.UUID, clientID string) (*Client, error)
}

// Client represents an OIDC application.
type Client struct {
	ID              uuid.UUID
	ClientID        string
	ClientSecret    string // empty for public clients
	RedirectURIs    []string
	GrantTypes      []string
	ResponseTypes   []string
	ApplicationType string
	AuthMethodType  string
}

// TokenStore persists authorization codes and refresh tokens.
type TokenStore interface {
	StoreAuthCode(ctx context.Context, code *AuthCode) error
	ConsumeAuthCode(ctx context.Context, code string) (*AuthCode, error)
	StoreRefreshToken(ctx context.Context, rt *RefreshToken) error
	ConsumeRefreshToken(ctx context.Context, token string) (*RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, token string) error
}

// AuthCode represents a short-lived authorization code.
type AuthCode struct {
	Code                string
	ClientID            string
	IdentityID          uuid.UUID
	OrgID               uuid.UUID
	SessionID           uuid.UUID
	RedirectURI         string
	Scopes              []string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

// RefreshToken represents a long-lived refresh token.
type RefreshToken struct {
	Token      string
	IdentityID uuid.UUID
	SessionID  uuid.UUID
	OrgID      uuid.UUID
	ClientID   string
	Scopes     []string
	ExpiresAt  time.Time
}

// WellKnownDocument is the OIDC discovery document.
type WellKnownDocument struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserinfoEndpoint                  string   `json:"userinfo_endpoint"`
	JWKSUri                           string   `json:"jwks_uri"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	SubjectTypesSupported             []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported  []string `json:"id_token_signing_alg_values_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	ClaimsSupported                   []string `json:"claims_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
}

// ProviderSigner is the subset of token signing capability the OIDC provider needs.
// Both *token.RS256Signer and *token.RotatingSigner satisfy this interface.
type ProviderSigner interface {
	token.Signer
	JWKS() token.JWKSDocument
	KeyID() string
}

// SessionReader checks whether an active, non-expired session exists.
// sessionID is the JWT sid claim (UUID string). Returns identityID, sessionID, orgID.
type SessionReader interface {
	ByToken(ctx context.Context, sessionID string) (identityID, sessID, orgID uuid.UUID, err error)
}

// Provider serves OIDC endpoints.
type Provider struct {
	cfg      Config
	clients  ClientStore
	tokens   TokenStore
	consent  ConsentStore
	sessions SessionReader
	tokenSvc *token.Service
	signer   ProviderSigner
	claims   *authz.ClaimsResolver
}

// WithClaimsResolver attaches the resolver that adds an identity's roles and
// org-granted authorization scopes to tokens minted at the token endpoint.
//
// Requested OAuth/OIDC scopes are deliberately not treated as authorization.
// A public client can choose its own `scope` query parameter, so allowing a
// request such as `crm:*` into an access token would let any signed-in user
// manufacture CRM authority. Human tokens retain only standard identity
// protocol scopes from the request; CRM and other resource scopes come solely
// from the identity's role permissions.
func (p *Provider) WithClaimsResolver(cr *authz.ClaimsResolver) *Provider {
	p.claims = cr
	return p
}

// protocolScopes returns the requested scopes that are safe to carry in a
// human access token without granting resource authority. New OIDC identity
// scopes must be added here deliberately; arbitrary `foo:*` values must never
// pass through this boundary.
func protocolScopes(requested []string) []string {
	allowed := map[string]struct{}{
		"openid": {}, "email": {}, "profile": {}, "offline_access": {},
	}
	filtered := make([]string, 0, len(requested))
	for _, scope := range requested {
		if _, ok := allowed[scope]; ok {
			filtered = append(filtered, scope)
		}
	}
	return filtered
}

// unionScopes merges two scope lists, preserving the order of the first and
// dropping duplicates. A scope repeated in the token is not wrong, but it makes
// two equivalent tokens compare unequal and grows every request header.
func unionScopes(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func NewProvider(cfg Config, clients ClientStore, tokens TokenStore, tokenSvc *token.Service, signer ProviderSigner) *Provider {
	return &Provider{
		cfg:      cfg,
		clients:  clients,
		tokens:   tokens,
		tokenSvc: tokenSvc,
		signer:   signer,
	}
}

// WithConsent attaches a ConsentStore so the authorize endpoint can check
// remembered grants and redirect to the consent screen when needed.
func (p *Provider) WithConsent(cs ConsentStore) *Provider {
	p.consent = cs
	return p
}

// WithSessionReader attaches a session reader so AuthorizeHandler can detect
// an already-authenticated user and skip the login step.
func (p *Provider) WithSessionReader(sr SessionReader) *Provider {
	p.sessions = sr
	return p
}

// WellKnown returns the OIDC discovery document.
func (p *Provider) WellKnown(_ context.Context) *WellKnownDocument {
	base := p.cfg.Issuer
	return &WellKnownDocument{
		Issuer:                            base,
		AuthorizationEndpoint:             base + "/oauth2/authorize",
		TokenEndpoint:                     base + "/oauth2/token",
		UserinfoEndpoint:                  base + "/oauth2/userinfo",
		JWKSUri:                           base + "/oauth2/keys",
		RevocationEndpoint:                base + "/oauth2/revoke",
		ResponseTypesSupported:            []string{"code"},
		SubjectTypesSupported:             []string{"public"},
		IDTokenSigningAlgValuesSupported:  []string{"RS256"},
		ScopesSupported:                   []string{"openid", "email", "profile", "offline_access"},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "client_secret_post", "none"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token", "client_credentials"},
		ClaimsSupported:                   []string{"sub", "iss", "aud", "iat", "exp", "email", "name", "sid"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
}

// WellKnownHandler serves GET /.well-known/openid-configuration.
func (p *Provider) WellKnownHandler(w http.ResponseWriter, r *http.Request) {
	doc := p.WellKnown(r.Context())
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		http.Error(w, fmt.Sprintf("encode: %v", err), http.StatusInternalServerError)
	}
}

// RevokeHandler handles POST /oauth2/revoke (RFC 7009).
// Accepts a refresh token and marks it as revoked.
// Always returns 200 per the RFC (to avoid token existence oracle).
func (p *Provider) RevokeHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	if token != "" && p.tokens != nil {
		_ = p.tokens.RevokeRefreshToken(r.Context(), token)
	}
	w.WriteHeader(http.StatusOK)
}

// IntrospectHandler handles POST /oauth2/introspect (RFC 7662).
// Returns active=false for any token that cannot be verified rather than an error,
// so resource servers cannot distinguish expired from nonexistent tokens.
func (p *Provider) IntrospectHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	tokenStr := r.FormValue("token")
	w.Header().Set("Content-Type", "application/json")

	if tokenStr == "" || p.signer == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		return
	}

	claims, err := p.signer.Verify(tokenStr)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"active":     true,
		"sub":        claims.Subject,
		"iss":        claims.Issuer,
		"aud":        claims.Audience,
		"exp":        claims.ExpiresAt.Unix(),
		"iat":        claims.IssuedAt.Unix(),
		"jti":        claims.JWTID,
		"sid":        claims.SessionID,
		"org_id":     claims.OrgID,
		"email":      claims.Email,
		"token_type": "Bearer",
	})
}
