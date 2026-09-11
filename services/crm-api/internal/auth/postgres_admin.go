package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *postgresRepository) ListAdminOrganizations(
	ctx context.Context, organizationID string,
) ([]AdminOrganization, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.name, o.plan_key, o.active,
		       count(m.user_id) FILTER (WHERE m.active AND u.active),
		       COALESCE(c.enabled,false),COALESCE(c.code_hint,''),c.updated_at,
		       (SELECT count(*) FROM auth_join_requests j
		         WHERE j.organization_id=o.id AND j.status='pending'),
		       o.created_at, o.updated_at
		  FROM auth_organizations o
		  LEFT JOIN auth_memberships m ON m.organization_id = o.id
		  LEFT JOIN auth_users u ON u.id = m.user_id
		  LEFT JOIN auth_organization_join_codes c ON c.organization_id=o.id
		 WHERE ($1 = '' OR o.id = $1)
		 GROUP BY o.id,c.organization_id,c.enabled,c.code_hint,c.updated_at
		 ORDER BY o.active DESC, lower(o.name), o.id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("auth admin: list organizations: %w", err)
	}
	defer rows.Close()
	organizations := make([]AdminOrganization, 0)
	for rows.Next() {
		var organization AdminOrganization
		if err := rows.Scan(&organization.ID, &organization.Name, &organization.Plan, &organization.Active,
			&organization.MemberCount, &organization.JoinCodeEnabled, &organization.JoinCodeHint,
			&organization.JoinCodeUpdatedAt, &organization.PendingJoinRequestCount,
			&organization.CreatedAt, &organization.UpdatedAt); err != nil {
			return nil, fmt.Errorf("auth admin: scan organization: %w", err)
		}
		organizations = append(organizations, organization)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth admin: list organizations: %w", err)
	}
	return organizations, nil
}

func (r *postgresRepository) ListAdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT u.id,u.email,u.first_name,u.last_name,
		       COALESCE(m.organization_id,''),COALESCE(o.name,''),
		       COALESCE(o.plan_key,'unassigned'),COALESCE(m.role_key,''),
		       u.active AND COALESCE(m.active,false) AND COALESCE(o.active,false),
		       u.created_at
		  FROM auth_users u
		  LEFT JOIN LATERAL (
			SELECT organization_id,role_key,active,updated_at
			  FROM auth_memberships
			 WHERE user_id=u.id
			 ORDER BY active DESC,updated_at DESC,organization_id
			 LIMIT 1
		  ) m ON true
		  LEFT JOIN auth_organizations o ON o.id=m.organization_id
		 ORDER BY u.created_at DESC,lower(u.email),u.id`)
	if err != nil {
		return nil, fmt.Errorf("auth admin: list registered users: %w", err)
	}
	defer rows.Close()
	users := make([]AdminUser, 0)
	for rows.Next() {
		var user AdminUser
		if err := rows.Scan(&user.UserID, &user.Email, &user.FirstName, &user.LastName,
			&user.OrganizationID, &user.OrganizationName, &user.Plan, &user.Role,
			&user.Active, &user.RegisteredAt); err != nil {
			return nil, fmt.Errorf("auth admin: scan registered user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth admin: list registered users: %w", err)
	}
	return users, nil
}

func (r *postgresRepository) CreateAdminOrganization(
	ctx context.Context, actorUserID string, in createOrganizationInput, now time.Time,
) (AdminOrganization, AdminMember, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminOrganization{}, AdminMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, in.AdminEmail); err != nil {
		return AdminOrganization{}, AdminMember{}, err
	}

	var user User
	var userActive bool
	err = tx.QueryRow(ctx, `
		SELECT id, email, first_name, last_name, active
		  FROM auth_users WHERE email_normalized=$1 FOR UPDATE`, in.AdminEmail).
		Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &userActive)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		user.ID, err = newRecordID("usr")
		if err != nil {
			return AdminOrganization{}, AdminMember{}, err
		}
		user.Email, user.FirstName, user.LastName = in.AdminEmail, in.AdminFirstName, in.AdminLastName
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_users
				(id,email_normalized,email,first_name,last_name,created_at,updated_at)
			VALUES ($1,$2,$2,$3,$4,$5,$5)`,
			user.ID, in.AdminEmail, in.AdminFirstName, in.AdminLastName, now); err != nil {
			return AdminOrganization{}, AdminMember{}, fmt.Errorf("auth admin: create organization admin: %w", err)
		}
	case err != nil:
		return AdminOrganization{}, AdminMember{}, fmt.Errorf("auth admin: find organization admin: %w", err)
	case !userActive:
		return AdminOrganization{}, AdminMember{}, ErrAdminConflict
	default:
		var existing bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM auth_memberships WHERE user_id=$1 AND active)`, user.ID).Scan(&existing); err != nil {
			return AdminOrganization{}, AdminMember{}, err
		}
		if existing {
			return AdminOrganization{}, AdminMember{}, ErrAdminConflict
		}
		if _, err := tx.Exec(ctx, `
			UPDATE auth_users
			   SET first_name=CASE WHEN first_name='' THEN $2 ELSE first_name END,
			       last_name=CASE WHEN last_name='' THEN $3 ELSE last_name END,
			       updated_at=$4
			 WHERE id=$1`, user.ID, in.AdminFirstName, in.AdminLastName, now); err != nil {
			return AdminOrganization{}, AdminMember{}, err
		}
		if user.FirstName == "" {
			user.FirstName = in.AdminFirstName
		}
		if user.LastName == "" {
			user.LastName = in.AdminLastName
		}
	}

	organizationID, err := newRecordID("org")
	if err != nil {
		return AdminOrganization{}, AdminMember{}, err
	}
	organization := AdminOrganization{ID: organizationID, Name: in.Name, Plan: in.Plan, Active: true,
		MemberCount: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_organizations (id,name,plan_key,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$4)`, organization.ID, organization.Name, organization.Plan, now); err != nil {
		return AdminOrganization{}, AdminMember{}, fmt.Errorf("auth admin: create organization: %w", err)
	}
	member := AdminMember{UserID: user.ID, OrganizationID: organization.ID, Email: user.Email,
		FirstName: user.FirstName, LastName: user.LastName, Role: "crm_admin", Active: true,
		CreatedAt: now, UpdatedAt: now}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_memberships
			(user_id,organization_id,role_key,created_at,updated_at)
		VALUES ($1,$2,'crm_admin',$3,$3)`, user.ID, organization.ID, now); err != nil {
		return AdminOrganization{}, AdminMember{}, fmt.Errorf("auth admin: create administrator membership: %w", err)
	}
	if err := insertAdminAudit(ctx, tx, actorUserID, organization.ID, user.ID,
		"organization.created", nil, organization, now); err != nil {
		return AdminOrganization{}, AdminMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminOrganization{}, AdminMember{}, fmt.Errorf("auth admin: commit organization: %w", err)
	}
	return organization, member, nil
}

func (r *postgresRepository) UpdateAdminOrganization(
	ctx context.Context, actorUserID, organizationID string, in updateOrganizationInput, now time.Time,
) (AdminOrganization, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminOrganization{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var before AdminOrganization
	err = tx.QueryRow(ctx, `
		SELECT id,name,plan_key,active,created_at,updated_at
		  FROM auth_organizations WHERE id=$1 FOR UPDATE`, organizationID).
		Scan(&before.ID, &before.Name, &before.Plan, &before.Active, &before.CreatedAt, &before.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminOrganization{}, ErrAdminNotFound
	}
	if err != nil {
		return AdminOrganization{}, fmt.Errorf("auth admin: load organization: %w", err)
	}
	after := before
	if in.Name != nil {
		after.Name = *in.Name
	}
	if in.Plan != nil {
		after.Plan = *in.Plan
	}
	if in.Active != nil {
		after.Active = *in.Active
	}
	after.UpdatedAt = now
	if _, err := tx.Exec(ctx, `
		UPDATE auth_organizations SET name=$2,plan_key=$3,active=$4,updated_at=$5 WHERE id=$1`,
		after.ID, after.Name, after.Plan, after.Active, now); err != nil {
		return AdminOrganization{}, fmt.Errorf("auth admin: update organization: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM auth_memberships m JOIN auth_users u ON u.id=m.user_id
		 WHERE m.organization_id=$1 AND m.active AND u.active`, organizationID).Scan(&after.MemberCount); err != nil {
		return AdminOrganization{}, err
	}
	before.MemberCount = after.MemberCount
	if err := insertAdminAudit(ctx, tx, actorUserID, organizationID, "",
		"organization.updated", before, after, now); err != nil {
		return AdminOrganization{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminOrganization{}, fmt.Errorf("auth admin: commit organization update: %w", err)
	}
	return after, nil
}

func (r *postgresRepository) ListAdminMembers(ctx context.Context, organizationID string) ([]AdminMember, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_organizations WHERE id=$1)`, organizationID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrAdminNotFound
	}
	rows, err := r.pool.Query(ctx, `
		SELECT u.id,m.organization_id,u.email,u.first_name,u.last_name,m.role_key,
		       m.active AND u.active,m.created_at,m.updated_at
		  FROM auth_memberships m
		  JOIN auth_users u ON u.id=m.user_id
		 WHERE m.organization_id=$1
		 ORDER BY m.active DESC, lower(u.first_name), lower(u.last_name), lower(u.email)`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("auth admin: list members: %w", err)
	}
	defer rows.Close()
	members := make([]AdminMember, 0)
	for rows.Next() {
		var member AdminMember
		if err := rows.Scan(&member.UserID, &member.OrganizationID, &member.Email, &member.FirstName,
			&member.LastName, &member.Role, &member.Active, &member.CreatedAt, &member.UpdatedAt); err != nil {
			return nil, fmt.Errorf("auth admin: scan member: %w", err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

func (r *postgresRepository) CreateAdminMember(
	ctx context.Context, actorUserID string, in createMemberInput, now time.Time,
) (AdminMember, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, in.Email); err != nil {
		return AdminMember{}, err
	}
	var organizationActive bool
	if err := tx.QueryRow(ctx, `SELECT active FROM auth_organizations WHERE id=$1 FOR UPDATE`, in.OrganizationID).
		Scan(&organizationActive); errors.Is(err, pgx.ErrNoRows) {
		return AdminMember{}, ErrAdminNotFound
	} else if err != nil {
		return AdminMember{}, err
	} else if !organizationActive {
		return AdminMember{}, ErrAdminConflict
	}

	var user User
	var userActive bool
	err = tx.QueryRow(ctx, `
		SELECT id,email,first_name,last_name,active FROM auth_users
		 WHERE email_normalized=$1 FOR UPDATE`, in.Email).
		Scan(&user.ID, &user.Email, &user.FirstName, &user.LastName, &userActive)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		user.ID, err = newRecordID("usr")
		if err != nil {
			return AdminMember{}, err
		}
		user.Email, user.FirstName, user.LastName = in.Email, in.FirstName, in.LastName
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_users
				(id,email_normalized,email,first_name,last_name,created_at,updated_at)
			VALUES ($1,$2,$2,$3,$4,$5,$5)`, user.ID, in.Email, in.FirstName, in.LastName, now); err != nil {
			return AdminMember{}, fmt.Errorf("auth admin: create member identity: %w", err)
		}
	case err != nil:
		return AdminMember{}, fmt.Errorf("auth admin: find member identity: %w", err)
	case !userActive:
		return AdminMember{}, ErrAdminConflict
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE auth_users
			   SET first_name=CASE WHEN first_name='' THEN $2 ELSE first_name END,
			       last_name=CASE WHEN last_name='' THEN $3 ELSE last_name END,
			       updated_at=$4 WHERE id=$1`, user.ID, in.FirstName, in.LastName, now); err != nil {
			return AdminMember{}, err
		}
		if user.FirstName == "" {
			user.FirstName = in.FirstName
		}
		if user.LastName == "" {
			user.LastName = in.LastName
		}
	}

	rows, err := tx.Query(ctx, `SELECT organization_id,active FROM auth_memberships WHERE user_id=$1 FOR UPDATE`, user.ID)
	if err != nil {
		return AdminMember{}, err
	}
	existed := false
	for rows.Next() {
		var orgID string
		var active bool
		if err := rows.Scan(&orgID, &active); err != nil {
			rows.Close()
			return AdminMember{}, err
		}
		if orgID == in.OrganizationID {
			existed = true
		} else if active {
			rows.Close()
			return AdminMember{}, ErrAdminConflict
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return AdminMember{}, err
	}
	rows.Close()
	// Grant is intentionally create-only. Existing memberships, including
	// disabled ones, must go through UpdateAdminMember so last-administrator
	// protection and before/after audit state cannot be bypassed by resubmitting
	// an address through the "Add user" form.
	if existed {
		return AdminMember{}, ErrAdminConflict
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_memberships
			(user_id,organization_id,role_key,active,created_at,updated_at)
		VALUES ($1,$2,$3,true,$4,$4)`,
		user.ID, in.OrganizationID, in.Role, now); err != nil {
		return AdminMember{}, fmt.Errorf("auth admin: grant membership: %w", err)
	}
	member := AdminMember{UserID: user.ID, OrganizationID: in.OrganizationID, Email: user.Email,
		FirstName: user.FirstName, LastName: user.LastName, Role: in.Role, Active: true,
		CreatedAt: now, UpdatedAt: now}
	if err := insertAdminAudit(ctx, tx, actorUserID, in.OrganizationID, user.ID,
		"membership.created", nil, member, now); err != nil {
		return AdminMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminMember{}, fmt.Errorf("auth admin: commit membership: %w", err)
	}
	return member, nil
}

func (r *postgresRepository) UpdateAdminMember(
	ctx context.Context, actorUserID, userID string, in updateMemberInput, now time.Time,
) (AdminMember, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize membership changes for the organization. Without this lock,
	// two administrators could concurrently demote each other after both saw
	// the other as the remaining administrator.
	var organizationExists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM auth_organizations WHERE id=$1 FOR UPDATE`, in.OrganizationID).
		Scan(&organizationExists); errors.Is(err, pgx.ErrNoRows) {
		return AdminMember{}, ErrAdminNotFound
	} else if err != nil {
		return AdminMember{}, err
	}
	var before AdminMember
	var userActive bool
	err = tx.QueryRow(ctx, `
		SELECT u.id,m.organization_id,u.email,u.first_name,u.last_name,m.role_key,
		       m.active,u.active,m.created_at,m.updated_at
		  FROM auth_memberships m JOIN auth_users u ON u.id=m.user_id
		 WHERE m.user_id=$1 AND m.organization_id=$2
		 FOR UPDATE OF m,u`, userID, in.OrganizationID).
		Scan(&before.UserID, &before.OrganizationID, &before.Email, &before.FirstName, &before.LastName,
			&before.Role, &before.Active, &userActive, &before.CreatedAt, &before.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminMember{}, ErrAdminNotFound
	}
	if err != nil {
		return AdminMember{}, fmt.Errorf("auth admin: load member: %w", err)
	}
	if !userActive {
		return AdminMember{}, ErrAdminConflict
	}
	if in.ProtectedEmail != "" && strings.EqualFold(before.Email, in.ProtectedEmail) {
		return AdminMember{}, ErrSuperAdminProtected
	}
	after := before
	if in.Role != nil {
		after.Role = *in.Role
	}
	if in.Active != nil {
		after.Active = *in.Active
	}
	after.UpdatedAt = now
	if before.Active && before.Role == "crm_admin" && (!after.Active || after.Role != "crm_admin") {
		var otherAdmins int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM auth_memberships m JOIN auth_users u ON u.id=m.user_id
			 WHERE m.organization_id=$1 AND m.user_id<>$2 AND m.role_key='crm_admin'
			   AND m.active AND u.active`, in.OrganizationID, userID).Scan(&otherAdmins); err != nil {
			return AdminMember{}, err
		}
		if otherAdmins == 0 {
			return AdminMember{}, ErrLastOrganizationAdmin
		}
	}
	if !before.Active && after.Active {
		var otherActive int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM auth_memberships
			 WHERE user_id=$1 AND organization_id<>$2 AND active`, userID, in.OrganizationID).Scan(&otherActive); err != nil {
			return AdminMember{}, err
		}
		if otherActive > 0 {
			return AdminMember{}, ErrAdminConflict
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_memberships SET role_key=$3,active=$4,updated_at=$5
		 WHERE user_id=$1 AND organization_id=$2`, userID, in.OrganizationID, after.Role, after.Active, now); err != nil {
		return AdminMember{}, fmt.Errorf("auth admin: update membership: %w", err)
	}
	if err := insertAdminAudit(ctx, tx, actorUserID, in.OrganizationID, userID,
		"membership.updated", before, after, now); err != nil {
		return AdminMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminMember{}, fmt.Errorf("auth admin: commit membership update: %w", err)
	}
	return after, nil
}

func insertAdminAudit(
	ctx context.Context, tx pgx.Tx, actorUserID, organizationID, targetUserID, action string,
	before, after any, now time.Time,
) error {
	id, err := newRecordID("aud")
	if err != nil {
		return err
	}
	encode := func(value any) (any, error) {
		if value == nil {
			return nil, nil
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(data), nil
	}
	beforeJSON, err := encode(before)
	if err != nil {
		return err
	}
	afterJSON, err := encode(after)
	if err != nil {
		return err
	}
	var target any
	if targetUserID != "" {
		target = targetUserID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO auth_admin_audit
			(id,actor_user_id,organization_id,target_user_id,action,before_state,after_state,created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8)`,
		id, actorUserID, organizationID, target, action, beforeJSON, afterJSON, now)
	if err != nil {
		return fmt.Errorf("auth admin: write audit event: %w", err)
	}
	return nil
}
