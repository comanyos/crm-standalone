package domain

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a random 128-bit identifier for a caller outside this
// package.
//
// It exists for the one case that genuinely needs an id before the store
// assigns one: Gmail sync has to name the Activity inside its index row and
// then create that exact Activity, in one transaction. Everything else should
// let the store assign the id.
func NewID() string { return newID() }

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
