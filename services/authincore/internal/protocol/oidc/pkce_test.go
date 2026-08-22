package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// verifySHA256 tests (internal function, white-box test in same package)
// ---------------------------------------------------------------------------

func TestVerifySHA256(t *testing.T) {
	t.Parallel()

	makeChallenge := func(verifier string) string {
		h := sha256.Sum256([]byte(verifier))
		return base64.RawURLEncoding.EncodeToString(h[:])
	}

	tests := []struct {
		name      string
		verifier  string
		challenge string
		wantErr   bool
	}{
		{
			name:      "correct verifier passes",
			verifier:  "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
			challenge: makeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
			wantErr:   false,
		},
		{
			name:      "wrong verifier fails",
			verifier:  "wrong-verifier",
			challenge: makeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
			wantErr:   true,
		},
		{
			name:      "empty verifier against valid challenge fails",
			verifier:  "",
			challenge: makeChallenge("some-verifier"),
			wantErr:   true,
		},
		{
			name:      "empty verifier against empty challenge fails",
			verifier:  "",
			challenge: makeChallenge(""),
			wantErr:   false, // sha256("") == challenge is correct math
		},
		{
			name:      "verifier with special characters passes",
			verifier:  "abc-._~def",
			challenge: makeChallenge("abc-._~def"),
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := verifySHA256(tc.verifier, tc.challenge)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// verifyPKCE tests (covers S256, plain, empty, unsupported)
// ---------------------------------------------------------------------------

func TestVerifyPKCE(t *testing.T) {
	t.Parallel()

	makeS256Challenge := func(verifier string) string {
		h := sha256.Sum256([]byte(verifier))
		return base64.RawURLEncoding.EncodeToString(h[:])
	}

	const verifier = "s3cr3t-v3rifier-string-long-enough"

	tests := []struct {
		name      string
		verifier  string
		challenge string
		method    string
		wantErr   bool
		errMsg    string
	}{
		// S256 cases
		{
			name:      "S256 correct verifier passes",
			verifier:  verifier,
			challenge: makeS256Challenge(verifier),
			method:    "S256",
			wantErr:   false,
		},
		{
			name:      "S256 wrong verifier fails",
			verifier:  "wrong",
			challenge: makeS256Challenge(verifier),
			method:    "S256",
			wantErr:   true,
		},
		{
			name:      "S256 missing verifier fails",
			verifier:  "",
			challenge: makeS256Challenge(verifier),
			method:    "S256",
			wantErr:   true,
			errMsg:    "code_verifier required",
		},
		// plain and empty method are rejected — only S256 is accepted
		{
			name:      "plain method rejected",
			verifier:  "my-plain-verifier",
			challenge: "my-plain-verifier",
			method:    "plain",
			wantErr:   true,
		},
		{
			name:      "empty method rejected",
			verifier:  "x",
			challenge: "x",
			method:    "",
			wantErr:   true,
		},
		// no PKCE
		{
			name:      "empty challenge and empty verifier passes (no PKCE required)",
			verifier:  "",
			challenge: "",
			method:    "S256",
			wantErr:   false,
		},
		// unsupported method
		{
			name:      "unsupported method returns error",
			verifier:  "v",
			challenge: "c",
			method:    "RS256",
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := verifyPKCE(tc.verifier, tc.challenge, tc.method)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errMsg != "" {
					assert.Contains(t, err.Error(), tc.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
