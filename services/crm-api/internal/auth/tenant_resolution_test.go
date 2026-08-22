package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Tenant-resolution proof (task.md §1.3).
//
// This service has exactly one way to learn which organization a request
// belongs to: the org_id claim of a JWT it has verified against AuthinCore's
// JWKS. There is deliberately no host-domain, subdomain, header, or body
// path to a tenant — so the tests below are written as *negative* proofs
// that no such path exists, rather than as coverage of a resolver that does.
//
// That matters because host-based resolution is the usual place a
// multi-tenant service springs a leak: a request whose Host says one
// organization and whose token says another has to resolve to exactly one of
// them, and any rule other than "the token wins" is exploitable by anyone who
// can set a header.

// authWithJWKS returns an Authenticator verifying against a freshly served
// JWKS, plus the signing key.
func authWithJWKS(t *testing.T, issuer, audience string) (*Authenticator, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a, err := FromConfig(context.Background(), jwksServer(t, &key.PublicKey), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	return a, key
}

// tokenFor mints a valid token for one organization.
func tokenFor(t *testing.T, key *rsa.PrivateKey, org string) string {
	t.Helper()
	now := time.Now()
	return sign(t, key, jwt.MapClaims{
		"sub": "user-in-" + org, "iss": "authincore", "org_id": org,
		"scopes": []any{"crm:account:read"},
		"iat":    now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
}

// interceptWithHeaders runs the interceptor with an arbitrary header set and
// returns the principal the handler saw.
func interceptWithHeaders(t *testing.T, a *Authenticator, headers map[string]string) (*Principal, error) {
	t.Helper()
	var seen *Principal
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		if p, ok := FromContext(ctx); ok {
			seen = p
		}
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	req := connect.NewRequest(&emptypb.Empty{})
	for k, v := range headers {
		req.Header().Set(k, v)
	}
	_, err := a.Interceptor(nil)(next)(context.Background(), req)
	return seen, err
}

// TestTenantComesFromTheTokenAlone runs the same request shape with two
// different tokens and confirms the resolved organization tracks the token.
func TestTenantComesFromTheTokenAlone(t *testing.T) {
	a, key := authWithJWKS(t, "authincore", "")

	for _, org := range []string{"org-alpha", "org-beta"} {
		p, err := interceptWithHeaders(t, a, map[string]string{
			"Authorization": "Bearer " + tokenFor(t, key, org),
		})
		if err != nil {
			t.Fatalf("[%s] intercept: %v", org, err)
		}
		if p.OrgID != org || p.TenantID != org {
			t.Errorf("[%s] principal = {org:%q tenant:%q}, want both %q", org, p.OrgID, p.TenantID, org)
		}
	}
}

// TestHostAndHeadersCannotChangeTheResolvedTenant is the token-org/host
// mismatch case. Every header below is a plausible tenant hint that some
// multi-tenant services honour; this one must ignore all of them.
func TestHostAndHeadersCannotChangeTheResolvedTenant(t *testing.T) {
	a, key := authWithJWKS(t, "authincore", "")
	token := tokenFor(t, key, "org-alpha")

	for name, spoof := range map[string]map[string]string{
		"host header":       {"Host": "org-beta.crm.example.com"},
		"x-forwarded-host":  {"X-Forwarded-Host": "org-beta.crm.example.com"},
		"origin":            {"Origin": "https://org-beta.crm.example.com"},
		"explicit org id":   {"X-Org-Id": "org-beta"},
		"explicit tenant":   {"X-Tenant-Id": "org-beta"},
		"all of them":       {"Host": "org-beta.crm.example.com", "X-Org-Id": "org-beta", "X-Tenant-Id": "org-beta"},
		"empty tenant hint": {"X-Tenant-Id": ""},
	} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{"Authorization": "Bearer " + token}
			for k, v := range spoof {
				headers[k] = v
			}
			p, err := interceptWithHeaders(t, a, headers)
			if err != nil {
				t.Fatalf("intercept: %v", err)
			}
			if p.TenantID != "org-alpha" {
				t.Errorf("tenant resolved to %q with %s set; want org-alpha — "+
					"a caller who can set a header can choose an organization", p.TenantID, name)
			}
		})
	}
}

// TestTokenWithoutAnOrganizationYieldsNoTenant covers the claim shape that
// must not be allowed to become a default or shared tenant. An empty
// TenantID is refused downstream by api.scopeFrom and domain.Scope.Valid.
func TestTokenWithoutAnOrganizationYieldsNoTenant(t *testing.T) {
	for name, claims := range map[string]jwt.MapClaims{
		"no claims at all":  {"sub": "u"},
		"empty org_id":      {"sub": "u", "org_id": ""},
		"org_id wrong type": {"sub": "u", "org_id": 42},
		"org_id is null":    {"sub": "u", "org_id": nil},
	} {
		t.Run(name, func(t *testing.T) {
			p := principalFromClaims(claims)
			if p.TenantID != "" {
				t.Errorf("tenant = %q for a token carrying no organization; want empty", p.TenantID)
			}
		})
	}
}

// TestUnknownOrganizationGetsItsOwnEmptyTenant records the deliberate
// behaviour for an organization this service has never seen: it is not an
// error, and it is not a fallback to any shared or default tenant. The CRM
// keeps no organization registry of its own — AuthinCore owns that — so an
// unrecognised org_id simply names a tenant with no rows in it.
func TestUnknownOrganizationGetsItsOwnEmptyTenant(t *testing.T) {
	p := principalFromClaims(jwt.MapClaims{"sub": "u", "org_id": "org-never-seen-before"})
	if p.TenantID != "org-never-seen-before" {
		t.Errorf("tenant = %q, want the org id itself", p.TenantID)
	}
	for _, forbidden := range []string{"", "default", "public", "*"} {
		if p.TenantID == forbidden {
			t.Errorf("an unknown organization resolved to %q — a shared fallback tenant", forbidden)
		}
	}
}

// TestDeactivatedOrganizationIsBoundedByTokenExpiry documents the one
// revocation signal this service has.
//
// The CRM does not call AuthinCore on each request, so deactivating an
// organization (or a user) does not invalidate an access token that is
// already in a client's hands: the token stays good until it expires, and
// AuthinCore's refusal to refresh it is what ends the session. The window is
// therefore exactly the access-token lifetime. What must hold is that the
// window is bounded at all — an expired token is refused, and so is one that
// tries to avoid the question by carrying no expiry.
func TestDeactivatedOrganizationIsBoundedByTokenExpiry(t *testing.T) {
	a, key := authWithJWKS(t, "authincore", "")
	now := time.Now()

	expired := sign(t, key, jwt.MapClaims{
		"sub": "u", "iss": "authincore", "org_id": "org-deactivated",
		"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix(),
	})
	if _, err := a.v.verify(context.Background(), expired); err == nil {
		t.Error("a token from a deactivated organization outlived its expiry")
	}

	// A token with no exp claim never expires, which would make the
	// deactivation window unbounded.
	noExpiry := sign(t, key, jwt.MapClaims{
		"sub": "u", "iss": "authincore", "org_id": "org-deactivated",
		"iat": now.Unix(),
	})
	if _, err := a.v.verify(context.Background(), noExpiry); err == nil {
		t.Error("a token carrying no exp claim was accepted — deactivating an " +
			"organization would never take effect for a client holding one")
	}
}

// TestTokensFromAnotherIssuerOrAudienceAreRefused closes the other way a
// caller could name an organization of their choosing: signing their own
// token, or replaying one minted for a different service.
func TestTokensFromAnotherIssuerOrAudienceAreRefused(t *testing.T) {
	a, key := authWithJWKS(t, "authincore", "crm")
	now := time.Now()
	valid := jwt.MapClaims{
		"sub": "u", "iss": "authincore", "aud": "crm", "org_id": "org-alpha",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}

	if _, err := a.v.verify(context.Background(), sign(t, key, valid)); err != nil {
		t.Fatalf("a well-formed token was refused: %v", err)
	}

	wrongIssuer := jwt.MapClaims{}
	wrongAudience := jwt.MapClaims{}
	for k, v := range valid {
		wrongIssuer[k], wrongAudience[k] = v, v
	}
	wrongIssuer["iss"] = "https://attacker.example"
	wrongAudience["aud"] = "some-other-service"

	if _, err := a.v.verify(context.Background(), sign(t, key, wrongIssuer)); err == nil {
		t.Error("a token from another issuer was accepted")
	}
	if _, err := a.v.verify(context.Background(), sign(t, key, wrongAudience)); err == nil {
		t.Error("a token minted for another audience was accepted")
	}

	// Signed by a key that is not in the JWKS at all.
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.v.verify(context.Background(), sign(t, otherKey, valid)); err == nil {
		t.Error("a token signed by an unknown key was accepted")
	}
}

// TestTenantClaimPrecedence records which claim wins when both are present.
// AuthinCore issues org_id; tenant_id is honoured first for compatibility, so
// this is written down rather than left to be rediscovered.
func TestTenantClaimPrecedence(t *testing.T) {
	p := principalFromClaims(jwt.MapClaims{"sub": "u", "org_id": "org-1", "tenant_id": "tenant-1"})
	if p.TenantID != "tenant-1" {
		t.Errorf("tenant = %q, want the explicit tenant_id claim to win", p.TenantID)
	}
	if p.OrgID != "org-1" {
		t.Errorf("org = %q, want org-1", p.OrgID)
	}

	// With only org_id — AuthinCore's actual shape — the org is the tenant.
	p = principalFromClaims(jwt.MapClaims{"sub": "u", "org_id": "org-1"})
	if p.TenantID != "org-1" {
		t.Errorf("tenant = %q, want org-1", p.TenantID)
	}
}
