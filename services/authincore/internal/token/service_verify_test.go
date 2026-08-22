package token

import (
	"testing"
	"time"
)

func TestServiceVerifyRequiresIssuerAndAudience(t *testing.T) {
	signer, err := NewRS256Signer(nil, "test-key")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	service := NewService(signer, "https://expected.example", time.Minute, time.Hour)
	now := time.Now().UTC()

	tests := []struct {
		name   string
		claims Claims
		ok     bool
	}{
		{
			name: "valid issuer and audience",
			claims: Claims{Issuer: "https://expected.example", Audience: []string{"https://expected.example"},
				IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
			ok: true,
		},
		{
			name: "wrong issuer",
			claims: Claims{Issuer: "https://other.example", Audience: []string{"https://expected.example"},
				IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		},
		{
			name: "wrong audience",
			claims: Claims{Issuer: "https://expected.example", Audience: []string{"https://api.example"},
				IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := signer.Sign(test.claims)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			_, err = service.Verify(raw)
			if test.ok && err != nil {
				t.Fatalf("verify valid token: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("expected verification failure")
			}
		})
	}
}
