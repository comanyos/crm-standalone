package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	ErrAdminNotFound         = errors.New("administration record not found")
	ErrAdminConflict         = errors.New("the requested administration change conflicts with existing access")
	ErrLastOrganizationAdmin = errors.New("an organization must keep at least one active administrator")
	ErrSuperAdminProtected   = errors.New("the Savvy super administrator is protected and cannot be changed or disabled")
	adminRecordID            = regexp.MustCompile(`^[A-Za-z0-9_-]{4,128}$`)
)

type AdminOrganization struct {
	ID                      string     `json:"id"`
	Name                    string     `json:"name"`
	Plan                    string     `json:"plan"`
	Active                  bool       `json:"active"`
	MemberCount             int        `json:"memberCount"`
	JoinCodeEnabled         bool       `json:"joinCodeEnabled"`
	JoinCodeHint            string     `json:"joinCodeHint"`
	JoinCodeUpdatedAt       *time.Time `json:"joinCodeUpdatedAt,omitempty"`
	PendingJoinRequestCount int        `json:"pendingJoinRequestCount"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

type AdminMember struct {
	UserID         string    `json:"userId"`
	OrganizationID string    `json:"organizationId"`
	Email          string    `json:"email"`
	FirstName      string    `json:"firstName"`
	LastName       string    `json:"lastName"`
	Role           string    `json:"role"`
	Active         bool      `json:"active"`
	SuperAdmin     bool      `json:"superAdmin"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type AdminUser struct {
	UserID           string    `json:"userId"`
	Email            string    `json:"email"`
	FirstName        string    `json:"firstName"`
	LastName         string    `json:"lastName"`
	OrganizationID   string    `json:"organizationId"`
	OrganizationName string    `json:"organizationName"`
	Plan             string    `json:"plan"`
	Role             string    `json:"role"`
	Active           bool      `json:"active"`
	SuperAdmin       bool      `json:"superAdmin"`
	RegisteredAt     time.Time `json:"registeredAt"`
}

type createOrganizationInput struct {
	Name           string `json:"name"`
	Plan           string `json:"plan"`
	AdminEmail     string `json:"adminEmail"`
	AdminFirstName string `json:"adminFirstName"`
	AdminLastName  string `json:"adminLastName"`
}

type updateOrganizationInput struct {
	Name   *string `json:"name,omitempty"`
	Plan   *string `json:"plan,omitempty"`
	Active *bool   `json:"active,omitempty"`
}

type createMemberInput struct {
	OrganizationID string `json:"organizationId,omitempty"`
	Email          string `json:"email"`
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	Role           string `json:"role"`
}

type updateMemberInput struct {
	OrganizationID string  `json:"organizationId,omitempty"`
	Role           *string `json:"role,omitempty"`
	Active         *bool   `json:"active,omitempty"`
	ProtectedEmail string  `json:"-"`
}

type adminRepository interface {
	ListAdminOrganizations(context.Context, string) ([]AdminOrganization, error)
	ListAdminUsers(context.Context) ([]AdminUser, error)
	CreateAdminOrganization(context.Context, string, createOrganizationInput, time.Time) (AdminOrganization, AdminMember, error)
	UpdateAdminOrganization(context.Context, string, string, updateOrganizationInput, time.Time) (AdminOrganization, error)
	ListAdminMembers(context.Context, string) ([]AdminMember, error)
	CreateAdminMember(context.Context, string, createMemberInput, time.Time) (AdminMember, error)
	UpdateAdminMember(context.Context, string, string, updateMemberInput, time.Time) (AdminMember, error)
}

type adminRequest struct {
	session  Session
	repo     adminRepository
	platform bool
}

func hasRole(roles []string, role string) bool {
	for _, candidate := range roles {
		if candidate == role {
			return true
		}
	}
	return false
}

func (s *service) authenticateAdmin(w http.ResponseWriter, r *http.Request) (adminRequest, bool) {
	env, err := s.session(r.Context(), tokenFromRequest(r, s.cookieName))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return adminRequest{}, false
	}
	platform := hasRole(env.Roles, "platform_admin")
	if !platform && !hasRole(env.Roles, "crm_admin") {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return adminRequest{}, false
	}
	repo, ok := s.repo.(adminRepository)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "administration is unavailable")
		return adminRequest{}, false
	}
	return adminRequest{session: env.Session, repo: repo, platform: platform}, true
}

func (a adminRequest) targetOrganization(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return a.session.Organization.ID, true
	}
	if !adminRecordID.MatchString(requested) {
		return "", false
	}
	if !a.platform && requested != a.session.Organization.ID {
		return "", false
	}
	return requested, true
}

func validAdminText(value string, required bool) bool {
	value = strings.TrimSpace(value)
	return (!required || value != "") && len(value) <= 160 && !strings.ContainsAny(value, "\r\n")
}

func validPlan(plan string) bool {
	return plan == "free" || plan == "pro" || plan == "enterprise"
}

func validAdminRole(role string) bool {
	_, ok := roleScopes(role)
	return ok
}

func (s *service) listAdminOrganizations(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	organizationID := ""
	if !a.platform {
		organizationID = a.session.Organization.ID
	}
	organizations, err := a.repo.ListAdminOrganizations(r.Context(), organizationID)
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	if a.platform {
		customerOrganizations := organizations[:0]
		for _, organization := range organizations {
			// The platform identity needs an internal organization anchor for
			// session tenancy, but it is not a customer workspace or plan.
			if organization.ID != a.session.Organization.ID {
				customerOrganizations = append(customerOrganizations, organization)
			}
		}
		organizations = customerOrganizations
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": organizations})
}

func (s *service) createAdminOrganization(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	if !a.platform {
		writeError(w, http.StatusForbidden, "permission_denied", "platform administrator access required")
		return
	}
	var in createOrganizationInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Plan = strings.TrimSpace(in.Plan)
	if in.Plan == "" {
		in.Plan = "free"
	}
	in.AdminFirstName = strings.TrimSpace(in.AdminFirstName)
	in.AdminLastName = strings.TrimSpace(in.AdminLastName)
	var err error
	in.AdminEmail, err = normalizeEmail(in.AdminEmail)
	if err != nil || !validAdminText(in.Name, true) || !validPlan(in.Plan) ||
		!validAdminText(in.AdminFirstName, false) || !validAdminText(in.AdminLastName, false) {
		writeError(w, http.StatusBadRequest, "invalid_request", "valid organization, plan and administrator details are required")
		return
	}
	organization, member, err := a.repo.CreateAdminOrganization(r.Context(), a.session.User.ID, in, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"organization": organization, "administrator": member})
}

func (s *service) updateAdminOrganization(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	organizationID := r.PathValue("organizationID")
	if !adminRecordID.MatchString(organizationID) || (!a.platform && organizationID != a.session.Organization.ID) {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	var in updateOrganizationInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if in.Name == nil && in.Plan == nil && in.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one organization field is required")
		return
	}
	if in.Name != nil {
		trimmed := strings.TrimSpace(*in.Name)
		in.Name = &trimmed
		if !validAdminText(trimmed, true) {
			writeError(w, http.StatusBadRequest, "invalid_request", "organization name is invalid")
			return
		}
	}
	if !a.platform && (in.Plan != nil || in.Active != nil) {
		writeError(w, http.StatusForbidden, "permission_denied", "platform administrator access required")
		return
	}
	if in.Plan != nil {
		trimmed := strings.TrimSpace(*in.Plan)
		in.Plan = &trimmed
		if !validPlan(trimmed) {
			writeError(w, http.StatusBadRequest, "invalid_request", "plan must be free, pro or enterprise")
			return
		}
	}
	if in.Active != nil && !*in.Active && organizationID == a.session.Organization.ID {
		writeError(w, http.StatusConflict, "conflict", "you cannot deactivate the organization for your active session")
		return
	}
	organization, err := a.repo.UpdateAdminOrganization(r.Context(), a.session.User.ID, organizationID, in, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization": organization})
}

func (s *service) listAdminMembers(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	organizationID, allowed := a.targetOrganization(r.URL.Query().Get("organizationId"))
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	members, err := a.repo.ListAdminMembers(r.Context(), organizationID)
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	for index := range members {
		members[index].SuperAdmin = s.isSuperAdminEmail(members[index].Email)
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *service) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	if !a.platform {
		writeError(w, http.StatusForbidden, "permission_denied", "super administrator access required")
		return
	}
	users, err := a.repo.ListAdminUsers(r.Context())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	customerUsers := users[:0]
	for _, user := range users {
		// The protected platform login is an authentication principal, not a
		// registered customer, CRM seat, or billable user.
		if !s.isSuperAdminEmail(user.Email) {
			customerUsers = append(customerUsers, user)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": customerUsers})
}

func (s *service) isSuperAdminEmail(email string) bool {
	return s.platformAdminEmail != "" && strings.EqualFold(email, s.platformAdminEmail)
}

func (s *service) createAdminMember(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	var in createMemberInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	organizationID, allowed := a.targetOrganization(in.OrganizationID)
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	in.OrganizationID = organizationID
	in.FirstName = strings.TrimSpace(in.FirstName)
	in.LastName = strings.TrimSpace(in.LastName)
	in.Role = strings.TrimSpace(in.Role)
	if in.Role == "" {
		in.Role = "crm_member"
	}
	var err error
	in.Email, err = normalizeEmail(in.Email)
	if err != nil || !validAdminRole(in.Role) || !validAdminText(in.FirstName, false) || !validAdminText(in.LastName, false) {
		writeError(w, http.StatusBadRequest, "invalid_request", "valid email, name and role are required")
		return
	}
	member, err := a.repo.CreateAdminMember(r.Context(), a.session.User.ID, in, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"member": member})
}

func (s *service) updateAdminMember(w http.ResponseWriter, r *http.Request) {
	a, ok := s.authenticateAdmin(w, r)
	if !ok {
		return
	}
	userID := r.PathValue("userID")
	if !adminRecordID.MatchString(userID) {
		writeError(w, http.StatusNotFound, "not_found", ErrAdminNotFound.Error())
		return
	}
	var in updateMemberInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	organizationID, allowed := a.targetOrganization(in.OrganizationID)
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	in.OrganizationID = organizationID
	in.ProtectedEmail = s.platformAdminEmail
	if in.Role == nil && in.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "role or active status is required")
		return
	}
	if in.Role != nil {
		trimmed := strings.TrimSpace(*in.Role)
		in.Role = &trimmed
		if !validAdminRole(trimmed) {
			writeError(w, http.StatusBadRequest, "invalid_request", "role is invalid")
			return
		}
	}
	member, err := a.repo.UpdateAdminMember(r.Context(), a.session.User.ID, userID, in, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": member})
}

func (s *service) adminFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrAdminNotFound):
		writeError(w, http.StatusNotFound, "not_found", ErrAdminNotFound.Error())
	case errors.Is(err, ErrLastOrganizationAdmin):
		writeError(w, http.StatusConflict, "last_administrator", ErrLastOrganizationAdmin.Error())
	case errors.Is(err, ErrSuperAdminProtected):
		writeError(w, http.StatusConflict, "protected_super_admin", ErrSuperAdminProtected.Error())
	case errors.Is(err, ErrAdminConflict):
		writeError(w, http.StatusConflict, "conflict", ErrAdminConflict.Error())
	default:
		slog.ErrorContext(r.Context(), "administration request failed", "err", err, "request_id", requestIDFrom(r))
		writeError(w, http.StatusInternalServerError, "internal_error", "could not complete administration request")
	}
}
