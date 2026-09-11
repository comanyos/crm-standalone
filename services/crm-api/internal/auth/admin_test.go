package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type recordingAdminRepository struct {
	*memoryRepository
	organizationFilter string
	memberFilter       string
	organizations      []AdminOrganization
	members            []AdminMember
	users              []AdminUser
	joinRequests       []JoinRequest
	reviewedRequestID  string
	reviewedDecision   string
	reviewedRole       string
}

func (r *recordingAdminRepository) ListAdminUsers(_ context.Context) ([]AdminUser, error) {
	return r.users, nil
}

func (r *recordingAdminRepository) ListAdminOrganizations(_ context.Context, filter string) ([]AdminOrganization, error) {
	r.organizationFilter = filter
	return r.organizations, nil
}

func (r *recordingAdminRepository) CreateAdminOrganization(
	_ context.Context, _ string, in createOrganizationInput, now time.Time,
) (AdminOrganization, AdminMember, error) {
	organization := AdminOrganization{ID: "org_created", Name: in.Name, Plan: in.Plan, Active: true, MemberCount: 1, CreatedAt: now, UpdatedAt: now}
	member := AdminMember{UserID: "usr_created", OrganizationID: organization.ID, Email: in.AdminEmail, Role: "crm_admin", Active: true}
	return organization, member, nil
}

func (r *recordingAdminRepository) UpdateAdminOrganization(
	_ context.Context, _ string, organizationID string, in updateOrganizationInput, now time.Time,
) (AdminOrganization, error) {
	organization := AdminOrganization{ID: organizationID, Name: "Organization", Plan: "free", Active: true, UpdatedAt: now}
	if in.Name != nil {
		organization.Name = *in.Name
	}
	if in.Plan != nil {
		organization.Plan = *in.Plan
	}
	if in.Active != nil {
		organization.Active = *in.Active
	}
	return organization, nil
}

func (r *recordingAdminRepository) ListAdminMembers(_ context.Context, organizationID string) ([]AdminMember, error) {
	r.memberFilter = organizationID
	return r.members, nil
}

func (r *recordingAdminRepository) CreateAdminMember(
	_ context.Context, _ string, in createMemberInput, now time.Time,
) (AdminMember, error) {
	return AdminMember{UserID: "usr_added", OrganizationID: in.OrganizationID, Email: in.Email,
		FirstName: in.FirstName, LastName: in.LastName, Role: in.Role, Active: true, CreatedAt: now, UpdatedAt: now}, nil
}

func (r *recordingAdminRepository) UpdateAdminMember(
	_ context.Context, _ string, userID string, in updateMemberInput, now time.Time,
) (AdminMember, error) {
	for _, member := range r.members {
		if member.UserID == userID && in.ProtectedEmail != "" && strings.EqualFold(member.Email, in.ProtectedEmail) {
			return AdminMember{}, ErrSuperAdminProtected
		}
	}
	member := AdminMember{UserID: userID, OrganizationID: in.OrganizationID, Role: "crm_member", Active: true, UpdatedAt: now}
	if in.Role != nil {
		member.Role = *in.Role
	}
	if in.Active != nil {
		member.Active = *in.Active
	}
	return member, nil
}

func (r *recordingAdminRepository) ListAdminJoinRequests(_ context.Context, organizationID string) ([]JoinRequest, error) {
	r.memberFilter = organizationID
	return r.joinRequests, nil
}

func (r *recordingAdminRepository) ReviewAdminJoinRequest(
	_ context.Context, _ string, requestID, organizationID, decision, role string, now time.Time,
) (JoinRequest, error) {
	r.reviewedRequestID, r.memberFilter, r.reviewedDecision, r.reviewedRole = requestID, organizationID, decision, role
	status := "rejected"
	if decision == "approve" {
		status = "approved"
	}
	return JoinRequest{ID: requestID, OrganizationID: organizationID, Email: "applicant@example.com",
		Status: status, AssignedRole: role, RequestedAt: now, ReviewedAt: &now}, nil
}

func (r *recordingAdminRepository) RotateOrganizationJoinCode(
	_ context.Context, _ string, organizationID string, _ []byte, hint string, now time.Time,
) (OrganizationJoinCode, error) {
	return OrganizationJoinCode{OrganizationID: organizationID, Enabled: true, Hint: hint, UpdatedAt: &now}, nil
}

func (r *recordingAdminRepository) UpdateOrganizationJoinCode(
	_ context.Context, _ string, organizationID string, enabled bool, now time.Time,
) (OrganizationJoinCode, error) {
	return OrganizationJoinCode{OrganizationID: organizationID, Enabled: enabled, Hint: "••••-ABCD", UpdatedAt: &now}, nil
}

func adminTestHandler(
	t *testing.T, email, role, platformEmail string,
) (*recordingAdminRepository, http.Handler, *http.Cookie) {
	t.Helper()
	repo := &recordingAdminRepository{memoryRepository: newMemoryRepository()}
	account := struct {
		u User
		o Organization
		m Membership
	}{
		u: User{ID: "usr_current", Email: email},
		o: Organization{ID: "org_current", Name: "Current"},
		m: Membership{Role: role},
	}
	repo.accounts[email] = account
	token := "opaque-administration-test-session-token-123456789"
	now := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC)
	if err := repo.CreateSession(context.Background(), account.u.ID, account.o.ID, tokenDigest(token), now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	svc := newService(repo, nil, nil, Config{
		OTPSecret: stringsOfLength(32), SessionTTL: time.Hour, CookieName: "savvy_session",
		BootstrapEmail: platformEmail,
	})
	svc.now = func() time.Time { return now }
	authenticator := &Authenticator{v: &sessionVerifier{service: svc}, service: svc}
	mux := http.NewServeMux()
	authenticator.RegisterRoutes(mux)
	return repo, mux, &http.Cookie{Name: "savvy_session", Value: token}
}

func stringsOfLength(length int) string {
	value := make([]byte, length)
	for index := range value {
		value[index] = 's'
	}
	return string(value)
}

func TestWorkspaceAdminControlsJoinCodeAndApprovalsOnlyForOwnOrganization(t *testing.T) {
	repo, handler, cookie := adminTestHandler(t, "admin@example.com", "crm_admin", "support@savvyadvi.com")
	repo.joinRequests = []JoinRequest{{
		ID: "join_1234", OrganizationID: "org_current", OrganizationName: "Current",
		UserID: "usr_applicant", Email: "applicant@example.com", Status: "pending",
	}}

	rotate := jsonRequest(t, handler, http.MethodPost, "/auth/admin/organizations/org_current/join-code", nil, cookie)
	if rotate.Code != http.StatusOK || !strings.Contains(rotate.Body.String(), `"code":"SAVVY-`) {
		t.Fatalf("rotate status=%d body=%s", rotate.Code, rotate.Body.String())
	}
	if strings.Contains(rotate.Body.String(), `"codeDigest"`) {
		t.Fatal("join-code response exposed its digest")
	}

	list := jsonRequest(t, handler, http.MethodGet, "/auth/admin/join-requests?organizationId=org_current", nil, cookie)
	if list.Code != http.StatusOK || repo.memberFilter != "org_current" || !strings.Contains(list.Body.String(), "applicant@example.com") {
		t.Fatalf("list status=%d filter=%q body=%s", list.Code, repo.memberFilter, list.Body.String())
	}
	approve := jsonRequest(t, handler, http.MethodPatch, "/auth/admin/join-requests/join_1234", map[string]any{
		"organizationId": "org_current", "decision": "approve", "role": "crm_manager",
	}, cookie)
	if approve.Code != http.StatusOK || repo.reviewedRequestID != "join_1234" ||
		repo.reviewedDecision != "approve" || repo.reviewedRole != "crm_manager" {
		t.Fatalf("approve status=%d request=%q decision=%q role=%q body=%s",
			approve.Code, repo.reviewedRequestID, repo.reviewedDecision, repo.reviewedRole, approve.Body.String())
	}

	forbidden := jsonRequest(t, handler, http.MethodPost, "/auth/admin/organizations/org_other/join-code", nil, cookie)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-organization rotation status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func TestPlatformAdminCanManageEveryOrganization(t *testing.T) {
	repo, handler, cookie := adminTestHandler(t, "support@savvyadvi.com", "crm_admin", "support@savvyadvi.com")
	repo.organizations = []AdminOrganization{
		{ID: "org_current", Name: "Savvy internal", Plan: "free", Active: true},
		{ID: "org_customer", Name: "Customer", Plan: "pro", Active: true},
	}
	repo.members = []AdminMember{{UserID: "usr_current", OrganizationID: "org_current", Email: "support@savvyadvi.com", Role: "crm_admin", Active: true}}
	repo.users = []AdminUser{
		{UserID: "usr_current", Email: "support@savvyadvi.com", OrganizationID: "org_current", OrganizationName: "Savvy internal", Plan: "free", Role: "crm_admin", Active: true},
		{UserID: "usr_customer", Email: "owner@example.com", OrganizationID: "org_customer", OrganizationName: "Customer", Plan: "pro", Role: "crm_admin", Active: true},
	}

	list := jsonRequest(t, handler, http.MethodGet, "/auth/admin/organizations", nil, cookie)
	if list.Code != http.StatusOK || repo.organizationFilter != "" || strings.Contains(list.Body.String(), "Savvy internal") ||
		!strings.Contains(list.Body.String(), "Customer") {
		t.Fatalf("list status=%d filter=%q body=%s", list.Code, repo.organizationFilter, list.Body.String())
	}
	create := jsonRequest(t, handler, http.MethodPost, "/auth/admin/organizations", map[string]any{
		"name": "Customer", "plan": "pro", "adminEmail": "owner@example.com",
	}, cookie)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	users := jsonRequest(t, handler, http.MethodGet, "/auth/admin/users", nil, cookie)
	if users.Code != http.StatusOK || strings.Contains(users.Body.String(), "support@savvyadvi.com") ||
		!strings.Contains(users.Body.String(), "owner@example.com") {
		t.Fatalf("users status=%d body=%s", users.Code, users.Body.String())
	}
	deactivate := jsonRequest(t, handler, http.MethodPatch, "/auth/admin/members/usr_current", map[string]any{
		"organizationId": "org_current", "active": false,
	}, cookie)
	if deactivate.Code != http.StatusConflict || !strings.Contains(deactivate.Body.String(), "protected_super_admin") {
		t.Fatalf("protected update status=%d body=%s", deactivate.Code, deactivate.Body.String())
	}
}

func TestWorkspaceAdminIsRestrictedToCurrentOrganization(t *testing.T) {
	repo, handler, cookie := adminTestHandler(t, "owner@example.com", "crm_admin", "support@savvyadvi.com")
	repo.organizations = []AdminOrganization{{ID: "org_current", Name: "Current", Plan: "free", Active: true}}

	list := jsonRequest(t, handler, http.MethodGet, "/auth/admin/organizations", nil, cookie)
	if list.Code != http.StatusOK || repo.organizationFilter != "org_current" {
		t.Fatalf("list status=%d filter=%q", list.Code, repo.organizationFilter)
	}
	other := jsonRequest(t, handler, http.MethodGet, "/auth/admin/members?organizationId=org_other", nil, cookie)
	if other.Code != http.StatusForbidden || repo.memberFilter != "" {
		t.Fatalf("cross-organization status=%d filter=%q", other.Code, repo.memberFilter)
	}
	own := jsonRequest(t, handler, http.MethodGet, "/auth/admin/members", nil, cookie)
	if own.Code != http.StatusOK || repo.memberFilter != "org_current" {
		t.Fatalf("own organization status=%d filter=%q", own.Code, repo.memberFilter)
	}
	createOrganization := jsonRequest(t, handler, http.MethodPost, "/auth/admin/organizations", map[string]any{
		"name": "Other", "plan": "pro", "adminEmail": "other@example.com",
	}, cookie)
	if createOrganization.Code != http.StatusForbidden {
		t.Fatalf("workspace admin create organization status=%d", createOrganization.Code)
	}
	users := jsonRequest(t, handler, http.MethodGet, "/auth/admin/users", nil, cookie)
	if users.Code != http.StatusForbidden {
		t.Fatalf("workspace admin global users status=%d", users.Code)
	}
}

func TestOrdinaryMemberCannotUseAdministrationAPI(t *testing.T) {
	_, handler, cookie := adminTestHandler(t, "member@example.com", "crm_member", "support@savvyadvi.com")
	response := jsonRequest(t, handler, http.MethodGet, "/auth/admin/organizations", nil, cookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("member status=%d body=%s", response.Code, response.Body.String())
	}
}
