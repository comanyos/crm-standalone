// Package token handles JWT minting and verification for sessions and API tokens.
package token

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Claims represents the standard + authincore-specific JWT claims.
type Claims struct {
	// Standard
	Subject   string    `json:"sub"`
	Issuer    string    `json:"iss"`
	Audience  []string  `json:"aud"`
	IssuedAt  time.Time `json:"iat"`
	ExpiresAt time.Time `json:"exp"`
	JWTID     string    `json:"jti"`
	// authincore
	SessionID   string   `json:"sid"`
	OrgID       string   `json:"org_id"`
	Email       string   `json:"email,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	AAL         int      `json:"aal"`
	AuthMethods []string `json:"amr,omitempty"`
}

// Signer signs and verifies tokens.
type Signer interface {
	Sign(claims Claims) (string, error)
	Verify(tokenString string) (*Claims, error)
}

// Service mints access/refresh/session tokens.
type Service struct {
	signer     Signer
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewService(signer Signer, issuer string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		signer:     signer,
		issuer:     issuer,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// MintAccessToken creates a short-lived JWT access token.
func (s *Service) MintAccessToken(_ context.Context, identityID, orgID, sessionID uuid.UUID, roles []string, scopes []string, email string, aal int) (string, error) {
	now := time.Now().UTC()
	claims := Claims{
		Subject:   identityID.String(),
		Issuer:    s.issuer,
		Audience:  []string{s.issuer},
		IssuedAt:  now,
		ExpiresAt: now.Add(s.accessTTL),
		JWTID:     uuid.New().String(),
		SessionID: sessionID.String(),
		OrgID:     orgID.String(),
		Email:     email,
		Roles:     roles,
		Scopes:    scopes,
		Scope:     strings.Join(scopes, " "),
		AAL:       aal,
	}
	return s.signer.Sign(claims)
}

// MintRefreshToken generates a cryptographically random opaque refresh token.
func (s *Service) MintRefreshToken(_ context.Context) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("mint refresh token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(b)
	expiresAt := time.Now().UTC().Add(s.refreshTTL)
	return token, expiresAt, nil
}

// MintPersonalAccessToken generates an opaque long-lived PAT for machine auth.
func (s *Service) MintPersonalAccessToken(_ context.Context) (string, error) {
	b := make([]byte, 40)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint pat: %w", err)
	}
	return "pat_" + base64.URLEncoding.EncodeToString(b), nil
}

func (s *Service) Verify(tokenString string) (*Claims, error) {
	claims, err := s.signer.Verify(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.Issuer != s.issuer {
		return nil, fmt.Errorf("token issuer mismatch")
	}
	validAudience := false
	for _, audience := range claims.Audience {
		if audience == s.issuer {
			validAudience = true
			break
		}
	}
	if !validAudience {
		return nil, fmt.Errorf("token audience mismatch")
	}
	return claims, nil
}
