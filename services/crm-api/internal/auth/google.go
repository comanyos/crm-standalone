package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MicahParks/jwkset"
	keyfunc "github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const (
	googleAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint         = "https://oauth2.googleapis.com/token" // #nosec G101 -- Public Google OAuth endpoint, not a credential.
	googleJWKSURL               = "https://www.googleapis.com/oauth2/v3/certs"
)

type googleOIDC struct {
	clientID     string
	clientSecret string
	redirectURL  string
	http         *http.Client
	keys         keyfunc.Keyfunc
	cancel       context.CancelFunc
}

func newGoogleOIDC(clientID, clientSecret, redirectURL string) (*googleOIDC, error) {
	redirect, err := url.Parse(redirectURL)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" {
		return nil, errors.New("auth: invalid AUTH_GOOGLE_REDIRECT_URL")
	}
	lifetimeCtx, cancel := context.WithCancel(context.Background())
	httpClient := &http.Client{Timeout: 15 * time.Second}
	remote, err := jwkset.NewStorageFromHTTP(googleJWKSURL, jwkset.HTTPClientStorageOptions{
		Ctx: lifetimeCtx, Client: httpClient, HTTPTimeout: 15 * time.Second,
		NoErrorReturnFirstHTTPReq: true, RefreshInterval: time.Hour,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("auth: initialize Google signing keys: %w", err)
	}
	storage, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs: map[string]jwkset.Storage{googleJWKSURL: remote},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("auth: initialize Google key client: %w", err)
	}
	keys, err := keyfunc.New(keyfunc.Options{Storage: storage})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("auth: initialize Google verifier: %w", err)
	}
	return &googleOIDC{clientID: clientID, clientSecret: clientSecret,
		redirectURL: redirectURL, http: httpClient, keys: keys, cancel: cancel}, nil
}

func (g *googleOIDC) Close() { g.cancel() }

func (g *googleOIDC) AuthorizationURL(state, nonce, codeChallenge string) string {
	query := url.Values{
		"client_id":             {g.clientID},
		"redirect_uri":          {g.redirectURL},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return googleAuthorizationEndpoint + "?" + query.Encode()
}

func (g *googleOIDC) Exchange(ctx context.Context, code, verifier string) (externalIdentity, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"redirect_uri":  {g.redirectURL},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return externalIdentity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := g.http.Do(req)
	if err != nil {
		return externalIdentity{}, fmt.Errorf("auth: exchange Google authorization: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return externalIdentity{}, errors.New("auth: read Google token response")
	}
	if response.StatusCode != http.StatusOK {
		return externalIdentity{}, fmt.Errorf("auth: Google token endpoint returned %d", response.StatusCode)
	}
	var tokenResponse struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil || tokenResponse.IDToken == "" {
		return externalIdentity{}, errors.New("auth: Google token response has no ID token")
	}
	return g.verifyIDToken(tokenResponse.IDToken)
}

func (g *googleOIDC) verifyIDToken(raw string) (externalIdentity, error) {
	token, err := jwt.Parse(raw, g.keys.Keyfunc,
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithExpirationRequired(), jwt.WithAudience(g.clientID))
	if err != nil || !token.Valid {
		return externalIdentity{}, errors.New("auth: invalid Google ID token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return externalIdentity{}, errors.New("auth: invalid Google claims")
	}
	issuer, _ := claims["iss"].(string)
	if issuer != "https://accounts.google.com" && issuer != "accounts.google.com" {
		return externalIdentity{}, errors.New("auth: invalid Google issuer")
	}
	verified := false
	switch value := claims["email_verified"].(type) {
	case bool:
		verified = value
	case string:
		verified, _ = strconv.ParseBool(value)
	}
	identity := externalIdentity{
		Subject: googleStringClaim(claims, "sub"), Email: googleStringClaim(claims, "email"), EmailVerified: verified,
		FirstName: googleStringClaim(claims, "given_name"), LastName: googleStringClaim(claims, "family_name"),
		Nonce: googleStringClaim(claims, "nonce"),
	}
	if identity.Subject == "" || identity.Email == "" || !identity.EmailVerified {
		return externalIdentity{}, errors.New("auth: incomplete Google identity")
	}
	return identity, nil
}

func googleStringClaim(claims jwt.MapClaims, name string) string {
	value, _ := claims[name].(string)
	return value
}
