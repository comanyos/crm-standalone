package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// NewPostgres constructs the CRM's first-party authenticator and applies the
// optional idempotent bootstrap. Schema migration 0033 must already be
// applied by the normal migration runner.
func NewPostgres(ctx context.Context, raw Config) (*Authenticator, error) {
	cfg, err := normalizeConfig(raw)
	if err != nil {
		return nil, err
	}
	repo, err := newPostgresRepository(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	var sender mailer
	if strings.TrimSpace(cfg.SMTP.Host) != "" {
		sender, err = newSMTPMailer(cfg.SMTP)
		if err != nil {
			repo.Close()
			return nil, err
		}
	}
	var google googleProvider
	if cfg.GoogleClientID != "" {
		google, err = newGoogleOIDC(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL)
		if err != nil {
			repo.Close()
			return nil, err
		}
	}
	svc := newService(repo, sender, google, cfg)
	a := &Authenticator{v: &sessionVerifier{service: svc}, service: svc}
	if cfg.BootstrapEmail != "" {
		if _, _, err := repo.Bootstrap(ctx, cfg.BootstrapEmail, cfg.BootstrapOrgName, cfg.BootstrapOrgID, svc.now()); err != nil {
			repo.Close()
			return nil, fmt.Errorf("auth: bootstrap: %w", err)
		}
	}
	return a, nil
}

func newService(repo repository, sender mailer, google googleProvider, cfg Config) *service {
	return &service{
		repo: repo, mailer: sender, google: google,
		platformAdminEmail: cfg.BootstrapEmail,
		otpSecret:          []byte(cfg.OTPSecret), otpTTL: cfg.OTPTTL,
		sessionTTL: cfg.SessionTTL, oauthTTL: defaultOAuthTTL,
		cookieName: cfg.CookieName, cookieSecure: cfg.CookieSecure,
		devOTP: cfg.DevOTP, successURL: strings.TrimRight(cfg.LoginSuccessURL, "/"),
		joinResultURL: strings.TrimRight(cfg.JoinResultURL, "/"),
		now:           func() time.Time { return time.Now().UTC() },
	}
}

type sessionVerifier struct{ service *service }

func (v *sessionVerifier) verify(ctx context.Context, token string) (*Principal, error) {
	envelope, err := v.service.session(ctx, token)
	if err != nil {
		// Authentication failures are intentionally indistinguishable at the
		// API boundary; database details must never become a Connect error.
		return nil, ErrInvalidSession
	}
	return &Principal{
		Subject: envelope.User.ID, TenantID: envelope.Organization.ID,
		OrgID: envelope.Organization.ID, Roles: envelope.Roles, Scopes: envelope.Scopes,
	}, nil
}

func (v *sessionVerifier) ready(ctx context.Context) error { return v.service.repo.Ping(ctx) }

func (s *service) session(ctx context.Context, token string) (sessionEnvelope, error) {
	s.maybeCleanup(ctx)
	if len(token) < 32 || len(token) > 512 {
		return sessionEnvelope{}, ErrInvalidSession
	}
	u, o, m, expires, err := s.repo.Session(ctx, tokenDigest(token), s.now())
	if err != nil {
		return sessionEnvelope{}, err
	}
	return s.envelope(u, o, m, expires, "")
}

func (s *service) issueSession(
	ctx context.Context, u User, o Organization, m Membership,
) (sessionEnvelope, error) {
	token, err := randomToken(32)
	if err != nil {
		return sessionEnvelope{}, fmt.Errorf("auth: generate session: %w", err)
	}
	now := s.now()
	expires := now.Add(s.sessionTTL)
	if err := s.repo.CreateSession(ctx, u.ID, o.ID, tokenDigest(token), expires, now); err != nil {
		return sessionEnvelope{}, err
	}
	return s.envelope(u, o, m, expires, token)
}

func (s *service) envelope(
	u User, o Organization, m Membership, expires time.Time, token string,
) (sessionEnvelope, error) {
	env, err := envelope(u, o, m, expires, token)
	if err != nil {
		return sessionEnvelope{}, err
	}
	if s.platformAdminEmail != "" && strings.EqualFold(u.Email, s.platformAdminEmail) {
		// The recovery-level operator is intentionally a separate product
		// surface, not an all-powerful CRM user. Keep the membership only as the
		// active-session anchor and expose no tenant record scopes.
		env.Roles = []string{"platform_admin"}
		env.Scopes = []string{}
	}
	return env, nil
}

// RegisterRoutes installs the public JSON authentication surface. Calling it
// on a disabled or test authenticator is a no-op, which keeps isolated domain
// tests able to use Disabled without pretending a public login service exists.
func (a *Authenticator) RegisterRoutes(mux *http.ServeMux) {
	if a == nil || a.service == nil {
		return
	}
	mux.HandleFunc("POST /auth/email/request", a.service.requestEmail)
	mux.HandleFunc("POST /auth/email/verify", a.service.verifyEmail)
	mux.HandleFunc("GET /auth/capabilities", a.service.capabilities)
	mux.HandleFunc("GET /auth/session", a.service.getSession)
	mux.HandleFunc("POST /auth/logout", a.service.logout)
	mux.HandleFunc("GET /auth/google/start", a.service.googleStart)
	mux.HandleFunc("GET /auth/google/callback", a.service.googleCallback)
	mux.HandleFunc("POST /auth/admin/organizations/{organizationID}/join-code", a.service.rotateOrganizationJoinCode)
	mux.HandleFunc("PATCH /auth/admin/organizations/{organizationID}/join-code", a.service.updateOrganizationJoinCode)
	mux.HandleFunc("GET /auth/admin/join-requests", a.service.listAdminJoinRequests)
	mux.HandleFunc("PATCH /auth/admin/join-requests/{requestID}", a.service.reviewAdminJoinRequest)
	mux.HandleFunc("GET /auth/admin/organizations", a.service.listAdminOrganizations)
	mux.HandleFunc("POST /auth/admin/organizations", a.service.createAdminOrganization)
	mux.HandleFunc("PATCH /auth/admin/organizations/{organizationID}", a.service.updateAdminOrganization)
	mux.HandleFunc("GET /auth/admin/users", a.service.listAdminUsers)
	mux.HandleFunc("GET /auth/admin/members", a.service.listAdminMembers)
	mux.HandleFunc("POST /auth/admin/members", a.service.createAdminMember)
	mux.HandleFunc("PATCH /auth/admin/members/{userID}", a.service.updateAdminMember)
}

func (s *service) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"google": s.google != nil})
}

func (a *Authenticator) Close() {
	if a != nil && a.service != nil {
		a.service.close()
	}
}

func (s *service) close() {
	s.closeOnce.Do(func() {
		if closer, ok := s.google.(interface{ Close() }); ok {
			closer.Close()
		}
		s.repo.Close()
	})
}

type requestEmailInput struct {
	Email string `json:"email"`
	Flow  string `json:"flow"`
}

func (s *service) requestEmail(w http.ResponseWriter, r *http.Request) {
	s.maybeCleanup(r.Context())
	var in requestEmailInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	email, err := normalizeEmail(in.Email)
	if err != nil || !validFlow(in.Flow) {
		writeError(w, http.StatusBadRequest, "invalid_request", "email and flow (login, signup or join) are required")
		return
	}
	if s.mailer == nil && s.devOTP == "" {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", ErrLoginUnavailable.Error())
		return
	}
	code := s.devOTP
	if code == "" {
		code, err = generateOTP()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not create verification code")
			return
		}
	}
	id, err := newRecordID("otp")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create verification code")
		return
	}
	now := s.now()
	c := challenge{ID: id, EmailNormalized: email, Flow: in.Flow,
		CodeDigest: otpDigest(s.otpSecret, id, code), ExpiresAt: now.Add(s.otpTTL), CreatedAt: now}
	if err := s.repo.CreateChallenge(r.Context(), c, maxEmailRequests, maxGlobalRequests, emailRequestWindow); err != nil {
		if errors.Is(err, ErrRateLimited) {
			w.Header().Set("Retry-After", "900")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
			return
		}
		slog.Error("create login challenge failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create verification code")
		return
	}
	if s.mailer != nil {
		body := fmt.Sprintf("Your Savvy CRM verification code is %s. It expires in %d minutes.\n\nIf you did not request this, you can ignore this email.", code, int(s.otpTTL.Minutes()))
		if err := s.mailer.Send(r.Context(), email, "Your Savvy CRM verification code", body); err != nil {
			_ = s.repo.RetireChallenge(r.Context(), id, s.now())
			// SMTP errors contain the failed protocol stage and provider reply,
			// but never the generated OTP, message body, or configured password.
			// Keeping that detail is necessary to distinguish network, TLS,
			// authentication, envelope, and DATA failures in production.
			slog.Error("send login email failed", "err", err, "request_id", requestIDFrom(r))
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", ErrLoginUnavailable.Error())
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"challengeId": id, "expiresInSeconds": int(s.otpTTL.Seconds()),
	})
}

type verifyEmailInput struct {
	ChallengeID string       `json:"challengeId"`
	Code        string       `json:"code"`
	Profile     *Profile     `json:"profile,omitempty"`
	Join        *JoinProfile `json:"join,omitempty"`
}

func (s *service) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var in verifyEmailInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(in.ChallengeID) < 16 || len(in.ChallengeID) > 128 || !sixDigits.MatchString(in.Code) {
		writeError(w, http.StatusUnauthorized, "invalid_code", ErrInvalidChallenge.Error())
		return
	}
	boundFlow, err := s.repo.ChallengeFlow(r.Context(), in.ChallengeID, s.now())
	if err != nil {
		if errors.Is(err, ErrInvalidChallenge) {
			writeError(w, http.StatusUnauthorized, "invalid_code", ErrInvalidChallenge.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not verify code")
		return
	}
	// Validate onboarding before consuming a valid one-time credential. A
	// typo in the signup form must be correctable with the same OTP.
	if boundFlow == "signup" {
		if in.Profile == nil {
			writeError(w, http.StatusBadRequest, "profile_required", "profile is required for signup")
			return
		}
		if err := validateProfile(*in.Profile); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_profile", err.Error())
			return
		}
	} else if boundFlow == "join" {
		if in.Join == nil {
			writeError(w, http.StatusBadRequest, "join_profile_required", "name and organization code are required")
			return
		}
		if err := validateJoinProfile(*in.Join); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_join_request", err.Error())
			return
		}
	}
	// The digest is bound to the unguessable challenge id. Email and flow are
	// returned only by the atomic consume, so the client cannot restate or
	// switch either after receiving the code.
	email, flow, err := s.repo.VerifyChallenge(r.Context(), in.ChallengeID,
		otpDigest(s.otpSecret, in.ChallengeID, in.Code), s.now())
	if err != nil {
		if errors.Is(err, ErrInvalidChallenge) {
			writeError(w, http.StatusUnauthorized, "invalid_code", ErrInvalidChallenge.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not verify code")
		return
	}

	var u User
	var o Organization
	var m Membership
	if flow == "join" {
		result, joinErr := s.repo.RequestJoin(r.Context(), email, *in.Join,
			joinCodeDigest(in.Join.OrganizationCode), s.now())
		if joinErr != nil {
			s.joinFailure(w, r, joinErr)
			return
		}
		if result.ExistingMember {
			s.finishLogin(w, r, result.User, result.Organization, result.Membership)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "pending", "request": result.Request,
		})
		return
	}
	if flow == "login" {
		u, o, m, err = s.repo.Login(r.Context(), email)
	} else {
		u, o, m, err = s.repo.Signup(r.Context(), email, *in.Profile, s.now())
	}
	if err != nil {
		if errors.Is(err, ErrApprovalPending) {
			writeError(w, http.StatusForbidden, "approval_pending", ErrApprovalPending.Error())
			return
		}
		if errors.Is(err, ErrNoAccount) {
			writeError(w, http.StatusUnauthorized, "account_not_found", ErrNoAccount.Error())
			return
		}
		slog.Error("finish email login failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "could not finish login")
		return
	}
	s.finishLogin(w, r, u, o, m)
}

func (s *service) finishLogin(w http.ResponseWriter, r *http.Request, u User, o Organization, m Membership) {
	env, err := s.issueSession(r.Context(), u, o, m)
	if err != nil {
		slog.Error("create login session failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create session")
		return
	}
	s.setCookie(w, env.token, env.ExpiresAt)
	writeJSON(w, http.StatusOK, env.Session)
}

func (s *service) getSession(w http.ResponseWriter, r *http.Request) {
	token := tokenFromRequest(r, s.cookieName)
	env, err := s.session(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, env.Session)
}

func (s *service) logout(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	now := s.now()
	for _, token := range []string{bearer(r.Header.Get("Authorization")), cookieToken(r, s.cookieName)} {
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		if err := s.repo.RevokeSession(r.Context(), tokenDigest(token), now); err != nil {
			// Do not clear the browser credential unless the database has
			// confirmed revocation. Otherwise a transient outage would make the
			// UI claim logout succeeded while the server-side session remained
			// usable.
			slog.ErrorContext(r.Context(), "revoke login session failed", "request_id", requestIDFrom(r))
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "could not log out; try again")
			return
		}
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) googleStart(w http.ResponseWriter, r *http.Request) {
	s.maybeCleanup(r.Context())
	if s.google == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", ErrOAuthUnavailable.Error())
		return
	}
	flow := r.URL.Query().Get("flow")
	if !validFlow(flow) {
		writeError(w, http.StatusBadRequest, "invalid_request", "flow must be login, signup or join")
		return
	}
	orgName := strings.TrimSpace(r.URL.Query().Get("organizationName"))
	jobRole := strings.TrimSpace(r.URL.Query().Get("jobRole"))
	salesTeamSize := strings.TrimSpace(r.URL.Query().Get("salesTeamSize"))
	joinCode := strings.TrimSpace(r.URL.Query().Get("organizationCode"))
	if flow == "signup" {
		if err := validateOAuthSignupMetadata(orgName, jobRole, salesTeamSize); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_profile", err.Error())
			return
		}
	} else if flow == "join" {
		if _, err := normalizeJoinCode(joinCode); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_join_request", err.Error())
			return
		}
		orgName, jobRole, salesTeamSize = "", "", ""
	} else {
		orgName, jobRole, salesTeamSize = "", "", ""
		joinCode = ""
	}
	stateToken, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google login")
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google login")
		return
	}
	verifier, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google login")
		return
	}
	bindingToken, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google login")
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	now := s.now()
	state := oauthState{
		Digest: tokenDigest(stateToken), BindingDigest: tokenDigest(bindingToken),
		Flow: flow, Nonce: nonce, PKCEVerifier: verifier,
		OrganizationName: orgName, JobRole: jobRole,
		SalesTeamSize: salesTeamSize, JoinCodeDigest: joinCodeDigest(joinCode),
		ExpiresAt: now.Add(s.oauthTTL), CreatedAt: now,
	}
	if err := s.repo.CreateOAuthState(r.Context(), state, maxGlobalRequests, emailRequestWindow); err != nil {
		if errors.Is(err, ErrRateLimited) {
			w.Header().Set("Retry-After", "900")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again later")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google login")
		return
	}
	s.setOAuthBindingCookie(w, stateToken, bindingToken, state.ExpiresAt)
	http.Redirect(w, r, s.google.AuthorizationURL(stateToken, nonce,
		base64.RawURLEncoding.EncodeToString(challenge[:])), http.StatusFound)
}

func (s *service) googleCallback(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", ErrOAuthUnavailable.Error())
		return
	}
	stateToken, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	if len(stateToken) < 32 || len(stateToken) > 512 {
		writeError(w, http.StatusBadRequest, "invalid_oauth_callback", "Google callback is incomplete")
		return
	}
	if code == "" || len(code) > 4096 {
		s.clearOAuthBindingCookie(w, stateToken)
		writeError(w, http.StatusBadRequest, "invalid_oauth_callback", "Google callback is incomplete")
		return
	}
	bindingCookie, cookieErr := r.Cookie(s.oauthBindingCookieName(stateToken))
	if cookieErr != nil || len(bindingCookie.Value) < 32 || len(bindingCookie.Value) > 512 {
		s.clearOAuthBindingCookie(w, stateToken)
		writeError(w, http.StatusUnauthorized, "invalid_oauth_state", ErrOAuthState.Error())
		return
	}
	state, err := s.repo.ConsumeOAuthState(r.Context(), tokenDigest(stateToken),
		tokenDigest(bindingCookie.Value), s.now())
	if err != nil {
		s.clearOAuthBindingCookie(w, stateToken)
		writeError(w, http.StatusUnauthorized, "invalid_oauth_state", ErrOAuthState.Error())
		return
	}
	s.clearOAuthBindingCookie(w, stateToken)
	identity, err := s.google.Exchange(r.Context(), code, state.PKCEVerifier)
	if err != nil || identity.Subject == "" || !identity.EmailVerified ||
		subtle.ConstantTimeCompare([]byte(identity.Nonce), []byte(state.Nonce)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_google_identity", "Google identity could not be verified")
		return
	}
	identity.Email, err = normalizeEmail(identity.Email)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_google_identity", "Google identity could not be verified")
		return
	}
	var u User
	var o Organization
	var m Membership
	if state.Flow == "login" {
		u, o, m, err = s.repo.LoginExternal(r.Context(), "google", identity)
	} else if state.Flow == "join" {
		if !validAdminText(identity.FirstName, true) || !validAdminText(identity.LastName, true) {
			writeError(w, http.StatusUnauthorized, "invalid_google_identity", "Google profile name is required to request organization access")
			return
		}
		result, joinErr := s.repo.RequestJoinExternal(r.Context(), "google", identity,
			JoinProfile{FirstName: identity.FirstName, LastName: identity.LastName}, state.JoinCodeDigest, s.now())
		if joinErr != nil {
			if s.redirectJoinStatus(w, r, joinErr) {
				return
			}
			s.joinFailure(w, r, joinErr)
			return
		}
		if !result.ExistingMember {
			if s.redirectJoinStatusValue(w, r, "pending") {
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"status": "pending", "request": result.Request})
			return
		}
		u, o, m = result.User, result.Organization, result.Membership
	} else {
		profile := Profile{FirstName: identity.FirstName, LastName: identity.LastName,
			OrganizationName: state.OrganizationName, JobRole: state.JobRole, SalesTeamSize: state.SalesTeamSize}
		if err = validateProfile(profile); err == nil {
			u, o, m, err = s.repo.SignupExternal(r.Context(), "google", identity, profile, s.now())
		}
	}
	if err != nil {
		if errors.Is(err, ErrApprovalPending) && s.redirectJoinStatusValue(w, r, "pending") {
			return
		}
		if errors.Is(err, ErrApprovalPending) {
			writeError(w, http.StatusForbidden, "approval_pending", ErrApprovalPending.Error())
			return
		}
		if errors.Is(err, ErrNoAccount) {
			writeError(w, http.StatusUnauthorized, "account_not_found", ErrNoAccount.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not finish Google login")
		return
	}
	env, err := s.issueSession(r.Context(), u, o, m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create session")
		return
	}
	s.setCookie(w, env.token, env.ExpiresAt)
	if s.successURL != "" {
		http.Redirect(w, r, s.successURL, http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, env.Session)
}

func tokenFromRequest(r *http.Request, cookieName string) string {
	if token := bearer(r.Header.Get("Authorization")); token != "" {
		return token
	}
	return cookieToken(r, cookieName)
}

func cookieToken(r *http.Request, cookieName string) string {
	if cookie, err := r.Cookie(cookieName); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func tokenFromHeader(h http.Header, cookieName string) string {
	if token := bearer(h.Get("Authorization")); token != "" {
		return token
	}
	request := &http.Request{Header: h}
	if cookie, err := request.Cookie(cookieName); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func (s *service) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	s.writeAuthCookie(w, s.cookieName, token, "/", expires, max(1, int(s.sessionTTL.Seconds())))
}

func (s *service) clearCookie(w http.ResponseWriter) {
	s.writeAuthCookie(w, s.cookieName, "", "/", time.Unix(1, 0), -1)
}

func (s *service) oauthBindingCookieName(stateToken string) string {
	digest := tokenDigest(stateToken)
	return s.cookieName + "_oauth_" + base64.RawURLEncoding.EncodeToString(digest[:9])
}

func (s *service) setOAuthBindingCookie(w http.ResponseWriter, stateToken, bindingToken string, expires time.Time) {
	s.writeAuthCookie(w, s.oauthBindingCookieName(stateToken), bindingToken,
		"/auth/google/callback", expires, max(1, int(s.oauthTTL.Seconds())))
}

func (s *service) clearOAuthBindingCookie(w http.ResponseWriter, stateToken string) {
	s.writeAuthCookie(w, s.oauthBindingCookieName(stateToken), "",
		"/auth/google/callback", time.Unix(1, 0), -1)
}

func (s *service) writeAuthCookie(
	w http.ResponseWriter, name, value, path string, expires time.Time, maxAge int,
) {
	// Secure is intentionally runtime-configurable solely so HTTP localhost
	// works in development. Config validation requires it in staging and
	// production; every auth cookie is always HttpOnly and SameSite=Lax.
	// #nosec G124 -- Secure is false only for validated local HTTP development.
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path,
		HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: maxAge,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

func requestIDFrom(r *http.Request) string { return r.Header.Get("X-Request-ID") }

func (s *service) maybeCleanup(ctx context.Context) {
	now := s.now()
	s.cleanupMu.Lock()
	if !s.nextCleanup.IsZero() && now.Before(s.nextCleanup) {
		s.cleanupMu.Unlock()
		return
	}
	// Reserve the interval before doing I/O so concurrent requests cannot all
	// launch the same database sweep.
	s.nextCleanup = now.Add(cleanupInterval)
	s.cleanupMu.Unlock()
	if err := s.repo.Cleanup(ctx, now, emailRequestWindow); err != nil {
		slog.WarnContext(ctx, "authentication record cleanup failed")
		// A transient failure should retry before another full interval, while
		// still avoiding a cleanup attempt on every incoming request.
		s.cleanupMu.Lock()
		s.nextCleanup = now.Add(5 * time.Minute)
		s.cleanupMu.Unlock()
	}
}
