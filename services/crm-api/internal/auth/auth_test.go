package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/protobuf/types/known/emptypb"
)

const testKID = "test-key"

// jwksServer serves a JWKS containing the public half of key, and returns its URL.
func jwksServer(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	eBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(eBuf, uint32(pub.E))
	// trim leading zero bytes of the exponent
	i := 0
	for i < len(eBuf)-1 && eBuf[i] == 0 {
		i++
	}
	jwks := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(eBuf[i:]),
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sign(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKID
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWKSVerify(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	url := jwksServer(t, &key.PublicKey)
	a, err := FromConfig(context.Background(), url, "authincore", "")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	good := sign(t, key, jwt.MapClaims{
		"sub": "u1", "iss": "authincore", "tenant_id": "t1", "org_id": "o1",
		"roles": []any{"ADMIN"}, "scopes": []any{"crm:query:read"},
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})

	p, err := a.v.verify(context.Background(), good)
	if err != nil {
		t.Fatalf("verify good token: %v", err)
	}
	if p.Subject != "u1" || p.TenantID != "t1" || p.OrgID != "o1" {
		t.Errorf("claims = %+v", p)
	}
	if !p.HasScope("crm:query:read") {
		t.Errorf("missing expected scope: %+v", p.Scopes)
	}

	// wrong issuer rejected
	bad := sign(t, key, jwt.MapClaims{"sub": "u", "iss": "evil", "exp": now.Add(time.Hour).Unix()})
	if _, err := a.v.verify(context.Background(), bad); err == nil {
		t.Error("expected wrong-issuer token to be rejected")
	}

	// expired rejected
	expired := sign(t, key, jwt.MapClaims{"sub": "u", "iss": "authincore", "exp": now.Add(-time.Hour).Unix()})
	if _, err := a.v.verify(context.Background(), expired); err == nil {
		t.Error("expected expired token to be rejected")
	}
}

func TestScopesClaimParsing(t *testing.T) {
	// OAuth-style space-delimited "scope"
	got := scopesClaim(jwt.MapClaims{"scope": "a b c"})
	if len(got) != 3 || got[0] != "a" {
		t.Errorf("scope string parse = %v", got)
	}
	// array "scopes" wins
	got = scopesClaim(jwt.MapClaims{"scopes": []any{"x", "y"}, "scope": "z"})
	if len(got) != 2 || got[1] != "y" {
		t.Errorf("scopes array parse = %v", got)
	}
}

func TestPrincipalUsesAuthinCoreOrgAsTenant(t *testing.T) {
	p := principalFromClaims(jwt.MapClaims{
		"sub": "user-1", "org_id": "org-1",
	})
	if p.OrgID != "org-1" || p.TenantID != "org-1" {
		t.Fatalf("principal = %+v, want org_id to define the CRM tenant", p)
	}
}

func TestHasScopeWildcard(t *testing.T) {
	p := &Principal{Scopes: []string{"crm:*"}}
	if !p.HasScope("crm:anything:write") {
		t.Error("wildcard should grant any dbcore scope")
	}
}

// stubVerifier is an in-package fake (verifier is unexported).
type stubVerifier struct {
	p   *Principal
	err error
}

func (s stubVerifier) verify(context.Context, string) (*Principal, error) { return s.p, s.err }

func callInterceptor(t *testing.T, a *Authenticator, policy map[string]string, authHeader string) (*Principal, error) {
	t.Helper()
	var seen *Principal
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		if p, ok := FromContext(ctx); ok {
			seen = p
		}
		return connect.NewResponse(&emptypb.Empty{}), nil
	}
	wrapped := a.Interceptor(policy)(next)
	req := connect.NewRequest(&emptypb.Empty{})
	if authHeader != "" {
		req.Header().Set("Authorization", authHeader)
	}
	_, err := wrapped(context.Background(), req)
	return seen, err
}

func TestInterceptorDisabledInjectsDevPrincipal(t *testing.T) {
	seen, err := callInterceptor(t, Disabled(), nil, "")
	if err != nil {
		t.Fatalf("disabled interceptor err: %v", err)
	}
	if seen == nil || seen.Subject != "dev" || !seen.HasScope("crm:anything") {
		t.Errorf("dev principal not injected: %+v", seen)
	}
}

func TestInterceptorMissingToken(t *testing.T) {
	a := &Authenticator{v: stubVerifier{}}
	if _, err := callInterceptor(t, a, nil, ""); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("missing token code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestInterceptorBadToken(t *testing.T) {
	a := &Authenticator{v: stubVerifier{err: errors.New("nope")}}
	if _, err := callInterceptor(t, a, nil, "Bearer x"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("bad token code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestInterceptorGoodTokenInjectsPrincipal(t *testing.T) {
	a := &Authenticator{v: stubVerifier{p: &Principal{Subject: "u1", Scopes: []string{"crm:*"}}}}
	seen, err := callInterceptor(t, a, nil, "Bearer x")
	if err != nil {
		t.Fatalf("good token err: %v", err)
	}
	if seen == nil || seen.Subject != "u1" {
		t.Errorf("principal not injected: %+v", seen)
	}
}

func TestWithPrincipalRoundTrip(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{Subject: "x"})
	p, ok := FromContext(ctx)
	if !ok || p.Subject != "x" {
		t.Errorf("round-trip = %+v ok=%v", p, ok)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Error("empty context should have no principal")
	}
}

func TestFromConfigEmptyJWKSDisablesAuth(t *testing.T) {
	a, err := FromConfig(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.disabled {
		t.Error("empty jwksURL should yield a disabled authenticator")
	}
}

func TestAuthenticateDisabledInjectsDev(t *testing.T) {
	ctx, err := Disabled().Authenticate(context.Background(), "")
	if err != nil {
		t.Fatalf("disabled Authenticate err: %v", err)
	}
	if p, ok := FromContext(ctx); !ok || p.Subject != "dev" {
		t.Errorf("dev principal not injected: %+v ok=%v", p, ok)
	}
}

func TestAuthenticateMissingAndBadToken(t *testing.T) {
	a := &Authenticator{v: stubVerifier{err: errors.New("nope")}}
	if _, err := a.Authenticate(context.Background(), ""); err == nil {
		t.Error("missing token should error")
	}
	if _, err := a.Authenticate(context.Background(), "Bearer x"); err == nil {
		t.Error("bad token should error")
	}
}

func TestAuthenticateGoodToken(t *testing.T) {
	a := &Authenticator{v: stubVerifier{p: &Principal{Subject: "u1"}}}
	ctx, err := a.Authenticate(context.Background(), "Bearer x")
	if err != nil {
		t.Fatalf("good token err: %v", err)
	}
	if p, ok := FromContext(ctx); !ok || p.Subject != "u1" {
		t.Errorf("principal not injected: %+v ok=%v", p, ok)
	}
}

func TestHasScopeNoMatch(t *testing.T) {
	p := &Principal{Scopes: []string{"crm:query:read"}}
	if p.HasScope("crm:connection:write") {
		t.Error("should not grant an unheld scope")
	}
}

func TestBearer(t *testing.T) {
	if got := bearer("Bearer abc"); got != "abc" {
		t.Errorf("bearer = %q", got)
	}
	if got := bearer("bearer xyz"); got != "xyz" {
		t.Errorf("case-insensitive bearer = %q", got)
	}
	if got := bearer("Basic abc"); got != "" {
		t.Errorf("non-bearer should be empty, got %q", got)
	}
}
