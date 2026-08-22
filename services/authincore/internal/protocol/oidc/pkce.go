package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

func verifySHA256(verifier, challenge string) error {
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	if computed != challenge {
		return fmt.Errorf("PKCE S256 verification failed")
	}
	return nil
}
