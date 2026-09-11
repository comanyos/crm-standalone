package server

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/santoshguru/crm/internal/auth"
)

// testSessions is an in-process stand-in for opaque database sessions. It is
// deliberately only test plumbing: production always constructs auth with
// auth.NewPostgres, while these server tests need arbitrary scope sets to
// sweep the fail-closed policy catalog.
func testSessions(t *testing.T) (*auth.Authenticator, func(string, []string) string) {
	t.Helper()
	var mu sync.RWMutex
	principals := map[string]*auth.Principal{}
	sequence := 0
	authn := auth.NewTestingAuthenticator(func(_ context.Context, token string) (*auth.Principal, error) {
		mu.RLock()
		defer mu.RUnlock()
		p, ok := principals[token]
		if !ok {
			return nil, auth.ErrInvalidSession
		}
		copy := *p
		copy.Roles = append([]string(nil), p.Roles...)
		copy.Scopes = append([]string(nil), p.Scopes...)
		return &copy, nil
	}, nil)
	mint := func(org string, scopes []string) string {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		sequence++
		token := fmt.Sprintf("opaque-test-session-%d", sequence)
		principals[token] = &auth.Principal{
			Subject: "user-" + org, TenantID: org, OrgID: org,
			Roles: []string{"test_role"}, Scopes: append([]string(nil), scopes...),
		}
		return token
	}
	return authn, mint
}
