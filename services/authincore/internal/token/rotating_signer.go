// Package token provides JWT signing with hot key rotation.
// RotatingSigner wraps RS256Signer and supports:
//   - Loading a PEM-encoded private key from an env var or file path.
//   - Rotating to a new key while still verifying tokens signed by the previous key (2-key window).
package token

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// RotatingSigner holds the active + previous RS256 signer and rotates on a schedule.
// Verification tries the active key first, then falls back to the previous key so
// in-flight tokens remain valid during a rotation window.
type RotatingSigner struct {
	mu        sync.RWMutex
	active    *RS256Signer
	previous  *RS256Signer // nil until first rotation
	rotatedAt time.Time
}

// NewRotatingSignerFromPEM loads an RSA private key from a PEM-encoded string.
// If pemKey is empty, a fresh ephemeral key is generated (dev mode).
func NewRotatingSignerFromPEM(pemKey, keyID string) (*RotatingSigner, error) {
	var s *RS256Signer
	if pemKey != "" {
		key, err := parseRSAPrivateKey([]byte(pemKey))
		if err != nil {
			return nil, fmt.Errorf("rotating signer parse pem: %w", err)
		}
		var err2 error
		s, err2 = NewRS256Signer(key, keyID)
		if err2 != nil {
			return nil, err2
		}
	} else {
		var err error
		s, err = NewRS256Signer(nil, keyID)
		if err != nil {
			return nil, err
		}
	}
	return &RotatingSigner{active: s}, nil
}

// NewRotatingSignerFromEnv reads RSA_PRIVATE_KEY_PEM and JWT_KEY_ID env vars.
// Falls back to ephemeral key generation when RSA_PRIVATE_KEY_PEM is unset,
// with a loud warning — ephemeral keys break multi-replica deployments.
func NewRotatingSignerFromEnv() (*RotatingSigner, error) {
	if rawRing := os.Getenv("RSA_PRIVATE_KEYS_JSON"); rawRing != "" {
		var ring map[string]string
		if err := json.Unmarshal([]byte(rawRing), &ring); err != nil {
			return nil, fmt.Errorf("parse RSA_PRIVATE_KEYS_JSON: %w", err)
		}
		activeID := os.Getenv("JWT_KEY_ID")
		activePEM, ok := ring[activeID]
		if !ok || activeID == "" {
			return nil, fmt.Errorf("JWT_KEY_ID must select a key in RSA_PRIVATE_KEYS_JSON")
		}
		signer, err := NewRotatingSignerFromPEM(activePEM, activeID)
		if err != nil {
			return nil, err
		}
		if previousID := os.Getenv("JWT_PREVIOUS_KEY_ID"); previousID != "" {
			previousPEM, ok := ring[previousID]
			if !ok {
				return nil, fmt.Errorf("JWT_PREVIOUS_KEY_ID does not exist in RSA_PRIVATE_KEYS_JSON")
			}
			previous, err := NewRotatingSignerFromPEM(previousPEM, previousID)
			if err != nil {
				return nil, err
			}
			signer.previous = previous.active
		}
		return signer, nil
	}
	pemKey := os.Getenv("RSA_PRIVATE_KEY_PEM")
	if pemKey == "" {
		// Try loading from a file path if set.
		if path := os.Getenv("RSA_PRIVATE_KEY_FILE"); path != "" {
			// #nosec G304,G703 -- the path is trusted process configuration supplied by the operator/Kubernetes secret mount.
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read RSA_PRIVATE_KEY_FILE: %w", err)
			}
			pemKey = string(raw)
		}
	}
	if pemKey == "" {
		if os.Getenv("REQUIRE_PERSISTENT_SIGNING_KEY") == "true" {
			return nil, fmt.Errorf("persistent signing key is required")
		}
		slog.Warn("RSA_PRIVATE_KEY_PEM not set — using ephemeral key. Tokens WILL break on restart and across replicas. Generate a permanent key for production.")
	}
	keyID := os.Getenv("JWT_KEY_ID")
	if keyID == "" {
		keyID = "k1"
	}
	return NewRotatingSignerFromPEM(pemKey, keyID)
}

// Sign delegates to the active signer.
func (r *RotatingSigner) Sign(c Claims) (string, error) {
	r.mu.RLock()
	s := r.active
	r.mu.RUnlock()
	return s.Sign(c)
}

// Verify tries the active key, then the previous key.
func (r *RotatingSigner) Verify(tokenString string) (*Claims, error) {
	r.mu.RLock()
	active := r.active
	previous := r.previous
	r.mu.RUnlock()

	if c, err := active.Verify(tokenString); err == nil {
		return c, nil
	}
	if previous != nil {
		if c, err := previous.Verify(tokenString); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("jwt verify: invalid or expired token")
}

// JWKS returns both the active and previous public keys so clients can validate
// tokens signed by either key during the rotation window.
func (r *RotatingSigner) JWKS() JWKSDocument {
	r.mu.RLock()
	active := r.active
	previous := r.previous
	r.mu.RUnlock()

	doc := active.JWKS()
	if previous != nil {
		prev := previous.JWKS()
		doc.Keys = append(doc.Keys, prev.Keys...)
	}
	return doc
}

// KeyID returns the active key ID.
func (r *RotatingSigner) KeyID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active.KeyID()
}

// Rotate generates a new RSA-2048 key, promotes the current active to previous,
// and sets the new key as active. Call this on a schedule (e.g. every 24h).
func (r *RotatingSigner) Rotate(newKeyID string) error {
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("rotate: generate key: %w", err)
	}
	newSigner, err := NewRS256Signer(newKey, newKeyID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.previous = r.active
	r.active = newSigner
	r.rotatedAt = time.Now().UTC()
	r.mu.Unlock()
	return nil
}

// RotatedAt returns the time of the last key rotation (zero if never rotated).
func (r *RotatingSigner) RotatedAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rotatedAt
}

// StartRotationSchedule launches a goroutine that calls Rotate every interval.
// The key ID is derived from the rotation timestamp.
func (r *RotatingSigner) StartRotationSchedule(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case t := <-ticker.C:
				keyID := fmt.Sprintf("k-%d", t.Unix())
				if err := r.Rotate(keyID); err == nil {
					_ = err // rotation succeeded; log externally if needed
				}
			}
		}
	}()
}

func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}
