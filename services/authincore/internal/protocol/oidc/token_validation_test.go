package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/authincore/authincore/internal/token"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestSigner(t *testing.T) *token.RS256Signer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "generate RSA key")
	s, err := token.NewRS256Signer(priv, "test-kid")
	require.NoError(t, err)
	return s
}

func newTestClaims(issuer string, audience []string, ttl time.Duration) token.Claims {
	now := time.Now().UTC()
	return token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    issuer,
		Audience:  audience,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		JWTID:     uuid.New().String(),
		SessionID: uuid.New().String(),
		OrgID:     uuid.New().String(),
		Email:     "alice@example.com",
		AAL:       1,
	}
}

// tamperPayload replaces the JWT payload segment with a base64url-encoded
// modified payload, keeping the original header and signature unchanged.
// This proves the signer rejects structurally valid but re-encoded tokens.
func tamperPayload(t *testing.T, tokenStr string, mutate func(map[string]any)) string {
	t.Helper()
	parts := strings.Split(tokenStr, ".")
	require.Len(t, parts, 3, "JWT must have 3 segments")

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var claims map[string]any
	require.NoError(t, json.Unmarshal(raw, &claims))
	mutate(claims)

	modified, err := json.Marshal(claims)
	require.NoError(t, err)

	parts[1] = base64.RawURLEncoding.EncodeToString(modified)
	return strings.Join(parts, ".")
}

// ---------------------------------------------------------------------------
// RS256Signer.Verify — white-box tests (same package)
// ---------------------------------------------------------------------------

func TestTokenValidation_ValidToken(t *testing.T) {
	signer := newTestSigner(t)
	claims := newTestClaims("https://auth.example.com", []string{"https://auth.example.com"}, time.Hour)

	tokenStr, err := signer.Sign(claims)
	require.NoError(t, err)

	got, err := signer.Verify(tokenStr)
	require.NoError(t, err)
	assert.Equal(t, claims.Subject, got.Subject)
	assert.Equal(t, claims.Issuer, got.Issuer)
	assert.Equal(t, claims.Email, got.Email)
	assert.Equal(t, claims.SessionID, got.SessionID)
	assert.Equal(t, claims.OrgID, got.OrgID)
}

func TestTokenValidation_ExpiredToken(t *testing.T) {
	signer := newTestSigner(t)
	claims := newTestClaims("https://auth.example.com", []string{"https://auth.example.com"}, -1*time.Second)

	tokenStr, err := signer.Sign(claims)
	require.NoError(t, err)

	_, err = signer.Verify(tokenStr)
	require.Error(t, err, "expired token must not verify")
}

func TestTokenValidation_TamperedPayload(t *testing.T) {
	signer := newTestSigner(t)
	claims := newTestClaims("https://auth.example.com", []string{"https://auth.example.com"}, time.Hour)

	tokenStr, err := signer.Sign(claims)
	require.NoError(t, err)

	tampered := tamperPayload(t, tokenStr, func(c map[string]any) {
		c["sub"] = "evil-actor"
	})

	_, err = signer.Verify(tampered)
	require.Error(t, err, "tampered token must not verify")
}

func TestTokenValidation_WrongSigningKey(t *testing.T) {
	signer1 := newTestSigner(t)
	signer2 := newTestSigner(t) // different private key

	claims := newTestClaims("https://auth.example.com", []string{"https://auth.example.com"}, time.Hour)
	tokenStr, err := signer1.Sign(claims)
	require.NoError(t, err)

	_, err = signer2.Verify(tokenStr)
	require.Error(t, err, "token signed by a different key must not verify")
}

func TestTokenValidation_UnsupportedAlgorithm(t *testing.T) {
	signer := newTestSigner(t)

	// Craft an HS256-signed token — algorithm confusion attack.
	hmacToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "attacker",
		"iss": "https://auth.example.com",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, err := hmacToken.SignedString([]byte("hmac-secret"))
	require.NoError(t, err)

	_, err = signer.Verify(tokenStr)
	require.Error(t, err, "token with non-RSA algorithm must be rejected")
}

func TestTokenValidation_EmptyTokenString(t *testing.T) {
	signer := newTestSigner(t)
	_, err := signer.Verify("")
	require.Error(t, err)
}

func TestTokenValidation_MalformedToken(t *testing.T) {
	signer := newTestSigner(t)
	tests := []struct{ name, tok string }{
		{"random string", "not.a.jwt"},
		{"only header", "eyJhbGciOiJSUzI1NiJ9"},
		{"two parts only", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := signer.Verify(tc.tok)
			assert.Error(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// token.Service integration — MintAccessToken + Verify
// ---------------------------------------------------------------------------

func TestTokenService_MintAndVerify(t *testing.T) {
	t.Parallel()

	signer := newTestSigner(t)
	const issuer = "https://auth.example.com"
	svc := token.NewService(signer, issuer, time.Hour, 24*time.Hour)

	identityID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()

	tokenStr, err := svc.MintAccessToken(context.Background(), identityID, orgID, sessionID,
		[]string{"admin"}, nil, "alice@example.com", 1)
	require.NoError(t, err)

	got, err := svc.Verify(tokenStr)
	require.NoError(t, err)
	assert.Equal(t, identityID.String(), got.Subject)
	assert.Equal(t, orgID.String(), got.OrgID)
	assert.Equal(t, sessionID.String(), got.SessionID)
	assert.Equal(t, issuer, got.Issuer)
	assert.Equal(t, "alice@example.com", got.Email)
	assert.Equal(t, 1, got.AAL)
}

func TestTokenService_ExpiredAccessToken(t *testing.T) {
	signer := newTestSigner(t)
	svc := token.NewService(signer, "https://auth.example.com", -time.Second, 24*time.Hour)

	tokenStr, err := svc.MintAccessToken(context.Background(), uuid.New(), uuid.New(), uuid.New(), nil, nil, "", 1)
	require.NoError(t, err)

	_, err = svc.Verify(tokenStr)
	require.Error(t, err, "expired access token must not verify")
}

func TestTokenService_MintRefreshToken_IsRandom(t *testing.T) {
	signer := newTestSigner(t)
	svc := token.NewService(signer, "https://auth.example.com", time.Hour, 24*time.Hour)

	t1, exp1, err := svc.MintRefreshToken(context.Background())
	require.NoError(t, err)
	t2, exp2, err := svc.MintRefreshToken(context.Background())
	require.NoError(t, err)

	assert.NotEqual(t, t1, t2, "each refresh token must be unique")
	assert.WithinDuration(t, exp1, exp2, 2*time.Second, "expiry times should be close")
}

func TestTokenService_PersonalAccessToken_Prefix(t *testing.T) {
	signer := newTestSigner(t)
	svc := token.NewService(signer, "https://auth.example.com", time.Hour, 24*time.Hour)

	pat, err := svc.MintPersonalAccessToken(context.Background())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(pat, "pat_"), "PAT must have 'pat_' prefix, got: %s", pat)
}
