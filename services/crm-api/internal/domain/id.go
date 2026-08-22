package domain

import (
	"crypto/rand"
	"encoding/hex"
)

// newID returns a random 128-bit identifier. Store implementations assign one when a
// record arrives without an ID; callers may supply their own.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not recoverable and must not produce a
		// predictable or colliding identifier.
		panic("domain: entropy source unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
