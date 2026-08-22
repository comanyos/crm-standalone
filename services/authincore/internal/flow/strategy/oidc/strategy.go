// Package oidc implements the OIDC/social-login strategy for flows.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

const ID = "oidc"

// Provider abstracts an upstream OIDC provider configuration.
type Provider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	UserInfoURL  string   `json:"user_info_url"`
	Scopes       []string `json:"scopes"`
	Mapper       string   `json:"mapper,omitempty"` // JSON path traits mapper
}

// ProviderRegistry returns configured upstream IdP providers for an org.
type ProviderRegistry interface {
	List(ctx context.Context, orgID uuid.UUID) ([]*Provider, error)
	ByID(ctx context.Context, orgID uuid.UUID, providerID string) (*Provider, error)
}

// IdentityLinker creates or links identities after OIDC token exchange.
type IdentityLinker interface {
	LinkOrCreate(ctx context.Context, orgID uuid.UUID, provider *Provider, subject string, claims json.RawMessage) (identityID uuid.UUID, created bool, err error)
}

type Strategy struct {
	registry   ProviderRegistry
	linker     IdentityLinker
	httpClient *http.Client
}

func New(registry ProviderRegistry, linker IdentityLinker) *Strategy {
	return &Strategy{
		registry:   registry,
		linker:     linker,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(ctx context.Context, f *domflow.Flow) error {
	providers, err := s.registry.List(ctx, f.OrgID)
	if err != nil {
		return fmt.Errorf("oidc strategy list providers: %w", err)
	}
	for _, p := range providers {
		f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
			Type:  "input",
			Group: "oidc",
			Attributes: mustJSON(map[string]any{
				"name":      "provider",
				"type":      "submit",
				"value":     p.ID,
				"node_type": "input",
				"provider":  p.ID,
				"label":     fmt.Sprintf("Sign in with %s", p.Name),
			}),
		})
	}
	return nil
}

type submitBody struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
	State    string `json:"state"`
}

func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b submitBody
	if err := json.Unmarshal(body, &b); err != nil || b.Provider == "" {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "provider required")
	}

	p, err := s.registry.ByID(ctx, f.OrgID, b.Provider)
	if err != nil {
		return nil, domainerr.New(domainerr.CodeNotFound, "provider not found")
	}

	if b.Code == "" {
		// Phase 1: build redirect URL to upstream IdP.
		redirectURL := buildAuthURL(p, f.ID.String())
		f.InternalContext, _ = json.Marshal(map[string]string{
			"redirect_to": redirectURL,
			"provider":    b.Provider,
		})
		// Signal the flow layer to redirect the browser.
		return nil, domainerr.Wrap(domainerr.CodePreconditionFailed, "redirect:"+redirectURL, nil)
	}

	// Phase 2: exchange code for tokens then fetch user info.
	subject, claims, err := s.exchangeCodeAndFetchUser(ctx, p, b.Code, f.ID.String())
	if err != nil {
		return nil, fmt.Errorf("oidc code exchange: %w", err)
	}

	identityID, _, err := s.linker.LinkOrCreate(ctx, f.OrgID, p, subject, claims)
	if err != nil {
		return nil, fmt.Errorf("oidc link identity: %w", err)
	}
	return &identityID, nil
}

// exchangeCodeAndFetchUser performs the real OAuth2 authorization_code exchange
// and fetches the user's profile from the upstream IdP's userinfo endpoint.
func (s *Strategy) exchangeCodeAndFetchUser(ctx context.Context, p *Provider, code, state string) (subject string, claims json.RawMessage, err error) {
	// Step 1: exchange code for tokens.
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {callbackURL(state)},
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("token endpoint request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", nil, fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", nil, fmt.Errorf("no access_token in response")
	}

	// Step 2: fetch user info.
	if p.UserInfoURL == "" {
		return "", nil, fmt.Errorf("provider %s has no userinfo_url configured", p.ID)
	}
	uiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("build userinfo request: %w", err)
	}
	uiReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)

	uiResp, err := s.httpClient.Do(uiReq)
	if err != nil {
		return "", nil, fmt.Errorf("userinfo request: %w", err)
	}
	defer uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(uiResp.Body)
		return "", nil, fmt.Errorf("userinfo returned %d: %s", uiResp.StatusCode, body)
	}

	rawClaims, err := io.ReadAll(uiResp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read userinfo body: %w", err)
	}

	// Extract "sub" claim.
	var subClaim struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(rawClaims, &subClaim); err != nil || subClaim.Sub == "" {
		return "", nil, fmt.Errorf("userinfo missing sub claim")
	}

	return subClaim.Sub, json.RawMessage(rawClaims), nil
}

func buildAuthURL(p *Provider, state string) string {
	scopes := "openid email profile"
	if len(p.Scopes) > 0 {
		scopes = strings.Join(p.Scopes, " ")
	}
	params := url.Values{
		"client_id":     {p.ClientID},
		"response_type": {"code"},
		"scope":         {scopes},
		"state":         {state},
		"redirect_uri":  {callbackURL(state)},
	}
	return p.AuthURL + "?" + params.Encode()
}

// callbackURL builds the redirect_uri for the upstream IdP to return to.
// In production, this is the full authincore public URL.
func callbackURL(state string) string {
	return "/self-service/oidc/callback?state=" + url.QueryEscape(state)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
