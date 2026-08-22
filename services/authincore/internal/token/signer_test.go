package token_test

import (
	"testing"
	"time"

	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

func TestRS256Signer_RoundTrip(t *testing.T) {
	signer, err := token.NewRS256Signer(nil, "test-key")
	if err != nil {
		t.Fatalf("NewRS256Signer: %v", err)
	}

	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://authincore.test",
		Audience:  []string{"https://authincore.test"},
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		JWTID:     uuid.New().String(),
		SessionID: uuid.New().String(),
		OrgID:     uuid.New().String(),
		Email:     "user@example.com",
		AAL:       1,
	}

	signed, err := signer.Sign(claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if signed == "" {
		t.Fatal("expected non-empty signed token")
	}

	got, err := signer.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Errorf("subject mismatch: got %q want %q", got.Subject, claims.Subject)
	}
	if got.Email != claims.Email {
		t.Errorf("email mismatch: got %q want %q", got.Email, claims.Email)
	}
	if got.OrgID != claims.OrgID {
		t.Errorf("org_id mismatch: got %q want %q", got.OrgID, claims.OrgID)
	}
	if got.AAL != claims.AAL {
		t.Errorf("aal mismatch: got %d want %d", got.AAL, claims.AAL)
	}
}

func TestRS256Signer_ExpiredToken(t *testing.T) {
	signer, _ := token.NewRS256Signer(nil, "test-key")

	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://authincore.test",
		Audience:  []string{"https://authincore.test"},
		IssuedAt:  time.Now().UTC().Add(-2 * time.Hour),
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour), // already expired
		JWTID:     uuid.New().String(),
	}
	signed, err := signer.Sign(claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, err = signer.Verify(signed)
	if err == nil {
		t.Error("expected error verifying expired token")
	}
}

func TestRS256Signer_TamperedToken(t *testing.T) {
	signer, _ := token.NewRS256Signer(nil, "test-key")

	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://authincore.test",
		Audience:  []string{"https://authincore.test"},
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		JWTID:     uuid.New().String(),
	}
	signed, _ := signer.Sign(claims)
	tampered := signed[:len(signed)-4] + "XXXX"

	_, err := signer.Verify(tampered)
	if err == nil {
		t.Error("expected error verifying tampered token")
	}
}

func TestRS256Signer_WrongSigningKey(t *testing.T) {
	signer1, _ := token.NewRS256Signer(nil, "key-1")
	signer2, _ := token.NewRS256Signer(nil, "key-2")

	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://authincore.test",
		Audience:  []string{"https://authincore.test"},
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		JWTID:     uuid.New().String(),
	}
	signed, _ := signer1.Sign(claims)

	_, err := signer2.Verify(signed)
	if err == nil {
		t.Error("expected error verifying token signed with different key")
	}
}

func TestRS256Signer_JWKS(t *testing.T) {
	signer, _ := token.NewRS256Signer(nil, "my-key")
	jwks := signer.JWKS()

	if len(jwks.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(jwks.Keys))
	}
	k := jwks.Keys[0]
	if k.Kty != "RSA" {
		t.Errorf("expected kty=RSA, got %q", k.Kty)
	}
	if k.Alg != "RS256" {
		t.Errorf("expected alg=RS256, got %q", k.Alg)
	}
	if k.Kid != "my-key" {
		t.Errorf("expected kid=my-key, got %q", k.Kid)
	}
	if k.N == "" || k.E == "" {
		t.Error("expected non-empty N and E in JWKS")
	}
}
