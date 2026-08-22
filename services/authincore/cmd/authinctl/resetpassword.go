package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	passwordstrategy "github.com/authincore/authincore/internal/flow/strategy/password"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Administrator password recovery (task.md §1.1).
//
// This is deliberately a separate command from `bootstrap`, not a flag on it.
// Bootstrap is the command an operator re-runs routinely — to pick up a
// change to the role model, say — and it reads BOOTSTRAP_ADMIN_PASSWORD from
// the environment every time. If re-running it also reset the password, then
// an operator whose shell still had the original value exported would reset
// it invisibly to the same string, and one whose shell did not would lock the
// administrator out of their own deployment. Neither failure announces
// itself. So bootstrap leaves an existing credential strictly alone
// (TestBootstrapNeverResetsAnExistingAdministratorPassword), and changing a
// password is something you have to ask for by name.
//
// Asking by name is not enough on its own: the command is run in a shell, in
// a container, usually during an incident. It therefore requires the operator
// to name the account they mean a second time, in RESET_ADMIN_CONFIRM, and it
// refuses to create anything — if the organization or the identity does not
// exist, that is an error rather than an invitation to provision one.

type resetPasswordConfig struct {
	OrgDomain string
	Email     string
	Password  string
	Confirm   string
}

type resetPasswordResult struct {
	OrganizationID string `json:"organization_id"`
	IdentityID     string `json:"identity_id"`
	Email          string `json:"email"`
	ResetAt        string `json:"reset_at"`
}

func resetPasswordConfigFromEnv(getenv func(string) string) (resetPasswordConfig, error) {
	cfg := resetPasswordConfig{
		OrgDomain: strings.TrimSpace(getenv("RESET_ORG_DOMAIN")),
		Email:     strings.TrimSpace(strings.ToLower(getenv("RESET_ADMIN_EMAIL"))),
		Password:  getenv("RESET_ADMIN_PASSWORD"),
		Confirm:   strings.TrimSpace(strings.ToLower(getenv("RESET_ADMIN_CONFIRM"))),
	}
	if cfg.OrgDomain == "" || cfg.Email == "" || cfg.Password == "" {
		return resetPasswordConfig{}, errors.New(
			"RESET_ORG_DOMAIN, RESET_ADMIN_EMAIL, and RESET_ADMIN_PASSWORD are required")
	}
	if !strings.Contains(cfg.Email, "@") {
		return resetPasswordConfig{}, errors.New("RESET_ADMIN_EMAIL must be an email address")
	}
	if len(cfg.Password) < 12 {
		return resetPasswordConfig{}, errors.New("RESET_ADMIN_PASSWORD must be at least 12 characters")
	}
	// The confirmation is the account's own address rather than a bare "yes",
	// so that pasting the command from a runbook without editing it cannot
	// reset the wrong administrator.
	if cfg.Confirm != cfg.Email {
		return resetPasswordConfig{}, fmt.Errorf(
			"refusing to reset a password without confirmation: set RESET_ADMIN_CONFIRM to %q "+
				"to confirm you mean this account", cfg.Email)
	}
	canonical, err := tenancy.CanonicalDomain(cfg.OrgDomain)
	if err != nil {
		return resetPasswordConfig{}, fmt.Errorf("RESET_ORG_DOMAIN: %w", err)
	}
	cfg.OrgDomain = canonical
	return cfg, nil
}

func cmdResetAdminPassword(ctx context.Context) error {
	cfg, err := resetPasswordConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	result, err := resetAdminPassword(ctx, pool, cfg)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// resetAdminPassword replaces one existing identity's password credential.
// It creates nothing: a missing organization or identity is an error, because
// the alternative is a typo silently provisioning a new administrator.
func resetAdminPassword(ctx context.Context, pool *pgxpool.Pool, cfg resetPasswordConfig) (resetPasswordResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return resetPasswordResult{}, fmt.Errorf("begin reset transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	instanceID := uuid.MustParse(defaultInstanceID)
	var orgID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT id FROM orgs WHERE instance_id=$1 AND primary_domain=$2`,
		instanceID, cfg.OrgDomain).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return resetPasswordResult{}, fmt.Errorf(
			"no organization with primary domain %q; this command never creates one", cfg.OrgDomain)
	}
	if err != nil {
		return resetPasswordResult{}, fmt.Errorf("find organization: %w", err)
	}

	var identityID, credentialID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT i.id, c.id FROM identities i
		   JOIN identity_credentials c ON c.identity_id = i.id
		  WHERE i.org_id=$1 AND c.type='password' AND c.identifiers @> jsonb_build_array($2::text)
		  LIMIT 1`,
		orgID, cfg.Email).Scan(&identityID, &credentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return resetPasswordResult{}, fmt.Errorf(
			"no password identity for %q in organization %q; this command never creates one",
			cfg.Email, cfg.OrgDomain)
	}
	if err != nil {
		return resetPasswordResult{}, fmt.Errorf("find administrator: %w", err)
	}

	hash, err := passwordstrategy.NewArgon2idHasher().Hash(cfg.Password)
	if err != nil {
		return resetPasswordResult{}, fmt.Errorf("hash new password: %w", err)
	}
	credentialConfig, err := json.Marshal(map[string]string{"hashed_password": hash})
	if err != nil {
		return resetPasswordResult{}, fmt.Errorf("encode credential: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE identity_credentials SET config=$1 WHERE id=$2 AND type='password'`,
		credentialConfig, credentialID)
	if err != nil {
		return resetPasswordResult{}, fmt.Errorf("write new credential: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return resetPasswordResult{}, fmt.Errorf(
			"expected to update exactly one credential, updated %d", tag.RowsAffected())
	}

	// The audit event records that the password changed and who asked, never
	// the password or its hash. A recovery that leaves no trace is
	// indistinguishable from an attacker who obtained database access.
	if err := insertIdentityEvent(ctx, tx, identityID, orgID, "identity.password.reset",
		map[string]string{
			"email":  cfg.Email,
			"method": "authinctl reset-admin-password",
			"actor":  "operator",
		}); err != nil {
		return resetPasswordResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return resetPasswordResult{}, fmt.Errorf("commit reset transaction: %w", err)
	}
	return resetPasswordResult{
		OrganizationID: orgID.String(),
		IdentityID:     identityID.String(),
		Email:          cfg.Email,
		ResetAt:        time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// insertIdentityEvent appends one event to an identity's stream.
//
// It computes the next sequence rather than assuming 1, because the events
// table is keyed on (aggregate_type, aggregate_id, sequence) — a second event
// about an identity that already has one would otherwise collide, and the
// whole reset would roll back with a primary-key error.
func insertIdentityEvent(ctx context.Context, tx pgx.Tx, identityID, orgID uuid.UUID, eventType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", eventType, err)
	}
	var next int64
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(sequence), 0) + 1 FROM events WHERE aggregate_type='identity' AND aggregate_id=$1`,
		identityID).Scan(&next); err != nil {
		return fmt.Errorf("determine next event sequence: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO events (id, aggregate_type, aggregate_id, org_id, event_type, payload, editor_actor, sequence)
		 VALUES ($1,'identity',$2,$3,$4,$5,'authinctl',$6)`,
		uuid.New(), identityID, orgID, eventType, b, next); err != nil {
		return fmt.Errorf("write %s event: %w", eventType, err)
	}
	return nil
}
