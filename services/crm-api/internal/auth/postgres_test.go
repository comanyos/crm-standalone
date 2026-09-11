package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santoshguru/crm/internal/pgtest"
)

func postgresAuthRepository(t *testing.T) *postgresRepository {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo, err := newPostgresRepository(ctx, pgtest.DSN())
	if err != nil {
		pgtest.Unavailable(t, "auth postgres unavailable: %v", err)
	}
	var exists bool
	if err := repo.pool.QueryRow(ctx, `SELECT to_regclass('public.auth_users') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		repo.Close()
		pgtest.Unavailable(t, "migration 0033_simple_auth.sql is not applied")
	}
	t.Cleanup(repo.Close)
	return repo
}

func postgresAdminRepository(t *testing.T) *postgresRepository {
	t.Helper()
	repo := postgresAuthRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var migrated bool
	if err := repo.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema='public' AND table_name='auth_organizations' AND column_name='plan_key'
		) AND to_regclass('public.auth_admin_audit') IS NOT NULL
		  AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='auth_users_protect_super_admin')
		  AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='auth_memberships_protect_super_admin')
		  AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='auth_organizations_protect_super_admin')`).Scan(&migrated); err != nil || !migrated {
		pgtest.Unavailable(t, "administration migrations 0037-0038 are not applied")
	}
	return repo
}

func postgresJoinRepository(t *testing.T) *postgresRepository {
	t.Helper()
	repo := postgresAdminRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var migrated bool
	if err := repo.pool.QueryRow(ctx, `
		SELECT to_regclass('public.auth_organization_join_codes') IS NOT NULL
		   AND to_regclass('public.auth_join_requests') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		pgtest.Unavailable(t, "organization join migration 0039 is not applied")
	}
	return repo
}

func TestPostgresOrganizationCodeRequiresApprovalBeforeLogin(t *testing.T) {
	repo := postgresJoinRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := fmt.Sprintf("%d", now.UnixNano())
	adminEmail := "join-admin-" + suffix + "@example.test"
	applicantEmail := "join-applicant-" + suffix + "@example.test"
	admin, organization, _, err := repo.Signup(ctx, adminEmail, Profile{
		FirstName: "Org", LastName: "Admin", OrganizationName: "Join Test " + suffix,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_admin_audit WHERE organization_id=$1 OR actor_user_id=$2`, organization.ID, admin.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, organization.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE email_normalized=ANY($1)`, []string{adminEmail, applicantEmail})
	})

	display, hint, digest, err := generateJoinCode()
	if err != nil {
		t.Fatal(err)
	}
	codeState, err := repo.RotateOrganizationJoinCode(ctx, admin.ID, organization.ID, digest, hint, now.Add(time.Second))
	if err != nil || !codeState.Enabled || codeState.Hint != hint {
		t.Fatalf("join code=%+v err=%v", codeState, err)
	}

	profile := JoinProfile{FirstName: "Pending", LastName: "Member", OrganizationCode: display}
	requested, err := repo.RequestJoin(ctx, applicantEmail, profile, joinCodeDigest(display), now.Add(2*time.Second))
	if err != nil || requested.ExistingMember || requested.Request.Status != "pending" ||
		requested.Request.OrganizationID != organization.ID {
		t.Fatalf("join result=%+v err=%v", requested, err)
	}
	if _, _, _, err := repo.Login(ctx, applicantEmail); !errors.Is(err, ErrApprovalPending) {
		t.Fatalf("pending applicant login err=%v", err)
	}
	if _, _, _, err := repo.Signup(ctx, applicantEmail, Profile{
		FirstName: "Pending", LastName: "Member", OrganizationName: "Accidental duplicate",
	}, now.Add(2500*time.Millisecond)); !errors.Is(err, ErrApprovalPending) {
		t.Fatalf("pending applicant created a second organization: %v", err)
	}
	repeated, err := repo.RequestJoin(ctx, applicantEmail, profile, joinCodeDigest(display), now.Add(3*time.Second))
	if err != nil || repeated.Request.ID != requested.Request.ID {
		t.Fatalf("repeated request=%+v err=%v", repeated, err)
	}

	organizations, err := repo.ListAdminOrganizations(ctx, organization.ID)
	if err != nil || len(organizations) != 1 || !organizations[0].JoinCodeEnabled ||
		organizations[0].JoinCodeHint != hint || organizations[0].PendingJoinRequestCount != 1 {
		t.Fatalf("organization join summary=%+v err=%v", organizations, err)
	}
	requests, err := repo.ListAdminJoinRequests(ctx, organization.ID)
	if err != nil || len(requests) != 1 || requests[0].ID != requested.Request.ID {
		t.Fatalf("pending requests=%+v err=%v", requests, err)
	}
	approved, err := repo.ReviewAdminJoinRequest(ctx, admin.ID, requested.Request.ID, organization.ID,
		"approve", "crm_member", now.Add(4*time.Second))
	if err != nil || approved.Status != "approved" || approved.AssignedRole != "crm_member" {
		t.Fatalf("approved request=%+v err=%v", approved, err)
	}
	_, loggedInOrganization, membership, err := repo.Login(ctx, applicantEmail)
	if err != nil || loggedInOrganization.ID != organization.ID || membership.Role != "crm_member" {
		t.Fatalf("approved login org=%q role=%q err=%v", loggedInOrganization.ID, membership.Role, err)
	}
	if _, err := repo.ReviewAdminJoinRequest(ctx, admin.ID, requested.Request.ID, organization.ID,
		"approve", "crm_admin", now.Add(5*time.Second)); !errors.Is(err, ErrAdminConflict) {
		t.Fatalf("review replay err=%v", err)
	}
	if _, err := repo.UpdateOrganizationJoinCode(ctx, admin.ID, organization.ID, false, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RequestJoin(ctx, "disabled-code-"+suffix+"@example.test", profile,
		joinCodeDigest(display), now.Add(7*time.Second)); !errors.Is(err, ErrInvalidJoinCode) {
		t.Fatalf("disabled code err=%v", err)
	}
}

func TestPostgresAdministrationManagesOrganizationsPlansAndAccess(t *testing.T) {
	repo := postgresAdminRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := fmt.Sprintf("%d", now.UnixNano())
	actor, actorOrg, _, err := repo.Signup(ctx, "platform-"+suffix+"@example.test", Profile{
		FirstName: "Platform", LastName: "Admin", OrganizationName: "Platform " + suffix,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	var createdOrg AdminOrganization
	var createdAdmin AdminMember
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_admin_audit WHERE actor_user_id=$1 OR organization_id=ANY($2)`,
			actor.ID, []string{actorOrg.ID, createdOrg.ID})
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=ANY($1)`, []string{actorOrg.ID, createdOrg.ID})
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE email_normalized LIKE $1`, "%"+suffix+"@example.test")
	})

	createdOrg, createdAdmin, err = repo.CreateAdminOrganization(ctx, actor.ID, createOrganizationInput{
		Name: "Customer " + suffix, Plan: "pro", AdminEmail: "customer-admin-" + suffix + "@example.test",
		AdminFirstName: "Customer", AdminLastName: "Admin",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if createdOrg.Plan != "pro" || createdOrg.MemberCount != 1 || createdAdmin.Role != "crm_admin" {
		t.Fatalf("organization=%+v administrator=%+v", createdOrg, createdAdmin)
	}

	organizations, err := repo.ListAdminOrganizations(ctx, createdOrg.ID)
	if err != nil || len(organizations) != 1 || organizations[0].ID != createdOrg.ID {
		t.Fatalf("organizations=%+v err=%v", organizations, err)
	}
	newName := "Renamed " + suffix
	enterprise := "enterprise"
	updatedOrg, err := repo.UpdateAdminOrganization(ctx, actor.ID, createdOrg.ID,
		updateOrganizationInput{Name: &newName, Plan: &enterprise}, now.Add(2*time.Second))
	if err != nil || updatedOrg.Name != newName || updatedOrg.Plan != "enterprise" {
		t.Fatalf("updated organization=%+v err=%v", updatedOrg, err)
	}
	planSessionToken := "administration-plan-session-token-" + suffix
	if err := repo.CreateSession(ctx, createdAdmin.UserID, createdOrg.ID, tokenDigest(planSessionToken),
		now.Add(time.Hour), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, sessionOrg, _, _, err := repo.Session(ctx, tokenDigest(planSessionToken), now.Add(3*time.Second))
	if err != nil || sessionOrg.Plan != "enterprise" {
		t.Fatalf("session organization plan=%q err=%v", sessionOrg.Plan, err)
	}

	member, err := repo.CreateAdminMember(ctx, actor.ID, createMemberInput{
		OrganizationID: createdOrg.ID, Email: "member-" + suffix + "@example.test",
		FirstName: "Sales", LastName: "Member", Role: "crm_member",
	}, now.Add(3*time.Second))
	if err != nil || member.Role != "crm_member" || !member.Active {
		t.Fatalf("member=%+v err=%v", member, err)
	}
	viewer := "crm_viewer"
	inactive := false
	member, err = repo.UpdateAdminMember(ctx, actor.ID, member.UserID,
		updateMemberInput{OrganizationID: createdOrg.ID, Role: &viewer, Active: &inactive}, now.Add(4*time.Second))
	if err != nil || member.Role != viewer || member.Active {
		t.Fatalf("updated member=%+v err=%v", member, err)
	}

	manager := "crm_manager"
	if _, err := repo.UpdateAdminMember(ctx, actor.ID, createdAdmin.UserID,
		updateMemberInput{OrganizationID: createdOrg.ID, Role: &manager}, now.Add(5*time.Second)); !errors.Is(err, ErrLastOrganizationAdmin) {
		t.Fatalf("last administrator demotion err=%v", err)
	}
	if _, err := repo.CreateAdminMember(ctx, actor.ID, createMemberInput{
		OrganizationID: createdOrg.ID, Email: createdAdmin.Email, Role: manager,
	}, now.Add(5*time.Second)); !errors.Is(err, ErrAdminConflict) {
		t.Fatalf("existing membership re-grant err=%v", err)
	}

	if _, err := repo.CreateAdminMember(ctx, actor.ID, createMemberInput{
		OrganizationID: createdOrg.ID, Email: actor.Email, Role: "crm_member",
	}, now.Add(6*time.Second)); !errors.Is(err, ErrAdminConflict) {
		t.Fatalf("cross-organization grant err=%v", err)
	}

	members, err := repo.ListAdminMembers(ctx, createdOrg.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members=%+v err=%v", members, err)
	}
	users, err := repo.ListAdminUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, user := range users {
		if user.UserID == createdAdmin.UserID {
			found = user.OrganizationID == createdOrg.ID && user.OrganizationName == newName &&
				user.Plan == "enterprise" && user.Role == "crm_admin" && user.Active
		}
	}
	if !found {
		t.Fatalf("global registered-user view did not include the organization administrator: %+v", users)
	}
	var audits int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM auth_admin_audit WHERE organization_id=$1`, createdOrg.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits < 4 {
		t.Fatalf("audit events=%d, want at least 4", audits)
	}
}

func TestPostgresSuperAdminCannotBeDeletedDisabledMovedOrDowngraded(t *testing.T) {
	repo := postgresAdminRepository(t)
	ctx := context.Background()
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := fmt.Sprintf("%d", now.UnixNano())
	userID := ""
	organizationID := ""
	err = tx.QueryRow(ctx, `
		SELECT u.id,m.organization_id
		  FROM auth_users u
		  JOIN auth_memberships m ON m.user_id=u.id
		 WHERE u.email_normalized='support@savvyadvi.com'
		 ORDER BY m.active DESC,m.updated_at DESC LIMIT 1`).Scan(&userID, &organizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		userID = "usr_protected_" + suffix
		organizationID = "org_protected_" + suffix
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_users (id,email_normalized,email,first_name,last_name,created_at,updated_at)
			VALUES ($1,'support@savvyadvi.com','support@savvyadvi.com','Savvy','Support',$2,$2)`, userID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_organizations (id,name,created_at,updated_at) VALUES ($1,'Protected test',$2,$2)`,
			organizationID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_memberships (user_id,organization_id,role_key,created_at,updated_at)
			VALUES ($1,$2,'crm_admin',$3,$3)`, userID, organizationID, now); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}

	expectBlocked := func(statement string, arguments ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, "SAVEPOINT protected_change"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, statement, arguments...); err == nil {
			t.Fatalf("protected SQL unexpectedly succeeded: %s", statement)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT protected_change"); err != nil {
			t.Fatal(err)
		}
	}

	expectBlocked(`DELETE FROM auth_users WHERE id=$1`, userID)
	expectBlocked(`UPDATE auth_users SET active=false WHERE id=$1`, userID)
	expectBlocked(`DELETE FROM auth_memberships WHERE user_id=$1 AND organization_id=$2`, userID, organizationID)
	expectBlocked(`UPDATE auth_memberships SET active=false WHERE user_id=$1 AND organization_id=$2`, userID, organizationID)
	expectBlocked(`UPDATE auth_memberships SET role_key='crm_viewer' WHERE user_id=$1 AND organization_id=$2`, userID, organizationID)
	secondOrganizationID := "org_second_protected_" + suffix
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_organizations (id,name,created_at,updated_at) VALUES ($1,'Second protected test',$2,$2)`,
		secondOrganizationID, now); err != nil {
		t.Fatal(err)
	}
	expectBlocked(`
		INSERT INTO auth_memberships (user_id,organization_id,role_key,created_at,updated_at)
		VALUES ($1,$2,'crm_admin',$3,$3)`, userID, secondOrganizationID, now)
	expectBlocked(`DELETE FROM auth_organizations WHERE id=$1`, organizationID)
	expectBlocked(`UPDATE auth_organizations SET active=false WHERE id=$1`, organizationID)

	ordinaryUserID := "usr_deletable_" + suffix
	ordinaryOrgID := "org_deletable_" + suffix
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_users (id,email_normalized,email,created_at,updated_at)
		VALUES ($1,$2,$2,$3,$3)`, ordinaryUserID, "deletable-"+suffix+"@example.test", now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_organizations (id,name,created_at,updated_at) VALUES ($1,'Deletable test',$2,$2)`,
		ordinaryOrgID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_memberships (user_id,organization_id,role_key,created_at,updated_at)
		VALUES ($1,$2,'crm_admin',$3,$3)`, ordinaryUserID, ordinaryOrgID, now); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(ctx, `DELETE FROM auth_users WHERE id=$1`, ordinaryUserID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected() != 1 {
		t.Fatalf("ordinary user deletion rows=%d", result.RowsAffected())
	}
}

func TestPostgresOTPIsHashedBoundedAndSingleUse(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := fmt.Sprintf("otp-%d@example.test", now.UnixNano())
	id := fmt.Sprintf("otp_test_%d", now.UnixNano())
	digest := otpDigest([]byte("01234567890123456789012345678901"), id, "123456")
	c := challenge{ID: id, EmailNormalized: email, Flow: "login", CodeDigest: digest,
		ExpiresAt: now.Add(time.Minute), CreatedAt: now}
	if err := repo.CreateChallenge(ctx, c, 5, maxGlobalRequests, time.Hour); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = repo.pool.Exec(ctx, `DELETE FROM auth_email_challenges WHERE id=$1`, id) })
	var stored []byte
	if err := repo.pool.QueryRow(ctx, `SELECT code_digest FROM auth_email_challenges WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("123456")) {
		t.Fatal("database contains the OTP in plaintext")
	}
	if _, _, err := repo.VerifyChallenge(ctx, id, otpDigest([]byte("01234567890123456789012345678901"), id, "000000"), now); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("wrong code err=%v", err)
	}
	gotEmail, gotFlow, err := repo.VerifyChallenge(ctx, id, digest, now)
	if err != nil || gotEmail != email || gotFlow != "login" {
		t.Fatalf("verify=(%q,%q,%v)", gotEmail, gotFlow, err)
	}
	if _, _, err := repo.VerifyChallenge(ctx, id, digest, now); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatalf("replayed code err=%v", err)
	}
}

func TestPostgresGlobalIssuanceLimitSpansEmailAndOAuth(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	// Use a future isolated window so retained rows from other live tests do
	// not influence this deliberately tiny test limit.
	now := time.Now().UTC().Add(365 * 24 * time.Hour).Truncate(time.Microsecond)
	challengeID := fmt.Sprintf("otp_global_%d", now.UnixNano())
	oauthDigest := tokenDigest(fmt.Sprintf("oauth-global-%d", now.UnixNano()))
	c := challenge{
		ID: challengeID, EmailNormalized: fmt.Sprintf("global-%d@example.test", now.UnixNano()),
		Flow: "login", CodeDigest: []byte("digest"), CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := repo.CreateChallenge(ctx, c, 5, 2, time.Hour); err != nil {
		t.Fatal(err)
	}
	state := oauthState{
		Digest: oauthDigest, BindingDigest: tokenDigest("oauth-browser-binding-value"),
		Flow: "login", Nonce: "nonce", PKCEVerifier: "verifier",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := repo.CreateOAuthState(ctx, state, 2, time.Hour); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_email_challenges WHERE id=$1`, challengeID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_oauth_states WHERE state_digest=$1`, oauthDigest)
	})
	third := challenge{
		ID: challengeID + "_third", EmailNormalized: "another-global@example.test",
		Flow: "login", CodeDigest: []byte("digest"), CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := repo.CreateChallenge(ctx, third, 5, 2, time.Hour); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third cross-flow issuance err=%v, want ErrRateLimited", err)
	}
}

func TestPostgresCleanupBoundsAuthRecordsWithoutWeakeningRateWindow(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	cleanupNow := time.Now().UTC().Add(400 * 24 * time.Hour).Truncate(time.Microsecond)
	old := cleanupNow.Add(-2 * time.Hour)
	recent := cleanupNow.Add(-time.Minute)
	profile := Profile{FirstName: "Cleanup", LastName: "Test", OrganizationName: "Cleanup Tenant"}
	u, o, _, err := repo.Signup(ctx, fmt.Sprintf("cleanup-%d@example.test", cleanupNow.UnixNano()), profile, old)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE id=$1`, u.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, o.ID)
	})

	oldSession := "old-session-token-with-at-least-thirty-two-bytes"
	activeSession := "active-session-token-with-at-least-thirty-two-bytes"
	revokedSession := "revoked-session-token-with-at-least-thirty-two-bytes"
	if err := repo.CreateSession(ctx, u.ID, o.ID, tokenDigest(oldSession), old.Add(time.Minute), old); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, u.ID, o.ID, tokenDigest(activeSession), cleanupNow.Add(time.Hour), recent); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, u.ID, o.ID, tokenDigest(revokedSession), cleanupNow.Add(time.Hour), recent); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeSession(ctx, tokenDigest(revokedSession), recent); err != nil {
		t.Fatal(err)
	}

	oldChallenge := challenge{
		ID: fmt.Sprintf("cleanup_old_%d", cleanupNow.UnixNano()), EmailNormalized: "old-cleanup@example.test",
		Flow: "login", CodeDigest: []byte("old"), CreatedAt: old, ExpiresAt: old.Add(time.Minute),
	}
	recentChallenge := challenge{
		ID: fmt.Sprintf("cleanup_recent_%d", cleanupNow.UnixNano()), EmailNormalized: "recent-cleanup@example.test",
		Flow: "login", CodeDigest: []byte("recent"), CreatedAt: recent, ExpiresAt: cleanupNow.Add(time.Minute),
	}
	for _, c := range []challenge{oldChallenge, recentChallenge} {
		if err := repo.CreateChallenge(ctx, c, 5, maxGlobalRequests, time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := repo.RetireChallenge(ctx, c.ID, c.CreatedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	oldState := oauthState{
		Digest: tokenDigest("old-cleanup-oauth-state"), BindingDigest: tokenDigest("old-cleanup-oauth-binding"),
		Flow: "login", Nonce: "nonce", PKCEVerifier: "verifier", CreatedAt: old, ExpiresAt: old.Add(time.Minute),
	}
	recentState := oauthState{
		Digest: tokenDigest("recent-cleanup-oauth-state"), BindingDigest: tokenDigest("recent-cleanup-oauth-binding"),
		Flow: "login", Nonce: "nonce", PKCEVerifier: "verifier", CreatedAt: recent, ExpiresAt: cleanupNow.Add(time.Minute),
	}
	for _, state := range []oauthState{oldState, recentState} {
		if err := repo.CreateOAuthState(ctx, state, maxGlobalRequests, time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ConsumeOAuthState(ctx, state.Digest, state.BindingDigest, state.CreatedAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_email_challenges WHERE id=ANY($1)`,
			[]string{oldChallenge.ID, recentChallenge.ID})
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_oauth_states WHERE state_digest=ANY($1)`,
			[][]byte{oldState.Digest, recentState.Digest})
	})

	if err := repo.Cleanup(ctx, cleanupNow, emailRequestWindow); err != nil {
		t.Fatal(err)
	}
	assertExists := func(table, column string, value any, want bool) {
		t.Helper()
		var got bool
		query := fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s WHERE %s=$1)", table, column)
		if err := repo.pool.QueryRow(ctx, query, value).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s %s existence=%v want=%v", table, column, got, want)
		}
	}
	assertExists("auth_sessions", "token_digest", tokenDigest(oldSession), false)
	assertExists("auth_sessions", "token_digest", tokenDigest(revokedSession), false)
	assertExists("auth_sessions", "token_digest", tokenDigest(activeSession), true)
	assertExists("auth_email_challenges", "id", oldChallenge.ID, false)
	assertExists("auth_email_challenges", "id", recentChallenge.ID, true)
	assertExists("auth_oauth_states", "state_digest", oldState.Digest, false)
	assertExists("auth_oauth_states", "state_digest", recentState.Digest, true)
}

func TestPostgresSessionReloadsMembershipAndFailsClosed(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := fmt.Sprintf("session-%d@example.test", now.UnixNano())
	u, o, m, err := repo.Signup(ctx, email, Profile{FirstName: "Test", LastName: "User", OrganizationName: "Tenant"}, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE id=$1`, u.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, o.ID)
	})
	if m.Role != "crm_admin" {
		t.Fatalf("signup role=%q, want server-selected crm_admin", m.Role)
	}
	token := "a-secure-opaque-test-token-with-enough-entropy"
	if err := repo.CreateSession(ctx, u.ID, o.ID, tokenDigest(token), now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	_, gotOrg, gotMembership, _, err := repo.Session(ctx, tokenDigest(token), now)
	if err != nil || gotOrg.ID != o.ID || gotMembership.Role != "crm_admin" {
		t.Fatalf("session org=%q role=%q err=%v", gotOrg.ID, gotMembership.Role, err)
	}
	if _, err := repo.pool.Exec(ctx, `
		UPDATE auth_memberships SET role_key='crm_viewer' WHERE user_id=$1 AND organization_id=$2`, u.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	_, _, gotMembership, _, err = repo.Session(ctx, tokenDigest(token), now)
	if err != nil || gotMembership.Role != "crm_viewer" {
		t.Fatalf("role change was not reloaded: role=%q err=%v", gotMembership.Role, err)
	}
	if _, err := repo.pool.Exec(ctx, `
		UPDATE auth_memberships SET active=false WHERE user_id=$1 AND organization_id=$2`, u.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := repo.Session(ctx, tokenDigest(token), now); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("inactive membership session err=%v", err)
	}
}

func TestPostgresBootstrapIsIdempotent(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := fmt.Sprintf("bootstrap-%d@example.test", now.UnixNano())
	orgID := fmt.Sprintf("org_bootstrap_%d", now.UnixNano())
	u1, o1, err := repo.Bootstrap(ctx, email, "Savvy CRM", orgID, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE id=$1`, u1.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, o1.ID)
	})
	u2, o2, err := repo.Bootstrap(ctx, email, "Savvy CRM", orgID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if u1.ID != u2.ID || o1.ID != o2.ID || o2.ID != orgID {
		t.Fatalf("bootstrap changed identity: first=(%s,%s) second=(%s,%s)", u1.ID, o1.ID, u2.ID, o2.ID)
	}
	var role string
	if err := repo.pool.QueryRow(ctx, `
		SELECT role_key FROM auth_memberships WHERE user_id=$1 AND organization_id=$2`, u1.ID, o1.ID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "crm_admin" {
		t.Fatalf("bootstrap role=%q", role)
	}
}

func TestPostgresBootstrapNeverRestoresRevokedAuthority(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := fmt.Sprintf("bootstrap-revoked-%d@example.test", now.UnixNano())
	orgID := fmt.Sprintf("org_bootstrap_revoked_%d", now.UnixNano())
	u, o, err := repo.Bootstrap(ctx, email, "Savvy CRM", orgID, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE id=$1`, u.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, o.ID)
	})

	if _, err := repo.pool.Exec(ctx, `
		UPDATE auth_memberships SET role_key='crm_viewer',updated_at=$3
		WHERE user_id=$1 AND organization_id=$2`, u.ID, o.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Bootstrap(ctx, email, "Savvy CRM", orgID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var role string
	if err := repo.pool.QueryRow(ctx, `
		SELECT role_key FROM auth_memberships WHERE user_id=$1 AND organization_id=$2`, u.ID, o.ID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "crm_viewer" {
		t.Fatalf("restart promoted a demoted bootstrap member to %q", role)
	}

	if _, err := repo.pool.Exec(ctx, `UPDATE auth_users SET active=false WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `
		UPDATE auth_memberships SET active=false WHERE user_id=$1 AND organization_id=$2`, u.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE auth_organizations SET active=false WHERE id=$1`, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Bootstrap(ctx, email, "Savvy CRM", orgID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	var userActive, membershipActive, orgActive bool
	if err := repo.pool.QueryRow(ctx, `
		SELECT u.active,m.active,o.active,m.role_key
		FROM auth_users u
		JOIN auth_memberships m ON m.user_id=u.id
		JOIN auth_organizations o ON o.id=m.organization_id
		WHERE u.id=$1 AND o.id=$2`, u.ID, o.ID).
		Scan(&userActive, &membershipActive, &orgActive, &role); err != nil {
		t.Fatal(err)
	}
	if userActive || membershipActive || orgActive || role != "crm_viewer" {
		t.Fatalf("restart restored revoked bootstrap authority: user=%v membership=%v org=%v role=%s",
			userActive, membershipActive, orgActive, role)
	}
	if _, _, _, err := repo.Signup(ctx, email,
		Profile{FirstName: "Retry", LastName: "Signup", OrganizationName: "New Tenant"},
		now.Add(4*time.Second)); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("signup resurrected or duplicated a disabled user: %v", err)
	}
}

func TestPostgresBootstrapDoesNotGrantIntoExistingOrganization(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	orgID := fmt.Sprintf("org_preexisting_%d", now.UnixNano())
	email := fmt.Sprintf("bootstrap-preexisting-%d@example.test", now.UnixNano())
	if _, err := repo.pool.Exec(ctx, `
		INSERT INTO auth_organizations (id,name,active,created_at,updated_at)
		VALUES ($1,'Existing Organization',false,$2,$2)`, orgID, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE email_normalized=$1`, email)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, orgID)
	})
	u, o, err := repo.Bootstrap(ctx, email, "Replacement Name", orgID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "" || o.ID != orgID || o.Name != "Existing Organization" {
		t.Fatalf("bootstrap existing org result user=%+v org=%+v", u, o)
	}
	var users, memberships int
	if err := repo.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM auth_users WHERE email_normalized=$1),
		       (SELECT count(*) FROM auth_memberships WHERE organization_id=$2)`, email, orgID).
		Scan(&users, &memberships); err != nil {
		t.Fatal(err)
	}
	if users != 0 || memberships != 0 {
		t.Fatalf("bootstrap granted access to existing org: users=%d memberships=%d", users, memberships)
	}
}

func TestPostgresGoogleLinkingUsesVerifiedEmailThenDurableSubject(t *testing.T) {
	repo := postgresAuthRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	email := fmt.Sprintf("google-link-%d@example.test", now.UnixNano())
	changedEmail := fmt.Sprintf("google-renamed-%d@example.test", now.UnixNano())
	wrongOrgName := fmt.Sprintf("Wrong Tenant %d", now.UnixNano())
	profile := Profile{FirstName: "Link", LastName: "Owner", OrganizationName: "Linked Tenant"}
	u, o, _, err := repo.Signup(ctx, email, profile, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE id=$1`, u.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_users WHERE email_normalized=$1`, changedEmail)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE id=$1`, o.ID)
		_, _ = repo.pool.Exec(ctx, `DELETE FROM auth_organizations WHERE name=$1`, wrongOrgName)
	})

	unverified := externalIdentity{Subject: "google-unverified", Email: email}
	if _, _, _, err := repo.LoginExternal(ctx, "google", unverified); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("unverified Google email linked an account: %v", err)
	}

	subject := fmt.Sprintf("google-subject-%d", now.UnixNano())
	verified := externalIdentity{Subject: subject, Email: email, EmailVerified: true}
	linkedUser, linkedOrg, _, err := repo.LoginExternal(ctx, "google", verified)
	if err != nil {
		t.Fatalf("link verified local account: %v", err)
	}
	if linkedUser.ID != u.ID || linkedOrg.ID != o.ID {
		t.Fatalf("linked to (%s,%s), want (%s,%s)", linkedUser.ID, linkedOrg.ID, u.ID, o.ID)
	}

	// The provider subject, not a mutable Google email address, is the durable
	// lookup key. A later email change must keep reaching the original account
	// and must not create or take over a user for the new address.
	renamed := externalIdentity{Subject: subject, Email: changedEmail, EmailVerified: true}
	gotUser, gotOrg, _, err := repo.SignupExternal(ctx, "google", renamed,
		Profile{FirstName: "Other", LastName: "Person", OrganizationName: wrongOrgName}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("load durable subject after email change: %v", err)
	}
	if gotUser.ID != u.ID || gotOrg.ID != o.ID {
		t.Fatalf("email change moved subject to (%s,%s), want (%s,%s)", gotUser.ID, gotOrg.ID, u.ID, o.ID)
	}
	var changedExists bool
	if err := repo.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM auth_users WHERE email_normalized=$1)`, changedEmail).Scan(&changedExists); err != nil {
		t.Fatal(err)
	}
	if changedExists {
		t.Fatal("provider email change created a second CRM user")
	}
}
