package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *postgresRepository) RequestJoin(
	ctx context.Context, email string, profile JoinProfile, codeDigest []byte, now time.Time,
) (joinResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return joinResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := requestJoinTx(ctx, tx, email, profile.FirstName, profile.LastName, codeDigest, now)
	if err != nil {
		return joinResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return joinResult{}, fmt.Errorf("auth join: commit request: %w", err)
	}
	return result, nil
}

func (r *postgresRepository) RequestJoinExternal(
	ctx context.Context, provider string, identity externalIdentity, profile JoinProfile, codeDigest []byte, now time.Time,
) (joinResult, error) {
	if !identity.EmailVerified {
		return joinResult{}, ErrJoinConflict
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return joinResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	email := identity.Email
	var linkedEmail string
	err = tx.QueryRow(ctx, `
		SELECT u.email_normalized
		  FROM auth_external_identities i
		  JOIN auth_users u ON u.id=i.user_id AND u.active
		 WHERE i.provider=$1 AND i.provider_subject=$2
		 FOR SHARE OF i,u`, provider, identity.Subject).Scan(&linkedEmail)
	switch {
	case err == nil:
		email = linkedEmail
	case !errors.Is(err, pgx.ErrNoRows):
		return joinResult{}, err
	}

	result, err := requestJoinTx(ctx, tx, email, profile.FirstName, profile.LastName, codeDigest, now)
	if err != nil {
		return joinResult{}, err
	}
	if linkedEmail == "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_external_identities (provider,provider_subject,user_id,email_at_link,updated_at)
			VALUES ($1,$2,$3,$4,$5)`, provider, identity.Subject, result.User.ID, identity.Email, now); err != nil {
			return joinResult{}, fmt.Errorf("auth join: link external identity: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return joinResult{}, fmt.Errorf("auth join: commit external request: %w", err)
	}
	return result, nil
}

func requestJoinTx(
	ctx context.Context, tx pgx.Tx, email, firstName, lastName string, codeDigest []byte, now time.Time,
) (joinResult, error) {
	if len(codeDigest) != sha256DigestBytes {
		return joinResult{}, ErrInvalidJoinCode
	}
	var organization Organization
	err := tx.QueryRow(ctx, `
		SELECT o.id,o.name,o.plan_key
		  FROM auth_organization_join_codes c
		  JOIN auth_organizations o ON o.id=c.organization_id AND o.active
		 WHERE c.code_digest=$1 AND c.enabled
		 FOR SHARE OF c,o`, codeDigest).
		Scan(&organization.ID, &organization.Name, &organization.Plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return joinResult{}, ErrInvalidJoinCode
	}
	if err != nil {
		return joinResult{}, fmt.Errorf("auth join: find organization: %w", err)
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, email); err != nil {
		return joinResult{}, err
	}
	firstName, lastName = stringsTrim(firstName), stringsTrim(lastName)
	var user User
	var active bool
	err = tx.QueryRow(ctx, `
		SELECT id,email,first_name,last_name,active
		  FROM auth_users WHERE email_normalized=$1 FOR UPDATE`, email).
		Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		user.ID, err = newRecordID("usr")
		if err != nil {
			return joinResult{}, err
		}
		user.Email, user.FirstName, user.LastName = email, firstName, lastName
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_users
				(id,email_normalized,email,first_name,last_name,email_verified_at,created_at,updated_at)
			VALUES ($1,$2,$2,$3,$4,$5,$5,$5)`, user.ID, email, firstName, lastName, now); err != nil {
			return joinResult{}, fmt.Errorf("auth join: create user: %w", err)
		}
	case err != nil:
		return joinResult{}, fmt.Errorf("auth join: find user: %w", err)
	case !active:
		return joinResult{}, ErrJoinConflict
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE auth_users
			   SET email_verified_at=COALESCE(email_verified_at,$2),
			       first_name=CASE WHEN first_name='' THEN $3 ELSE first_name END,
			       last_name=CASE WHEN last_name='' THEN $4 ELSE last_name END,
			       updated_at=$2
			 WHERE id=$1`, user.ID, now, firstName, lastName); err != nil {
			return joinResult{}, err
		}
		if user.FirstName == "" {
			user.FirstName = firstName
		}
		if user.LastName == "" {
			user.LastName = lastName
		}
	}

	var existingOrganization Organization
	var membership Membership
	err = tx.QueryRow(ctx, `
		SELECT o.id,o.name,o.plan_key,m.role_key,m.job_role,m.sales_team_size
		  FROM auth_memberships m
		  JOIN auth_organizations o ON o.id=m.organization_id AND o.active
		 WHERE m.user_id=$1 AND m.active
		 ORDER BY m.created_at,o.id LIMIT 1`, user.ID).
		Scan(&existingOrganization.ID, &existingOrganization.Name, &existingOrganization.Plan,
			&membership.Role, &membership.JobRole, &membership.SalesTeamSize)
	if err == nil {
		if existingOrganization.ID != organization.ID {
			return joinResult{}, ErrJoinConflict
		}
		return joinResult{ExistingMember: true, User: user, Organization: existingOrganization, Membership: membership}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return joinResult{}, err
	}

	var inactiveMembership bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM auth_memberships WHERE user_id=$1 AND organization_id=$2)`,
		user.ID, organization.ID).Scan(&inactiveMembership); err != nil {
		return joinResult{}, err
	}
	if inactiveMembership {
		return joinResult{}, ErrJoinConflict
	}

	var pending JoinRequest
	err = tx.QueryRow(ctx, `
		SELECT j.id,j.organization_id,o.name,j.user_id,u.email,u.first_name,u.last_name,
		       j.status,COALESCE(j.assigned_role,''),j.requested_at,j.reviewed_at
		  FROM auth_join_requests j
		  JOIN auth_users u ON u.id=j.user_id
		  JOIN auth_organizations o ON o.id=j.organization_id
		 WHERE j.user_id=$1 AND j.status='pending'
		 FOR UPDATE OF j`, user.ID).
		Scan(&pending.ID, &pending.OrganizationID, &pending.OrganizationName, &pending.UserID,
			&pending.Email, &pending.FirstName, &pending.LastName, &pending.Status,
			&pending.AssignedRole, &pending.RequestedAt, &pending.ReviewedAt)
	if err == nil {
		if pending.OrganizationID != organization.ID {
			return joinResult{}, ErrJoinConflict
		}
		return joinResult{Request: pending, User: user, Organization: organization}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return joinResult{}, err
	}

	requestID, err := newRecordID("join")
	if err != nil {
		return joinResult{}, err
	}
	request := JoinRequest{
		ID: requestID, OrganizationID: organization.ID, OrganizationName: organization.Name,
		UserID: user.ID, Email: user.Email, FirstName: user.FirstName, LastName: user.LastName,
		Status: "pending", RequestedAt: now,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_join_requests (id,organization_id,user_id,status,requested_at,updated_at)
		VALUES ($1,$2,$3,'pending',$4,$4)`, request.ID, organization.ID, user.ID, now); err != nil {
		return joinResult{}, fmt.Errorf("auth join: create request: %w", err)
	}
	return joinResult{Request: request, User: user, Organization: organization}, nil
}

const sha256DigestBytes = 32

func (r *postgresRepository) ListAdminJoinRequests(ctx context.Context, organizationID string) ([]JoinRequest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT j.id,j.organization_id,o.name,j.user_id,u.email,u.first_name,u.last_name,
		       j.status,COALESCE(j.assigned_role,''),j.requested_at,j.reviewed_at
		  FROM auth_join_requests j
		  JOIN auth_users u ON u.id=j.user_id
		  JOIN auth_organizations o ON o.id=j.organization_id
		 WHERE j.organization_id=$1 AND j.status='pending'
		 ORDER BY j.requested_at,j.id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("auth join: list requests: %w", err)
	}
	defer rows.Close()
	requests := make([]JoinRequest, 0)
	for rows.Next() {
		var request JoinRequest
		if err := rows.Scan(&request.ID, &request.OrganizationID, &request.OrganizationName,
			&request.UserID, &request.Email, &request.FirstName, &request.LastName, &request.Status,
			&request.AssignedRole, &request.RequestedAt, &request.ReviewedAt); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (r *postgresRepository) ReviewAdminJoinRequest(
	ctx context.Context, actorUserID, requestID, organizationID, decision, role string, now time.Time,
) (JoinRequest, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return JoinRequest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var request JoinRequest
	var userActive, organizationActive bool
	err = tx.QueryRow(ctx, `
		SELECT j.id,j.organization_id,o.name,j.user_id,u.email,u.first_name,u.last_name,
		       j.status,COALESCE(j.assigned_role,''),j.requested_at,j.reviewed_at,
		       u.active,o.active
		  FROM auth_join_requests j
		  JOIN auth_users u ON u.id=j.user_id
		  JOIN auth_organizations o ON o.id=j.organization_id
		 WHERE j.id=$1 AND j.organization_id=$2
		 FOR UPDATE OF j,u,o`, requestID, organizationID).
		Scan(&request.ID, &request.OrganizationID, &request.OrganizationName, &request.UserID,
			&request.Email, &request.FirstName, &request.LastName, &request.Status,
			&request.AssignedRole, &request.RequestedAt, &request.ReviewedAt, &userActive, &organizationActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return JoinRequest{}, ErrAdminNotFound
	}
	if err != nil {
		return JoinRequest{}, err
	}
	if request.Status != "pending" || !userActive || !organizationActive {
		return JoinRequest{}, ErrAdminConflict
	}
	before := request
	if decision == "approve" {
		var existing int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_memberships WHERE user_id=$1`, request.UserID).Scan(&existing); err != nil {
			return JoinRequest{}, err
		}
		if existing != 0 {
			return JoinRequest{}, ErrAdminConflict
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_memberships (user_id,organization_id,role_key,active,created_at,updated_at)
			VALUES ($1,$2,$3,true,$4,$4)`, request.UserID, organizationID, role, now); err != nil {
			return JoinRequest{}, fmt.Errorf("auth join: grant membership: %w", err)
		}
		request.Status, request.AssignedRole = "approved", role
	} else {
		request.Status, request.AssignedRole = "rejected", ""
	}
	request.ReviewedAt = &now
	if _, err := tx.Exec(ctx, `
		UPDATE auth_join_requests
		   SET status=$2,assigned_role=NULLIF($3,''),reviewed_by=$4,reviewed_at=$5,updated_at=$5
		 WHERE id=$1`, request.ID, request.Status, request.AssignedRole, actorUserID, now); err != nil {
		return JoinRequest{}, fmt.Errorf("auth join: update request: %w", err)
	}
	if err := insertAdminAudit(ctx, tx, actorUserID, organizationID, request.UserID,
		"join_request."+request.Status, before, request, now); err != nil {
		return JoinRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return JoinRequest{}, fmt.Errorf("auth join: commit review: %w", err)
	}
	return request, nil
}

func (r *postgresRepository) RotateOrganizationJoinCode(
	ctx context.Context, actorUserID, organizationID string, digest []byte, hint string, now time.Time,
) (OrganizationJoinCode, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrganizationJoinCode{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var active bool
	if err := tx.QueryRow(ctx, `SELECT active FROM auth_organizations WHERE id=$1 FOR UPDATE`, organizationID).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
		return OrganizationJoinCode{}, ErrAdminNotFound
	} else if err != nil {
		return OrganizationJoinCode{}, err
	} else if !active {
		return OrganizationJoinCode{}, ErrAdminConflict
	}
	var before OrganizationJoinCode
	_ = tx.QueryRow(ctx, `
		SELECT organization_id,enabled,code_hint,updated_at
		  FROM auth_organization_join_codes WHERE organization_id=$1`, organizationID).
		Scan(&before.OrganizationID, &before.Enabled, &before.Hint, &before.UpdatedAt)
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_organization_join_codes
			(organization_id,code_digest,code_hint,enabled,created_at,updated_at)
		VALUES ($1,$2,$3,true,$4,$4)
		ON CONFLICT (organization_id) DO UPDATE
		SET code_digest=EXCLUDED.code_digest,code_hint=EXCLUDED.code_hint,enabled=true,updated_at=EXCLUDED.updated_at`,
		organizationID, digest, hint, now); err != nil {
		return OrganizationJoinCode{}, fmt.Errorf("auth join: rotate code: %w", err)
	}
	after := OrganizationJoinCode{OrganizationID: organizationID, Enabled: true, Hint: hint, UpdatedAt: &now}
	if err := insertAdminAudit(ctx, tx, actorUserID, organizationID, "", "join_code.rotated", before, after, now); err != nil {
		return OrganizationJoinCode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OrganizationJoinCode{}, err
	}
	return after, nil
}

func (r *postgresRepository) UpdateOrganizationJoinCode(
	ctx context.Context, actorUserID, organizationID string, enabled bool, now time.Time,
) (OrganizationJoinCode, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrganizationJoinCode{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before OrganizationJoinCode
	err = tx.QueryRow(ctx, `
		SELECT c.organization_id,c.enabled,c.code_hint,c.updated_at
		  FROM auth_organization_join_codes c
		  JOIN auth_organizations o ON o.id=c.organization_id AND o.active
		 WHERE c.organization_id=$1 FOR UPDATE OF c,o`, organizationID).
		Scan(&before.OrganizationID, &before.Enabled, &before.Hint, &before.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrganizationJoinCode{}, ErrAdminConflict
	}
	if err != nil {
		return OrganizationJoinCode{}, err
	}
	after := before
	after.Enabled, after.UpdatedAt = enabled, &now
	if _, err := tx.Exec(ctx, `UPDATE auth_organization_join_codes SET enabled=$2,updated_at=$3 WHERE organization_id=$1`,
		organizationID, enabled, now); err != nil {
		return OrganizationJoinCode{}, err
	}
	if err := insertAdminAudit(ctx, tx, actorUserID, organizationID, "", "join_code.updated", before, after, now); err != nil {
		return OrganizationJoinCode{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OrganizationJoinCode{}, err
	}
	return after, nil
}
