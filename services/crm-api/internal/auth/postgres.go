package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepository struct {
	pool *pgxpool.Pool
}

func newPostgresRepository(ctx context.Context, dsn string) (*postgresRepository, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("auth: connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("auth: ping database: %w", err)
	}
	return &postgresRepository{pool: pool}, nil
}

func (r *postgresRepository) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }
func (r *postgresRepository) Close()                         { r.pool.Close() }

func (r *postgresRepository) CreateChallenge(
	ctx context.Context, c challenge, perEmailLimit, globalLimit int, window time.Duration,
) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The global lock makes the ceiling durable across every CRM process. It is
	// deliberately not keyed by caller IP: trusting forwarding headers without
	// an explicit proxy trust boundary would make that limit spoofable.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('savvy-auth-global-issuance', 0))`); err != nil {
		return fmt.Errorf("auth: lock global issuance: %w", err)
	}
	if err := enforceGlobalIssuanceLimit(ctx, tx, c.CreatedAt, globalLimit, window); err != nil {
		return err
	}
	// Serialize the per-address decision. Without this lock, parallel requests
	// could all observe a count below the limit and then all insert.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, c.EmailNormalized); err != nil {
		return fmt.Errorf("auth: lock challenge address: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM auth_email_challenges
		 WHERE email_normalized = $1 AND created_at > $2`,
		c.EmailNormalized, c.CreatedAt.Add(-window)).Scan(&count); err != nil {
		return fmt.Errorf("auth: count challenges: %w", err)
	}
	if count >= perEmailLimit {
		return ErrRateLimited
	}
	// Only the newest challenge for an address and flow remains live. This
	// prevents an old email becoming a second valid credential after resend.
	if _, err := tx.Exec(ctx, `
		UPDATE auth_email_challenges SET consumed_at = $3
		 WHERE email_normalized = $1 AND flow = $2 AND consumed_at IS NULL`,
		c.EmailNormalized, c.Flow, c.CreatedAt); err != nil {
		return fmt.Errorf("auth: retire earlier challenges: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_email_challenges
			(id, email_normalized, flow, code_digest, attempts_remaining, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID, c.EmailNormalized, c.Flow, c.CodeDigest, maxOTPAttempts, c.ExpiresAt, c.CreatedAt); err != nil {
		return fmt.Errorf("auth: insert challenge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit challenge: %w", err)
	}
	return nil
}

func (r *postgresRepository) RetireChallenge(ctx context.Context, id string, now time.Time) error {
	// Retain a failed-delivery attempt inside the rate window. Deleting it here
	// would let a caller evade both SMTP-spend limits simply by targeting an
	// address/provider combination that rejects delivery.
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_email_challenges SET consumed_at=COALESCE(consumed_at,$2)
		WHERE id=$1`, id, now)
	return err
}

func enforceGlobalIssuanceLimit(
	ctx context.Context, tx pgx.Tx, now time.Time, limit int, window time.Duration,
) error {
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM auth_email_challenges WHERE created_at > $1) +
			(SELECT count(*) FROM auth_oauth_states WHERE created_at > $1)`,
		now.Add(-window)).Scan(&count); err != nil {
		return fmt.Errorf("auth: count global issuance: %w", err)
	}
	if count >= limit {
		return ErrRateLimited
	}
	return nil
}

func (r *postgresRepository) ChallengeFlow(ctx context.Context, id string, now time.Time) (string, error) {
	var flow string
	err := r.pool.QueryRow(ctx, `
		SELECT flow FROM auth_email_challenges
		 WHERE id=$1 AND consumed_at IS NULL AND expires_at>$2 AND attempts_remaining>0`, id, now).Scan(&flow)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidChallenge
	}
	if err != nil {
		return "", fmt.Errorf("auth: inspect challenge: %w", err)
	}
	return flow, nil
}

func (r *postgresRepository) VerifyChallenge(
	ctx context.Context, id string, digest []byte, now time.Time,
) (string, string, error) {
	var email, flow string
	var matched bool
	err := r.pool.QueryRow(ctx, `
		UPDATE auth_email_challenges
		   SET attempts_remaining = CASE WHEN code_digest = $2 THEN attempts_remaining ELSE attempts_remaining - 1 END,
		       consumed_at = CASE WHEN code_digest = $2 THEN $3 ELSE consumed_at END
		 WHERE id = $1 AND consumed_at IS NULL AND expires_at > $3 AND attempts_remaining > 0
		 RETURNING email_normalized, flow, code_digest = $2`, id, digest, now).Scan(&email, &flow, &matched)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrInvalidChallenge
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: verify challenge: %w", err)
	}
	if !matched {
		return "", "", ErrInvalidChallenge
	}
	return email, flow, nil
}

const accountSelect = `
	SELECT u.id, u.email, u.first_name, u.last_name,
	       o.id, o.name, o.plan_key, m.role_key, m.job_role, m.sales_team_size
	  FROM auth_users u
	  JOIN auth_memberships m ON m.user_id = u.id AND m.active
	  JOIN auth_organizations o ON o.id = m.organization_id AND o.active
	 WHERE u.email_normalized = $1 AND u.active
	 ORDER BY m.created_at, o.id
	 LIMIT 1`

func scanAccount(row pgx.Row) (User, Organization, Membership, error) {
	var u User
	var o Organization
	var m Membership
	err := row.Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName,
		&o.ID, &o.Name, &o.Plan, &m.Role, &m.JobRole, &m.SalesTeamSize)
	return u, o, m, err
}

func (r *postgresRepository) Login(ctx context.Context, email string) (User, Organization, Membership, error) {
	u, o, m, err := scanAccount(r.pool.QueryRow(ctx, accountSelect, email))
	if errors.Is(err, pgx.ErrNoRows) {
		var pending bool
		if pendingErr := r.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM auth_join_requests j
				JOIN auth_users u ON u.id=j.user_id AND u.active
				JOIN auth_organizations o ON o.id=j.organization_id AND o.active
				WHERE u.email_normalized=$1 AND j.status='pending'
			)`, email).Scan(&pending); pendingErr != nil {
			return User{}, Organization{}, Membership{}, fmt.Errorf("auth: inspect pending access: %w", pendingErr)
		}
		if pending {
			return User{}, Organization{}, Membership{}, ErrApprovalPending
		}
		return User{}, Organization{}, Membership{}, ErrNoAccount
	}
	if err != nil {
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: load account: %w", err)
	}
	return u, o, m, nil
}

func (r *postgresRepository) Signup(
	ctx context.Context, email string, profile Profile, now time.Time,
) (User, Organization, Membership, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	u, o, m, err := signupTx(ctx, tx, email, profile, now)
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: commit signup: %w", err)
	}
	return u, o, m, nil
}

func signupTx(
	ctx context.Context, tx pgx.Tx, email string, profile Profile, now time.Time,
) (User, Organization, Membership, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, email); err != nil {
		return User{}, Organization{}, Membership{}, err
	}

	var u User
	var userActive bool
	err := tx.QueryRow(ctx, `
		SELECT id, email, first_name, last_name, active
		  FROM auth_users WHERE email_normalized = $1 FOR UPDATE`, email).
		Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &userActive)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		u.ID, err = newRecordID("usr")
		if err != nil {
			return User{}, Organization{}, Membership{}, err
		}
		u.Email, u.FirstName, u.LastName = email, stringsTrim(profile.FirstName), stringsTrim(profile.LastName)
		_, err = tx.Exec(ctx, `
			INSERT INTO auth_users
				(id, email_normalized, email, first_name, last_name, email_verified_at, created_at, updated_at)
			VALUES ($1, $2, $2, $3, $4, $5, $5, $5)`,
			u.ID, email, u.FirstName, u.LastName, now)
		if err != nil {
			return User{}, Organization{}, Membership{}, fmt.Errorf("auth: create user: %w", err)
		}
	case err != nil:
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: find signup user: %w", err)
	default:
		if !userActive {
			return User{}, Organization{}, Membership{}, ErrNoAccount
		}
		_, err = tx.Exec(ctx, `
			UPDATE auth_users
			   SET email_verified_at = COALESCE(email_verified_at, $2),
			       first_name = CASE WHEN first_name = '' THEN $3 ELSE first_name END,
			       last_name = CASE WHEN last_name = '' THEN $4 ELSE last_name END,
			       updated_at = $2
			 WHERE id = $1`, u.ID, now, stringsTrim(profile.FirstName), stringsTrim(profile.LastName))
		if err != nil {
			return User{}, Organization{}, Membership{}, err
		}
		if u.FirstName == "" {
			u.FirstName = stringsTrim(profile.FirstName)
		}
		if u.LastName == "" {
			u.LastName = stringsTrim(profile.LastName)
		}
	}

	// A verified address that already belongs to an organization logs into
	// that organization instead of creating duplicates on repeated signup.
	var o Organization
	var m Membership
	err = tx.QueryRow(ctx, `
		SELECT o.id, o.name, o.plan_key, m.role_key, m.job_role, m.sales_team_size
		  FROM auth_memberships m
		  JOIN auth_organizations o ON o.id = m.organization_id AND o.active
		 WHERE m.user_id = $1 AND m.active
		 ORDER BY m.created_at, o.id LIMIT 1`, u.ID).
		Scan(&o.ID, &o.Name, &o.Plan, &m.Role, &m.JobRole, &m.SalesTeamSize)
	if err == nil {
		return u, o, m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: find membership: %w", err)
	}
	// A verified applicant with an unresolved organization request must not be
	// able to create a second workspace by switching back to the public signup
	// path. They can create a workspace after the request is reviewed/rejected;
	// until then the administrator decision remains the single source of truth.
	var pendingJoin bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM auth_join_requests WHERE user_id=$1 AND status='pending')`, u.ID).
		Scan(&pendingJoin); err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	if pendingJoin {
		return User{}, Organization{}, Membership{}, ErrApprovalPending
	}

	o.ID, err = newRecordID("org")
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	o.Name = stringsTrim(profile.OrganizationName)
	m = Membership{Role: "crm_admin", JobRole: stringsTrim(profile.JobRole), SalesTeamSize: stringsTrim(profile.SalesTeamSize)}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_organizations (id, name, created_at, updated_at) VALUES ($1, $2, $3, $3)`,
		o.ID, o.Name, now); err != nil {
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: create organization: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_memberships
			(user_id, organization_id, role_key, job_role, sales_team_size, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		u.ID, o.ID, m.Role, m.JobRole, m.SalesTeamSize, now); err != nil {
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: create membership: %w", err)
	}
	return u, o, m, nil
}

func stringsTrim(v string) string { return strings.TrimSpace(v) }

func (r *postgresRepository) CreateSession(
	ctx context.Context, userID, orgID string, digest []byte, expires, now time.Time,
) error {
	id, err := newRecordID("ses")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO auth_sessions
			(id, token_digest, user_id, organization_id, expires_at, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`, id, digest, userID, orgID, expires, now)
	if err != nil {
		return fmt.Errorf("auth: create session: %w", err)
	}
	return nil
}

func (r *postgresRepository) Session(
	ctx context.Context, digest []byte, now time.Time,
) (User, Organization, Membership, time.Time, error) {
	var u User
	var o Organization
	var m Membership
	var expires time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.first_name, u.last_name,
		       o.id, o.name, o.plan_key, m.role_key, m.job_role, m.sales_team_size, s.expires_at
		  FROM auth_sessions s
		  JOIN auth_users u ON u.id = s.user_id AND u.active
		  JOIN auth_memberships m ON m.user_id = s.user_id
		                           AND m.organization_id = s.organization_id AND m.active
		  JOIN auth_organizations o ON o.id = s.organization_id AND o.active
		 WHERE s.token_digest = $1 AND s.revoked_at IS NULL AND s.expires_at > $2`, digest, now).
		Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &o.ID, &o.Name, &o.Plan,
			&m.Role, &m.JobRole, &m.SalesTeamSize, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, Organization{}, Membership{}, time.Time{}, ErrInvalidSession
	}
	if err != nil {
		return User{}, Organization{}, Membership{}, time.Time{}, fmt.Errorf("auth: load session: %w", err)
	}
	_, _ = r.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = $2 WHERE token_digest = $1`, digest, now)
	return u, o, m, expires, nil
}

func (r *postgresRepository) RevokeSession(ctx context.Context, digest []byte, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, $2)
		 WHERE token_digest = $1`, digest, now)
	return err
}

func (r *postgresRepository) Bootstrap(
	ctx context.Context, rawEmail, orgName, orgID string, now time.Time,
) (User, Organization, error) {
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return User{}, Organization{}, fmt.Errorf("auth bootstrap: %w", err)
	}
	if orgName = stringsTrim(orgName); orgName == "" {
		orgName = "Savvy CRM"
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, Organization{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Bootstrap shares signup's address lock. It is a create-only operation:
	// finding any existing user or requested organization is success, but must
	// never reactivate or promote it merely because a process restarted with
	// bootstrap configuration still present.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, email); err != nil {
		return User{}, Organization{}, err
	}
	if orgID != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 3))`, orgID); err != nil {
			return User{}, Organization{}, err
		}
	}

	var u User
	err = tx.QueryRow(ctx, `SELECT id, email, first_name, last_name FROM auth_users WHERE email_normalized=$1`, email).
		Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName)
	if err == nil {
		var existingOrg Organization
		lookupErr := tx.QueryRow(ctx, `
			SELECT o.id,o.name,o.plan_key FROM auth_organizations o
			JOIN auth_memberships m ON m.organization_id=o.id
			WHERE m.user_id=$1 ORDER BY m.created_at,o.id LIMIT 1`, u.ID).
			Scan(&existingOrg.ID, &existingOrg.Name, &existingOrg.Plan)
		if errors.Is(lookupErr, pgx.ErrNoRows) && orgID != "" {
			lookupErr = tx.QueryRow(ctx,
				`SELECT id,name,plan_key FROM auth_organizations WHERE id=$1`, orgID).
				Scan(&existingOrg.ID, &existingOrg.Name, &existingOrg.Plan)
		}
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return User{}, Organization{}, lookupErr
		}
		if err := tx.Commit(ctx); err != nil {
			return User{}, Organization{}, err
		}
		return u, existingOrg, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, Organization{}, err
	}

	var o Organization
	if orgID != "" {
		lookupErr := tx.QueryRow(ctx,
			`SELECT id,name,plan_key FROM auth_organizations WHERE id=$1`, orgID).
			Scan(&o.ID, &o.Name, &o.Plan)
		if lookupErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return User{}, Organization{}, err
			}
			return User{}, o, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return User{}, Organization{}, lookupErr
		}
	}

	u.ID, err = newRecordID("usr")
	if err != nil {
		return User{}, Organization{}, err
	}
	u.Email = email
	if orgID == "" {
		orgID, err = newRecordID("org")
		if err != nil {
			return User{}, Organization{}, err
		}
	}
	o = Organization{ID: orgID, Name: orgName, Plan: "free"}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_users (id,email_normalized,email,email_verified_at,created_at,updated_at)
		VALUES ($1,$2,$2,$3,$3,$3)`, u.ID, email, now); err != nil {
		return User{}, Organization{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_organizations (id,name,created_at,updated_at) VALUES ($1,$2,$3,$3)`,
		o.ID, o.Name, now); err != nil {
		return User{}, Organization{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_memberships (user_id,organization_id,role_key,created_at,updated_at)
		VALUES ($1,$2,'crm_admin',$3,$3)`, u.ID, o.ID, now); err != nil {
		return User{}, Organization{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, Organization{}, err
	}
	return u, o, nil
}

func (r *postgresRepository) CreateOAuthState(
	ctx context.Context, state oauthState, globalLimit int, window time.Duration,
) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('savvy-auth-global-issuance', 0))`); err != nil {
		return fmt.Errorf("auth: lock global issuance: %w", err)
	}
	if err := enforceGlobalIssuanceLimit(ctx, tx, state.CreatedAt, globalLimit, window); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_oauth_states
			(state_digest,binding_digest,flow,nonce,pkce_verifier,organization_name,job_role,sales_team_size,
			 join_code_digest,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, state.Digest, state.BindingDigest, state.Flow, state.Nonce,
		state.PKCEVerifier, state.OrganizationName, state.JobRole, state.SalesTeamSize, state.JoinCodeDigest,
		state.ExpiresAt, state.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *postgresRepository) ConsumeOAuthState(
	ctx context.Context, digest, bindingDigest []byte, now time.Time,
) (oauthState, error) {
	var state oauthState
	err := r.pool.QueryRow(ctx, `
		UPDATE auth_oauth_states SET consumed_at=$2
		 WHERE state_digest=$1 AND binding_digest=$3 AND consumed_at IS NULL AND expires_at>$2
		 RETURNING state_digest,binding_digest,flow,nonce,pkce_verifier,organization_name,job_role,sales_team_size,
		           join_code_digest,expires_at`,
		digest, now, bindingDigest).Scan(&state.Digest, &state.BindingDigest, &state.Flow, &state.Nonce, &state.PKCEVerifier,
		&state.OrganizationName, &state.JobRole, &state.SalesTeamSize, &state.JoinCodeDigest, &state.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return oauthState{}, ErrOAuthState
	}
	return state, err
}

func (r *postgresRepository) Cleanup(ctx context.Context, now time.Time, challengeRetention time.Duration) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Challenge rows inside the request-rate window must remain even after use
	// or expiry; deleting them sooner would let repeated resends evade the
	// durable per-address rate limit.
	if _, err := tx.Exec(ctx, `
		DELETE FROM auth_email_challenges
		WHERE created_at < $1 AND (expires_at <= $2 OR consumed_at IS NOT NULL)`,
		now.Add(-challengeRetention), now); err != nil {
		return fmt.Errorf("auth: clean challenges: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM auth_oauth_states
		WHERE created_at < $1 AND (expires_at <= $2 OR consumed_at IS NOT NULL)`,
		now.Add(-challengeRetention), now); err != nil {
		return fmt.Errorf("auth: clean OAuth states: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM auth_sessions
		WHERE expires_at <= $1 OR revoked_at IS NOT NULL`, now); err != nil {
		return fmt.Errorf("auth: clean sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit cleanup: %w", err)
	}
	return nil
}

func (r *postgresRepository) LoginExternal(
	ctx context.Context, provider string, identity externalIdentity,
) (User, Organization, Membership, error) {
	if !identity.EmailVerified {
		return User{}, Organization{}, Membership{}, ErrNoAccount
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var email string
	err = tx.QueryRow(ctx, `
		SELECT u.email_normalized
		  FROM auth_external_identities i
		  JOIN auth_users u ON u.id=i.user_id AND u.active
		 WHERE i.provider=$1 AND i.provider_subject=$2`, provider, identity.Subject).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		email = identity.Email
		var userID string
		if err := tx.QueryRow(ctx, `
			SELECT id FROM auth_users
			WHERE email_normalized=$1 AND active AND email_verified_at IS NOT NULL`, email).Scan(&userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return User{}, Organization{}, Membership{}, ErrNoAccount
			}
			return User{}, Organization{}, Membership{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_external_identities (provider,provider_subject,user_id,email_at_link)
			VALUES ($1,$2,$3,$4)`, provider, identity.Subject, userID, email); err != nil {
			return User{}, Organization{}, Membership{}, fmt.Errorf("auth: link external identity: %w", err)
		}
	} else if err != nil {
		return User{}, Organization{}, Membership{}, err
	}

	u, o, m, err := scanAccount(tx.QueryRow(ctx, accountSelect, email))
	if errors.Is(err, pgx.ErrNoRows) {
		var pending bool
		if pendingErr := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM auth_join_requests j
				JOIN auth_users u ON u.id=j.user_id AND u.active
				JOIN auth_organizations o ON o.id=j.organization_id AND o.active
				WHERE u.email_normalized=$1 AND j.status='pending'
			)`, email).Scan(&pending); pendingErr != nil {
			return User{}, Organization{}, Membership{}, pendingErr
		}
		if pending {
			return User{}, Organization{}, Membership{}, ErrApprovalPending
		}
		return User{}, Organization{}, Membership{}, ErrNoAccount
	}
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	return u, o, m, nil
}

func (r *postgresRepository) SignupExternal(
	ctx context.Context, provider string, identity externalIdentity, profile Profile, now time.Time,
) (User, Organization, Membership, error) {
	if !identity.EmailVerified {
		return User{}, Organization{}, Membership{}, errors.New("auth: provider email is not verified")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Provider subject is the durable identity. If Google later changes the
	// account's email, it must keep reaching the originally linked CRM user;
	// it must never create or take over a second user selected by the new
	// email address.
	var linkedEmail string
	err = tx.QueryRow(ctx, `
		SELECT u.email_normalized
		FROM auth_external_identities i
		JOIN auth_users u ON u.id=i.user_id AND u.active
		WHERE i.provider=$1 AND i.provider_subject=$2`, provider, identity.Subject).Scan(&linkedEmail)
	if err == nil {
		u, o, m, loadErr := scanAccount(tx.QueryRow(ctx, accountSelect, linkedEmail))
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				return User{}, Organization{}, Membership{}, ErrNoAccount
			}
			return User{}, Organization{}, Membership{}, loadErr
		}
		if err := tx.Commit(ctx); err != nil {
			return User{}, Organization{}, Membership{}, err
		}
		return u, o, m, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, Organization{}, Membership{}, err
	}
	u, o, m, err := signupTx(ctx, tx, identity.Email, profile, now)
	if err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_external_identities (provider,provider_subject,user_id,email_at_link,updated_at)
		VALUES ($1,$2,$3,$4,$5)`, provider, identity.Subject, u.ID, identity.Email, now); err != nil {
		return User{}, Organization{}, Membership{}, fmt.Errorf("auth: link signup identity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, Organization{}, Membership{}, err
	}
	return u, o, m, nil
}
