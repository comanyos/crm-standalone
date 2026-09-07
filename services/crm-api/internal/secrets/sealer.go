// Package secrets seals small values at rest with AES-256-GCM.
//
// It exists because lead capture (docs/LEAD_CAPTURE.md) is the second feature
// in this service to store a credential it must be able to read back, and the
// first one's sealing lives inside internal/integration/googleoauth's client,
// bound to Google's own configuration. Reaching for that would have meant a
// lead-capture endpoint could not exist without a configured Google OAuth
// client, which is an unrelated dependency and the wrong shape.
//
// googleoauth is deliberately not refactored onto this. It holds working
// credential code covered by its own tests, and rewriting a sealing path to
// remove a duplicated forty lines is a poor trade against the chance of
// getting it subtly wrong. If a third caller appears, that is the moment.
//
// The associated-data discipline is the load-bearing part, and it is the same
// one googleoauth documents: every ciphertext is authenticated against a
// binding that names what it belongs to, so a row copied into another tenant
// fails to open rather than decrypting into a usable secret. Callers must
// build that binding through a shared function, never inline, or the property
// holds only until two call sites spell it differently.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Sealer encrypts and decrypts small values under one key.
type Sealer struct {
	aead cipher.AEAD
}

// New builds a Sealer from a base64-encoded 32-byte key.
//
// The key is required and must be exactly 32 bytes: accepting a short one
// would silently weaken every ciphertext it produces, and accepting an absent
// one would mean a deployment that stores secrets in the clear because
// somebody forgot an environment variable.
func New(encodedKey string) (*Sealer, error) {
	key, err := base64.RawStdEncoding.DecodeString(encodedKey)
	if err != nil {
		key, err = base64.StdEncoding.DecodeString(encodedKey)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("secrets: key must be base64-encoded 32 random bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: create GCM: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext, authenticating it against associatedData.
//
// The nonce is prepended to the ciphertext rather than stored separately:
// they are useless apart, and a schema that keeps them in two columns is a
// schema where one can be updated without the other.
func (s *Sealer) Seal(plaintext, associatedData []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, associatedData), nil
}

// Open decrypts a value sealed with the same key and associated data.
//
// A ciphertext that does not open is not distinguished from one that is
// merely absent or malformed: the caller cannot act differently on the
// difference, and reporting it would say whether a given binding was the
// right one.
func (s *Sealer) Open(ciphertext, associatedData []byte) ([]byte, error) {
	nonceSize := s.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("secrets: ciphertext is too short to contain a nonce")
	}
	plaintext, err := s.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], associatedData)
	if err != nil {
		return nil, errors.New("secrets: could not open sealed value")
	}
	return plaintext, nil
}
