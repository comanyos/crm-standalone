package auth

import (
	"context"
	"fmt"
	"strings"

	keyfunc "github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// jwksVerifier validates tokens against a remote JWKS (AuthinCore), with auto-refresh.
type jwksVerifier struct {
	kf       keyfunc.Keyfunc
	issuer   string
	audience string
}

func newFromJWKS(ctx context.Context, jwksURL, issuer, audience string) (*Authenticator, error) {
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("auth: load JWKS %q: %w", jwksURL, err)
	}
	return &Authenticator{v: &jwksVerifier{kf: kf, issuer: issuer, audience: audience}}, nil
}

func (v *jwksVerifier) verify(_ context.Context, tokenStr string) (*Principal, error) {
	opts := []jwt.ParserOption{jwt.WithValidMethods([]string{"RS256", "RS512", "ES256", "ES384"})}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	tok, err := jwt.Parse(tokenStr, v.kf.Keyfunc, opts...)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		return nil, fmt.Errorf("auth: invalid token claims")
	}
	return principalFromClaims(claims), nil
}

func principalFromClaims(c jwt.MapClaims) *Principal {
	orgID := strClaim(c, "org_id")
	tenantID := strClaim(c, "tenant_id")
	if tenantID == "" {
		// AuthinCore's canonical tenancy claim is org_id. The standalone CRM
		// uses that organization as its tenant boundary.
		tenantID = orgID
	}
	return &Principal{
		Subject:  strClaim(c, "sub"),
		TenantID: tenantID,
		OrgID:    orgID,
		Roles:    strsClaim(c, "roles"),
		Scopes:   scopesClaim(c),
	}
}

func strClaim(c jwt.MapClaims, key string) string {
	s, _ := c[key].(string)
	return s
}

// strsClaim reads a claim that may be a JSON array of strings.
func strsClaim(c jwt.MapClaims, key string) []string {
	raw, ok := c[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// scopesClaim accepts either "scopes" (array) or OAuth "scope" (space-delimited string).
func scopesClaim(c jwt.MapClaims) []string {
	if s := strsClaim(c, "scopes"); len(s) > 0 {
		return s
	}
	if s, ok := c["scope"].(string); ok && s != "" {
		return strings.Fields(s)
	}
	return nil
}
