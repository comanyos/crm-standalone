package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	passwordstrategy "github.com/authincore/authincore/internal/flow/strategy/password"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Administrator password recovery (task.md §1.1).

// TestResetPasswordConfigRequiresExplicitConfirmation covers the guardrails
// that run before anything touches the database. Each of these is a mistake
// somebody makes at 3am with a runbook open in another window.
func TestResetPasswordConfigRequiresExplicitConfirmation(t *testing.T) {
	base := envMap{
		"RESET_ORG_DOMAIN":     "acme.example",
		"RESET_ADMIN_EMAIL":    "admin@acme.example",
		"RESET_ADMIN_PASSWORD": "correct-horse-battery-staple",
		"RESET_ADMIN_CONFIRM":  "admin@acme.example",
	}
	if _, err := resetPasswordConfigFromEnv(base.get); err != nil {
		t.Fatalf("a fully specified reset was rejected: %v", err)
	}

	for name, mutate := range map[string]func(envMap){
		"no confirmation":            func(m envMap) { delete(m, "RESET_ADMIN_CONFIRM") },
		"confirmation is just 'yes'": func(m envMap) { m["RESET_ADMIN_CONFIRM"] = "yes" },
		"confirms a different account": func(m envMap) {
			m["RESET_ADMIN_CONFIRM"] = "someone.else@acme.example"
		},
		"no organization": func(m envMap) { delete(m, "RESET_ORG_DOMAIN") },
		"no email":        func(m envMap) { delete(m, "RESET_ADMIN_EMAIL") },
		"no password":     func(m envMap) { delete(m, "RESET_ADMIN_PASSWORD") },
		"short password":  func(m envMap) { m["RESET_ADMIN_PASSWORD"] = "short" },
		"not an email":    func(m envMap) { m["RESET_ADMIN_EMAIL"] = "admin"; m["RESET_ADMIN_CONFIRM"] = "admin" },
		"unusable domain": func(m envMap) { m["RESET_ORG_DOMAIN"] = "not a domain" },
	} {
		t.Run(name, func(t *testing.T) {
			env := envMap{}
			for k, v := range base {
				env[k] = v
			}
			mutate(env)
			if _, err := resetPasswordConfigFromEnv(env.get); err == nil {
				t.Error("accepted a reset it should have refused")
			}
		})
	}
}

// TestConfirmationIsCaseInsensitiveOnTheAddress keeps the guardrail from
// becoming a nuisance that operators route around: the address is normalized
// the same way on both sides, so differing case is not a failure.
func TestConfirmationIsCaseInsensitiveOnTheAddress(t *testing.T) {
	cfg, err := resetPasswordConfigFromEnv(envMap{
		"RESET_ORG_DOMAIN":     "acme.example",
		"RESET_ADMIN_EMAIL":    "Admin@Acme.Example",
		"RESET_ADMIN_PASSWORD": "correct-horse-battery-staple",
		"RESET_ADMIN_CONFIRM":  "ADMIN@acme.example",
	}.get)
	if err != nil {
		t.Fatalf("case-differing confirmation was refused: %v", err)
	}
	if cfg.Email != "admin@acme.example" {
		t.Errorf("email = %q, want it lowercased", cfg.Email)
	}
}

// credentialHash reads one identity's stored password hash.
func credentialHash(ctx context.Context, t *testing.T, pool *pgxpool.Pool, identityID uuid.UUID) string {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx,
		`SELECT config FROM identity_credentials WHERE identity_id=$1 AND type='password'`,
		identityID).Scan(&raw); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	var cred struct {
		HashedPassword string `json:"hashed_password"`
	}
	if err := json.Unmarshal(raw, &cred); err != nil {
		t.Fatalf("parse credential: %v", err)
	}
	return cred.HashedPassword
}

// TestResetAdminPasswordReplacesTheCredentialAndAudits is the happy path: the
// new password works, the old one stops working, and the change is on the
// record.
func TestResetAdminPasswordReplacesTheCredentialAndAudits(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "resetok")

	created, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	identityID := uuid.MustParse(created.AdministratorID)
	originalHash := credentialHash(ctx, t, pool, identityID)

	const newPassword = "a-completely-new-passphrase"
	result, err := resetAdminPassword(ctx, pool, resetPasswordConfig{
		OrgDomain: cfg.OrgDomain,
		Email:     cfg.AdminEmail,
		Password:  newPassword,
		Confirm:   cfg.AdminEmail,
	})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if result.IdentityID != created.AdministratorID {
		t.Errorf("reset reported identity %s, bootstrap created %s", result.IdentityID, created.AdministratorID)
	}
	if result.OrganizationID != created.OrganizationID {
		t.Errorf("reset reported organization %s, bootstrap created %s",
			result.OrganizationID, created.OrganizationID)
	}

	updatedHash := credentialHash(ctx, t, pool, identityID)
	if updatedHash == originalHash {
		t.Fatal("the stored hash did not change")
	}

	hasher := passwordstrategy.NewArgon2idHasher()
	if ok, err := hasher.Verify(newPassword, updatedHash); err != nil || !ok {
		t.Errorf("the new password does not verify (ok=%v err=%v)", ok, err)
	}
	if ok, err := hasher.Verify(cfg.AdminPassword, updatedHash); err == nil && ok {
		t.Error("the old password still authenticates after a reset")
	}

	// Exactly one credential row: a reset must replace, never add a second
	// way in that nobody knows about.
	var credentials int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM identity_credentials WHERE identity_id=$1 AND type='password'`,
		identityID).Scan(&credentials); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if credentials != 1 {
		t.Errorf("%d password credentials after a reset, want 1", credentials)
	}

	// The audit event exists, names the account, and carries no secret.
	var payload []byte
	var actor string
	if err := pool.QueryRow(ctx,
		`SELECT payload, editor_actor FROM events
		  WHERE aggregate_type='identity' AND aggregate_id=$1 AND event_type='identity.password.reset'`,
		identityID).Scan(&payload, &actor); err != nil {
		t.Fatalf("no identity.password.reset audit event: %v", err)
	}
	if actor == "" || actor == "system" {
		t.Errorf("audit event actor = %q; a recovery must record that an operator did it", actor)
	}
	recorded := string(payload)
	if !strings.Contains(recorded, cfg.AdminEmail) {
		t.Errorf("audit event does not name the account: %s", recorded)
	}
	for _, secret := range []string{newPassword, cfg.AdminPassword, updatedHash, "hashed_password"} {
		if secret != "" && strings.Contains(recorded, secret) {
			t.Errorf("audit event payload contains a secret: %s", recorded)
		}
	}
}

// TestResetRefusesToCreateAnything is the property that makes a typo safe. A
// mistyped domain or address must fail loudly, not quietly provision a second
// administrator nobody is watching.
func TestResetRefusesToCreateAnything(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "resetnocreate")

	if _, err := bootstrap(ctx, pool, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	countIdentities := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM identities i JOIN orgs o ON o.id=i.org_id WHERE o.primary_domain=$1`,
			cfg.OrgDomain).Scan(&n); err != nil {
			t.Fatalf("count identities: %v", err)
		}
		return n
	}
	before := countIdentities()

	for name, bad := range map[string]resetPasswordConfig{
		"unknown organization": {
			OrgDomain: "no-such-org.example", Email: cfg.AdminEmail,
			Password: "a-completely-new-passphrase", Confirm: cfg.AdminEmail,
		},
		"unknown administrator": {
			OrgDomain: cfg.OrgDomain, Email: "typo@" + cfg.OrgDomain,
			Password: "a-completely-new-passphrase", Confirm: "typo@" + cfg.OrgDomain,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resetAdminPassword(ctx, pool, bad); err == nil {
				t.Error("the reset succeeded against something that does not exist")
			}
		})
	}

	if after := countIdentities(); after != before {
		t.Errorf("a failed reset changed the identity count from %d to %d", before, after)
	}
}

// TestRepeatedResetsAppendDistinctAuditEvents catches the event-sequence bug
// this command would otherwise have: the events table is keyed on
// (aggregate_type, aggregate_id, sequence), so a second reset that reused
// sequence 1 would fail on the primary key and roll the whole reset back —
// leaving an operator unable to recover an account twice.
func TestRepeatedResetsAppendDistinctAuditEvents(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "resettwice")

	created, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	identityID := uuid.MustParse(created.AdministratorID)

	for i, password := range []string{"first-new-passphrase-x", "second-new-passphrase-y"} {
		if _, err := resetAdminPassword(ctx, pool, resetPasswordConfig{
			OrgDomain: cfg.OrgDomain, Email: cfg.AdminEmail,
			Password: password, Confirm: cfg.AdminEmail,
		}); err != nil {
			t.Fatalf("reset %d: %v", i+1, err)
		}
	}

	var events int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events
		  WHERE aggregate_type='identity' AND aggregate_id=$1 AND event_type='identity.password.reset'`,
		identityID).Scan(&events); err != nil {
		t.Fatalf("count reset events: %v", err)
	}
	if events != 2 {
		t.Errorf("%d password-reset audit events after two resets, want 2", events)
	}

	// And the last password is the one that works.
	hasher := passwordstrategy.NewArgon2idHasher()
	hash := credentialHash(ctx, t, pool, identityID)
	if ok, err := hasher.Verify("second-new-passphrase-y", hash); err != nil || !ok {
		t.Errorf("the most recent password does not verify (ok=%v err=%v)", ok, err)
	}
}

// TestResetLeavesRolesAndGrantsAlone confirms the blast radius: recovering a
// password is not an occasion to reprovision anything else.
func TestResetLeavesRolesAndGrantsAlone(t *testing.T) {
	pool := liveAuthDB(t)
	ctx := context.Background()
	cfg := testConfig(t, "resetscope")

	created, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	orgID := uuid.MustParse(created.OrganizationID)
	before := countFor(ctx, t, pool, orgID)

	if _, err := resetAdminPassword(ctx, pool, resetPasswordConfig{
		OrgDomain: cfg.OrgDomain, Email: cfg.AdminEmail,
		Password: "a-completely-new-passphrase", Confirm: cfg.AdminEmail,
	}); err != nil {
		t.Fatalf("reset: %v", err)
	}

	if after := countFor(ctx, t, pool, orgID); after != before {
		t.Errorf("a password reset changed the organization's model:\n  before %+v\n  after  %+v",
			before, after)
	}
}
