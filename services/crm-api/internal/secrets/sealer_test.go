package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestSealRoundTrips(t *testing.T) {
	s, err := New(testKey(t))
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	ad := []byte("lead-capture:tenant-a:endpoint-1")
	secret := []byte("a signing secret")

	sealed, err := s.Seal(secret, ad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// The plaintext must not be recoverable by reading the column.
	if string(sealed) == string(secret) {
		t.Fatal("the sealed value is the plaintext")
	}

	opened, err := s.Open(sealed, ad)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != string(secret) {
		t.Errorf("opened %q, want %q", opened, secret)
	}
}

// TestSealIsBoundToItsAssociatedData is the property the whole scheme rests
// on: a row copied into another tenant must fail to open rather than
// decrypting into a usable secret.
func TestSealIsBoundToItsAssociatedData(t *testing.T) {
	s, err := New(testKey(t))
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	sealed, err := s.Seal([]byte("secret"), []byte("lead-capture:tenant-a:endpoint-1"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	for _, wrong := range []string{
		"lead-capture:tenant-b:endpoint-1",      // another tenant
		"lead-capture:tenant-a:endpoint-2",      // another endpoint
		"google-connection:tenant-a:endpoint-1", // another purpose
		"",
	} {
		if _, err := s.Open(sealed, []byte(wrong)); err == nil {
			t.Errorf("a ciphertext opened under the wrong binding %q", wrong)
		}
	}
}

func TestSealIsBoundToItsKey(t *testing.T) {
	a, err := New(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	ad := []byte("binding")
	sealed, err := a.Seal([]byte("secret"), ad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(sealed, ad); err == nil {
		t.Error("a ciphertext opened under a different key")
	}
}

func TestNewRejectsAWeakKey(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	for _, bad := range []string{"", "not base64!", short} {
		if _, err := New(bad); err == nil {
			t.Errorf("accepted key %q; a short or absent key silently weakens every ciphertext", bad)
		}
	}
}

func TestOpenRejectsTruncatedCiphertext(t *testing.T) {
	s, err := New(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open([]byte("short"), []byte("binding")); err == nil {
		t.Error("opened a value too short to contain a nonce")
	}
}
