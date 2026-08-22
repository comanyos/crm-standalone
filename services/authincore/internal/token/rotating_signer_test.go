package token_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"testing"
	"time"

	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
)

func TestRotatingSigner_RoundTrip(t *testing.T) {
	rs, err := token.NewRotatingSignerFromPEM("", "key-1")
	if err != nil {
		t.Fatalf("NewRotatingSignerFromPEM: %v", err)
	}
	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://test",
		Audience:  []string{"https://test"},
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		JWTID:     uuid.New().String(),
	}
	signed, err := rs.Sign(claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := rs.Verify(signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Errorf("subject mismatch: got %q want %q", got.Subject, claims.Subject)
	}
}

func TestRotatingSigner_TokenValidAfterRotation(t *testing.T) {
	rs, err := token.NewRotatingSignerFromPEM("", "key-1")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://test",
		Audience:  []string{"https://test"},
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		JWTID:     uuid.New().String(),
	}
	// Sign with key-1.
	signed, _ := rs.Sign(claims)

	// Rotate to key-2.
	if err := rs.Rotate("key-2"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Token signed with key-1 must still verify (previous key window).
	got, err := rs.Verify(signed)
	if err != nil {
		t.Fatalf("Verify after rotation: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Errorf("subject mismatch after rotation")
	}
}

func TestRotatingSigner_TokenSignedWithNewKeyAfterRotation(t *testing.T) {
	rs, _ := token.NewRotatingSignerFromPEM("", "key-1")
	_ = rs.Rotate("key-2")

	claims := token.Claims{
		Subject:   uuid.New().String(),
		Issuer:    "https://test",
		Audience:  []string{"https://test"},
		IssuedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		JWTID:     uuid.New().String(),
	}
	signed, err := rs.Sign(claims)
	if err != nil {
		t.Fatalf("Sign with new key: %v", err)
	}
	got, err := rs.Verify(signed)
	if err != nil {
		t.Fatalf("Verify with new key: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Errorf("subject mismatch")
	}
}

func TestRotatingSigner_RotatedAt(t *testing.T) {
	rs, _ := token.NewRotatingSignerFromPEM("", "key-1")
	if !rs.RotatedAt().IsZero() {
		t.Error("RotatedAt should be zero before first rotation")
	}
	before := time.Now()
	_ = rs.Rotate("key-2")
	if rs.RotatedAt().Before(before) {
		t.Error("RotatedAt should be updated after rotation")
	}
}

func TestRotatingSigner_JWKS_HasTwoKeysAfterRotation(t *testing.T) {
	rs, _ := token.NewRotatingSignerFromPEM("", "key-1")
	_ = rs.Rotate("key-2")
	jwks := rs.JWKS()
	if len(jwks.Keys) != 2 {
		t.Errorf("expected 2 keys in JWKS after rotation, got %d", len(jwks.Keys))
	}
}

func TestRotatingSigner_LoadsSharedActiveAndPreviousKeyRing(t *testing.T) {
	active := testPrivateKeyPEM(t)
	previous := testPrivateKeyPEM(t)
	ring, err := json.Marshal(map[string]string{"active": active, "previous": previous})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSA_PRIVATE_KEYS_JSON", string(ring))
	t.Setenv("JWT_KEY_ID", "active")
	t.Setenv("JWT_PREVIOUS_KEY_ID", "previous")
	signer, err := token.NewRotatingSignerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if signer.KeyID() != "active" || len(signer.JWKS().Keys) != 2 {
		t.Fatalf("expected shared active+previous keys, kid=%q jwks=%d", signer.KeyID(), len(signer.JWKS().Keys))
	}
}

func TestRotatingSigner_ProductionRejectsEphemeralKey(t *testing.T) {
	t.Setenv("RSA_PRIVATE_KEYS_JSON", "")
	t.Setenv("RSA_PRIVATE_KEY_PEM", "")
	t.Setenv("RSA_PRIVATE_KEY_FILE", "")
	t.Setenv("REQUIRE_PERSISTENT_SIGNING_KEY", "true")
	if _, err := token.NewRotatingSignerFromEnv(); err == nil {
		t.Fatal("expected production mode to reject an ephemeral signing key")
	}
}

func TestRotatingSigner_ProductionAcceptsFileMountedKey(t *testing.T) {
	t.Setenv("RSA_PRIVATE_KEYS_JSON", "")
	t.Setenv("RSA_PRIVATE_KEY_PEM", "")
	t.Setenv("REQUIRE_PERSISTENT_SIGNING_KEY", "true")
	path := t.TempDir() + "/signing.pem"
	if err := os.WriteFile(path, []byte(testPrivateKeyPEM(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSA_PRIVATE_KEY_FILE", path)
	signer, err := token.NewRotatingSignerFromEnv()
	if err != nil {
		t.Fatalf("file-mounted production key rejected: %v", err)
	}
	if signer.KeyID() != "k1" {
		t.Fatalf("unexpected key id %q", signer.KeyID())
	}
}

func testPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}
