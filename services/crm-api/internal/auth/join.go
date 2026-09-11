package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const joinCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

var joinCodeBody = regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{12}$`)

// JoinProfile contains only applicant-owned data. The CRM role is deliberately
// absent: an authenticated organization administrator selects it at approval.
type JoinProfile struct {
	FirstName        string `json:"firstName"`
	LastName         string `json:"lastName"`
	OrganizationCode string `json:"organizationCode"`
}

type JoinRequest struct {
	ID               string     `json:"id"`
	OrganizationID   string     `json:"organizationId"`
	OrganizationName string     `json:"organizationName"`
	UserID           string     `json:"userId"`
	Email            string     `json:"email"`
	FirstName        string     `json:"firstName"`
	LastName         string     `json:"lastName"`
	Status           string     `json:"status"`
	AssignedRole     string     `json:"assignedRole,omitempty"`
	RequestedAt      time.Time  `json:"requestedAt"`
	ReviewedAt       *time.Time `json:"reviewedAt,omitempty"`
}

type joinResult struct {
	Request        JoinRequest
	ExistingMember bool
	User           User
	Organization   Organization
	Membership     Membership
}

type OrganizationJoinCode struct {
	OrganizationID string     `json:"organizationId"`
	Enabled        bool       `json:"enabled"`
	Hint           string     `json:"hint"`
	UpdatedAt      *time.Time `json:"updatedAt,omitempty"`
	Code           string     `json:"code,omitempty"`
}

type reviewJoinRequestInput struct {
	OrganizationID string `json:"organizationId,omitempty"`
	Decision       string `json:"decision"`
	Role           string `json:"role,omitempty"`
}

type updateJoinCodeInput struct {
	Enabled bool `json:"enabled"`
}

type joinAdminRepository interface {
	ListAdminJoinRequests(context.Context, string) ([]JoinRequest, error)
	ReviewAdminJoinRequest(context.Context, string, string, string, string, string, time.Time) (JoinRequest, error)
	RotateOrganizationJoinCode(context.Context, string, string, []byte, string, time.Time) (OrganizationJoinCode, error)
	UpdateOrganizationJoinCode(context.Context, string, string, bool, time.Time) (OrganizationJoinCode, error)
}

func normalizeJoinCode(value string) (string, error) {
	compact := strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(strings.TrimSpace(value)))
	if !strings.HasPrefix(compact, "SAVVY") {
		return "", errors.New("enter a valid Savvy organization code")
	}
	body := strings.TrimPrefix(compact, "SAVVY")
	if !joinCodeBody.MatchString(body) {
		return "", errors.New("enter a valid Savvy organization code")
	}
	return "SAVVY" + body, nil
}

func joinCodeDigest(value string) []byte {
	canonical, err := normalizeJoinCode(value)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256([]byte("savvy-organization-join-v1\x00" + canonical))
	return sum[:]
}

func generateJoinCode() (display, hint string, digest []byte, err error) {
	var body strings.Builder
	body.Grow(12)
	for range 12 {
		index, randomErr := rand.Int(rand.Reader, big.NewInt(int64(len(joinCodeAlphabet))))
		if randomErr != nil {
			return "", "", nil, randomErr
		}
		body.WriteByte(joinCodeAlphabet[index.Int64()])
	}
	raw := body.String()
	display = fmt.Sprintf("SAVVY-%s-%s-%s", raw[:4], raw[4:8], raw[8:])
	return display, "••••-" + raw[8:], joinCodeDigest(display), nil
}

func validateJoinProfile(profile JoinProfile) error {
	if !validAdminText(profile.FirstName, true) || !validAdminText(profile.LastName, true) {
		return errors.New("firstName and lastName are required")
	}
	if _, err := normalizeJoinCode(profile.OrganizationCode); err != nil {
		return err
	}
	return nil
}

func (s *service) joinFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidJoinCode):
		writeError(w, http.StatusBadRequest, "invalid_organization_code", ErrInvalidJoinCode.Error())
	case errors.Is(err, ErrApprovalPending):
		writeError(w, http.StatusForbidden, "approval_pending", ErrApprovalPending.Error())
	case errors.Is(err, ErrJoinConflict):
		writeError(w, http.StatusConflict, "join_conflict", ErrJoinConflict.Error())
	default:
		slog.ErrorContext(r.Context(), "organization join request failed", "err", err, "request_id", requestIDFrom(r))
		writeError(w, http.StatusInternalServerError, "internal_error", "could not submit organization access request")
	}
}

func (s *service) redirectJoinStatus(w http.ResponseWriter, r *http.Request, err error) bool {
	status := "error"
	switch {
	case errors.Is(err, ErrInvalidJoinCode):
		status = "invalid-code"
	case errors.Is(err, ErrJoinConflict):
		status = "conflict"
	case errors.Is(err, ErrApprovalPending):
		status = "pending"
	}
	return s.redirectJoinStatusValue(w, r, status)
}

func (s *service) redirectJoinStatusValue(w http.ResponseWriter, r *http.Request, status string) bool {
	if s.joinResultURL == "" {
		return false
	}
	destination, err := url.Parse(s.joinResultURL)
	if err != nil {
		return false
	}
	query := destination.Query()
	query.Set("joinStatus", status)
	destination.RawQuery = query.Encode()
	http.Redirect(w, r, destination.String(), http.StatusSeeOther)
	return true
}

func (s *service) joinAdmin(w http.ResponseWriter, r *http.Request) (adminRequest, joinAdminRepository, bool) {
	admin, ok := s.authenticateAdmin(w, r)
	if !ok {
		return adminRequest{}, nil, false
	}
	repo, ok := s.repo.(joinAdminRepository)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "organization access requests are unavailable")
		return adminRequest{}, nil, false
	}
	return admin, repo, true
}

func (s *service) rotateOrganizationJoinCode(w http.ResponseWriter, r *http.Request) {
	admin, repo, ok := s.joinAdmin(w, r)
	if !ok {
		return
	}
	organizationID := r.PathValue("organizationID")
	if target, allowed := admin.targetOrganization(organizationID); !allowed || target != organizationID {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	code, hint, digest, err := generateJoinCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not generate organization code")
		return
	}
	state, err := repo.RotateOrganizationJoinCode(r.Context(), admin.session.User.ID, organizationID, digest, hint, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	state.Code = code // Returned once; only its digest and final characters are stored.
	writeJSON(w, http.StatusOK, map[string]any{"joinCode": state})
}

func (s *service) updateOrganizationJoinCode(w http.ResponseWriter, r *http.Request) {
	admin, repo, ok := s.joinAdmin(w, r)
	if !ok {
		return
	}
	organizationID := r.PathValue("organizationID")
	if target, allowed := admin.targetOrganization(organizationID); !allowed || target != organizationID {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	var in updateJoinCodeInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	state, err := repo.UpdateOrganizationJoinCode(r.Context(), admin.session.User.ID, organizationID, in.Enabled, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"joinCode": state})
}

func (s *service) listAdminJoinRequests(w http.ResponseWriter, r *http.Request) {
	admin, repo, ok := s.joinAdmin(w, r)
	if !ok {
		return
	}
	organizationID, allowed := admin.targetOrganization(r.URL.Query().Get("organizationId"))
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	requests, err := repo.ListAdminJoinRequests(r.Context(), organizationID)
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
}

func (s *service) reviewAdminJoinRequest(w http.ResponseWriter, r *http.Request) {
	admin, repo, ok := s.joinAdmin(w, r)
	if !ok {
		return
	}
	requestID := r.PathValue("requestID")
	if !adminRecordID.MatchString(requestID) {
		writeError(w, http.StatusNotFound, "not_found", ErrAdminNotFound.Error())
		return
	}
	var in reviewJoinRequestInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	organizationID, allowed := admin.targetOrganization(in.OrganizationID)
	if !allowed {
		writeError(w, http.StatusForbidden, "permission_denied", "administrator access required")
		return
	}
	in.Decision = strings.TrimSpace(in.Decision)
	in.Role = strings.TrimSpace(in.Role)
	if in.Decision != "approve" && in.Decision != "reject" {
		writeError(w, http.StatusBadRequest, "invalid_request", "decision must be approve or reject")
		return
	}
	if in.Decision == "approve" {
		if in.Role == "" {
			in.Role = "crm_member"
		}
		if !validAdminRole(in.Role) {
			writeError(w, http.StatusBadRequest, "invalid_request", "role is invalid")
			return
		}
	} else {
		in.Role = ""
	}
	request, err := repo.ReviewAdminJoinRequest(r.Context(), admin.session.User.ID, requestID,
		organizationID, in.Decision, in.Role, s.now())
	if err != nil {
		s.adminFailure(w, r, err)
		return
	}
	s.sendJoinDecisionNotification(r.Context(), request)
	writeJSON(w, http.StatusOK, map[string]any{"request": request})
}

func (s *service) sendJoinDecisionNotification(ctx context.Context, request JoinRequest) {
	if s.mailer == nil {
		return
	}
	subject := "Your Savvy CRM access request was reviewed"
	body := fmt.Sprintf("Your request to join %s was not approved. Contact your organization administrator if you think this was a mistake.", request.OrganizationName)
	if request.Status == "approved" {
		subject = "Your Savvy CRM access is ready"
		body = fmt.Sprintf("Your request to join %s was approved. Return to Savvy CRM and log in with this email address to continue.", request.OrganizationName)
	}
	if err := s.mailer.Send(ctx, request.Email, subject, body); err != nil {
		// Approval is the source of truth and must not be rolled back because a
		// notification provider is temporarily unavailable. The user can always
		// discover the current state by logging in again.
		slog.ErrorContext(ctx, "send organization access decision email failed", "err", err)
	}
}
