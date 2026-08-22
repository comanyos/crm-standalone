//go:build enterprise

// Package enterprise contains features compiled only into the enterprise binary.
package enterprise

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoginPolicy is the SSO enforcement configuration for an org.
type LoginPolicy struct {
	SSOEnforced           bool
	SSOProviderID         *uuid.UUID
	AllowPasswordFallback bool
}

// LoginPolicyStore loads org login policies from the database.
type LoginPolicyStore struct {
	db *pgxpool.Pool
}

func NewLoginPolicyStore(db *pgxpool.Pool) *LoginPolicyStore {
	return &LoginPolicyStore{db: db}
}

// Get returns the login policy for the given org.
// Returns a zero-value policy (SSO not enforced) if none is configured.
func (s *LoginPolicyStore) Get(ctx context.Context, orgID uuid.UUID) (LoginPolicy, error) {
	var p LoginPolicy
	err := s.db.QueryRow(ctx,
		`SELECT sso_enforced, sso_provider_id, allow_password_fallback
		 FROM org_login_policies WHERE org_id = $1`, orgID,
	).Scan(&p.SSOEnforced, &p.SSOProviderID, &p.AllowPasswordFallback)
	if err != nil {
		if err == pgx.ErrNoRows {
			return LoginPolicy{}, nil
		}
		return LoginPolicy{}, fmt.Errorf("login policy: %w", err)
	}
	return p, nil
}

// Upsert creates or updates the login policy for the given org.
func (s *LoginPolicyStore) Upsert(ctx context.Context, orgID uuid.UUID, p LoginPolicy) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO org_login_policies (org_id, sso_enforced, sso_provider_id, allow_password_fallback, updated_at)
		 VALUES ($1, $2, $3, $4, NOW())
		 ON CONFLICT (org_id) DO UPDATE
		 SET sso_enforced = EXCLUDED.sso_enforced,
		     sso_provider_id = EXCLUDED.sso_provider_id,
		     allow_password_fallback = EXCLUDED.allow_password_fallback,
		     updated_at = NOW()`,
		orgID, p.SSOEnforced, p.SSOProviderID, p.AllowPasswordFallback,
	)
	return err
}

// AllowMethod returns true if the given login method is permitted under the policy.
// When SSO is enforced only "saml" and "oidc" pass; password is allowed only if
// AllowPasswordFallback is set (break-glass path).
// When SSO is not enforced, all methods are allowed.
func AllowMethod(policy LoginPolicy, method string) bool {
	if !policy.SSOEnforced {
		return true
	}
	switch method {
	case "saml", "oidc":
		return true
	case "password":
		return policy.AllowPasswordFallback
	default:
		return false
	}
}
