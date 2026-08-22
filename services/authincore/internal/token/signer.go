package token

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// RS256Signer signs and verifies JWTs using RSA-SHA256.
type RS256Signer struct {
	privateKey *rsa.PrivateKey
	keyID      string
	createdAt  time.Time
}

// NewRS256Signer generates (or accepts) an RSA-2048 private key.
func NewRS256Signer(privateKey *rsa.PrivateKey, keyID string) (*RS256Signer, error) {
	if privateKey == nil {
		var err error
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, fmt.Errorf("generate rsa key: %w", err)
		}
	}
	if keyID == "" {
		keyID = "default"
	}
	return &RS256Signer{privateKey: privateKey, keyID: keyID, createdAt: time.Now().UTC()}, nil
}

type jwtClaims struct {
	jwt.RegisteredClaims
	SessionID   string   `json:"sid,omitempty"`
	OrgID       string   `json:"org_id,omitempty"`
	Email       string   `json:"email,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	AAL         int      `json:"aal"`
	AuthMethods []string `json:"amr,omitempty"`
}

func (s *RS256Signer) Sign(c Claims) (string, error) {
	claims := jwtClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   c.Subject,
			Issuer:    c.Issuer,
			Audience:  jwt.ClaimStrings(c.Audience),
			IssuedAt:  jwt.NewNumericDate(c.IssuedAt),
			ExpiresAt: jwt.NewNumericDate(c.ExpiresAt),
			ID:        c.JWTID,
		},
		SessionID:   c.SessionID,
		OrgID:       c.OrgID,
		Email:       c.Email,
		Roles:       c.Roles,
		Scopes:      c.Scopes,
		Scope:       c.Scope,
		AAL:         c.AAL,
		AuthMethods: c.AuthMethods,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = s.keyID
	signed, err := t.SignedString(s.privateKey)
	if err != nil {
		return "", fmt.Errorf("jwt sign: %w", err)
	}
	return signed, nil
}

func (s *RS256Signer) Verify(tokenString string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(tokenString, &jwtClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return &s.privateKey.PublicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("jwt verify: %w", err)
	}
	jc, ok := t.Claims.(*jwtClaims)
	if !ok || !t.Valid {
		return nil, fmt.Errorf("jwt invalid claims")
	}
	c := &Claims{
		Subject:     jc.Subject,
		Issuer:      jc.Issuer,
		Audience:    []string(jc.Audience),
		JWTID:       jc.ID,
		SessionID:   jc.SessionID,
		OrgID:       jc.OrgID,
		Email:       jc.Email,
		Roles:       jc.Roles,
		Scopes:      jc.Scopes,
		Scope:       jc.Scope,
		AAL:         jc.AAL,
		AuthMethods: jc.AuthMethods,
	}
	if jc.IssuedAt != nil {
		c.IssuedAt = jc.IssuedAt.Time
	}
	if jc.ExpiresAt != nil {
		c.ExpiresAt = jc.ExpiresAt.Time
	}
	return c, nil
}

// JWKSDocument is the JSON Web Key Set served at /oauth2/keys.
type JWKSDocument struct {
	Keys []JWK `json:"keys"`
}

// JWK is a single JSON Web Key entry (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS returns the public key set for token verification by clients.
func (s *RS256Signer) JWKS() JWKSDocument {
	pub := &s.privateKey.PublicKey
	return JWKSDocument{
		Keys: []JWK{{
			Kty: "RSA",
			Use: "sig",
			Kid: s.keyID,
			Alg: "RS256",
			N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
}

// KeyID returns the key identifier.
func (s *RS256Signer) KeyID() string { return s.keyID }

// RotatedAt returns when this key was created (used as rotation timestamp).
func (s *RS256Signer) RotatedAt() time.Time { return s.createdAt }
