package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryRepository struct {
	mu         sync.Mutex
	challenges map[string]challenge
	consumed   map[string]bool
	accounts   map[string]struct {
		u User
		o Organization
		m Membership
	}
	sessions map[string]struct {
		u       User
		o       Organization
		m       Membership
		expires time.Time
		revoked bool
	}
	states         map[string]oauthState
	consumedStates map[string]bool
	verifyCalls    int
	revokeErr      error
	joinResult     joinResult
	joinErr        error
	joinEmail      string
	joinProfile    JoinProfile
	joinDigest     []byte
	closed         bool
	closeCalls     int
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{challenges: map[string]challenge{}, consumed: map[string]bool{},
		accounts: map[string]struct {
			u User
			o Organization
			m Membership
		}{},
		sessions: map[string]struct {
			u       User
			o       Organization
			m       Membership
			expires time.Time
			revoked bool
		}{}, states: map[string]oauthState{}, consumedStates: map[string]bool{}}
}

func (m *memoryRepository) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("repository closed")
	}
	return nil
}
func (m *memoryRepository) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.closeCalls++
}
func (m *memoryRepository) CreateChallenge(_ context.Context, c challenge, perEmailLimit, globalLimit int, window time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	global, perEmail := 0, 0
	for _, existing := range m.challenges {
		if existing.CreatedAt.After(c.CreatedAt.Add(-window)) {
			global++
			if existing.EmailNormalized == c.EmailNormalized {
				perEmail++
			}
		}
	}
	for _, existing := range m.states {
		if existing.CreatedAt.After(c.CreatedAt.Add(-window)) {
			global++
		}
	}
	if global >= globalLimit || perEmail >= perEmailLimit {
		return ErrRateLimited
	}
	m.challenges[c.ID] = c
	return nil
}
func (m *memoryRepository) RetireChallenge(_ context.Context, id string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consumed[id] = true
	return nil
}
func (m *memoryRepository) ChallengeFlow(_ context.Context, id string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[id]
	if !ok || m.consumed[id] || !c.ExpiresAt.After(now) {
		return "", ErrInvalidChallenge
	}
	return c.Flow, nil
}
func (m *memoryRepository) VerifyChallenge(_ context.Context, id string, digest []byte, now time.Time) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifyCalls++
	c, ok := m.challenges[id]
	if !ok || m.consumed[id] || !c.ExpiresAt.After(now) || !hmac.Equal(c.CodeDigest, digest) {
		return "", "", ErrInvalidChallenge
	}
	m.consumed[id] = true
	return c.EmailNormalized, c.Flow, nil
}
func (m *memoryRepository) Login(_ context.Context, email string) (User, Organization, Membership, error) {
	a, ok := m.accounts[email]
	if !ok {
		return User{}, Organization{}, Membership{}, ErrNoAccount
	}
	return a.u, a.o, a.m, nil
}
func (m *memoryRepository) Signup(_ context.Context, email string, p Profile, _ time.Time) (User, Organization, Membership, error) {
	if a, ok := m.accounts[email]; ok {
		return a.u, a.o, a.m, nil
	}
	a := struct {
		u User
		o Organization
		m Membership
	}{
		u: User{ID: "usr_1", Email: email, FirstName: p.FirstName, LastName: p.LastName},
		o: Organization{ID: "org_1", Name: p.OrganizationName},
		m: Membership{Role: "crm_admin", JobRole: p.JobRole, SalesTeamSize: p.SalesTeamSize},
	}
	m.accounts[email] = a
	return a.u, a.o, a.m, nil
}
func digestKey(d []byte) string { return base64.RawURLEncoding.EncodeToString(d) }
func (m *memoryRepository) CreateSession(_ context.Context, userID, orgID string, digest []byte, expires, _ time.Time) error {
	for _, a := range m.accounts {
		if a.u.ID == userID && a.o.ID == orgID {
			m.sessions[digestKey(digest)] = struct {
				u       User
				o       Organization
				m       Membership
				expires time.Time
				revoked bool
			}{a.u, a.o, a.m, expires, false}
			return nil
		}
	}
	return errors.New("account missing")
}
func (m *memoryRepository) Session(_ context.Context, digest []byte, now time.Time) (User, Organization, Membership, time.Time, error) {
	s, ok := m.sessions[digestKey(digest)]
	if !ok || s.revoked || !s.expires.After(now) {
		return User{}, Organization{}, Membership{}, time.Time{}, ErrInvalidSession
	}
	return s.u, s.o, s.m, s.expires, nil
}
func (m *memoryRepository) RevokeSession(_ context.Context, digest []byte, _ time.Time) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	k := digestKey(digest)
	s := m.sessions[k]
	s.revoked = true
	m.sessions[k] = s
	return nil
}
func (m *memoryRepository) Bootstrap(_ context.Context, email, orgName, _ string, now time.Time) (User, Organization, error) {
	p := Profile{FirstName: "Support", LastName: "Admin", OrganizationName: orgName}
	u, o, _, err := m.Signup(context.Background(), email, p, now)
	return u, o, err
}
func (m *memoryRepository) CreateOAuthState(_ context.Context, s oauthState, globalLimit int, window time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	global := 0
	for _, existing := range m.challenges {
		if existing.CreatedAt.After(s.CreatedAt.Add(-window)) {
			global++
		}
	}
	for _, existing := range m.states {
		if existing.CreatedAt.After(s.CreatedAt.Add(-window)) {
			global++
		}
	}
	if global >= globalLimit {
		return ErrRateLimited
	}
	m.states[digestKey(s.Digest)] = s
	return nil
}
func (m *memoryRepository) ConsumeOAuthState(_ context.Context, d, binding []byte, now time.Time) (oauthState, error) {
	k := digestKey(d)
	s, ok := m.states[k]
	if !ok || m.consumedStates[k] || !s.ExpiresAt.After(now) || !hmac.Equal(s.BindingDigest, binding) {
		return oauthState{}, ErrOAuthState
	}
	m.consumedStates[k] = true
	return s, nil
}
func (m *memoryRepository) LoginExternal(ctx context.Context, _ string, i externalIdentity) (User, Organization, Membership, error) {
	return m.Login(ctx, i.Email)
}
func (m *memoryRepository) SignupExternal(ctx context.Context, _ string, i externalIdentity, p Profile, now time.Time) (User, Organization, Membership, error) {
	return m.Signup(ctx, i.Email, p, now)
}
func (m *memoryRepository) RequestJoin(_ context.Context, email string, profile JoinProfile, digest []byte, _ time.Time) (joinResult, error) {
	m.joinEmail, m.joinProfile, m.joinDigest = email, profile, append([]byte(nil), digest...)
	return m.joinResult, m.joinErr
}
func (m *memoryRepository) RequestJoinExternal(_ context.Context, _ string, identity externalIdentity, profile JoinProfile, digest []byte, _ time.Time) (joinResult, error) {
	m.joinEmail, m.joinProfile, m.joinDigest = identity.Email, profile, append([]byte(nil), digest...)
	return m.joinResult, m.joinErr
}
func (m *memoryRepository) Cleanup(_ context.Context, now time.Time, challengeRetention time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.challenges {
		if c.CreatedAt.Before(now.Add(-challengeRetention)) && (!c.ExpiresAt.After(now) || m.consumed[id]) {
			delete(m.challenges, id)
			delete(m.consumed, id)
		}
	}
	for key, state := range m.states {
		if state.CreatedAt.Before(now.Add(-challengeRetention)) &&
			(!state.ExpiresAt.After(now) || m.consumedStates[key]) {
			delete(m.states, key)
			delete(m.consumedStates, key)
		}
	}
	for key, session := range m.sessions {
		if session.revoked || !session.expires.After(now) {
			delete(m.sessions, key)
		}
	}
	return nil
}

type captureMailer struct {
	to, subject, body string
	err               error
}

func (m *captureMailer) Send(_ context.Context, to, subject, body string) error {
	m.to, m.subject, m.body = to, subject, body
	return m.err
}

func testAuth(t *testing.T, repo *memoryRepository, sender mailer, google googleProvider) (*Authenticator, http.Handler) {
	t.Helper()
	cfg := Config{OTPSecret: strings.Repeat("s", 32), OTPTTL: 10 * time.Minute,
		SessionTTL: time.Hour, CookieName: "savvy_session", DevOTP: "123456"}
	svc := newService(repo, sender, google, cfg)
	svc.now = func() time.Time { return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC) }
	a := &Authenticator{v: &sessionVerifier{service: svc}, service: svc}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	return a, mux
}

func jsonRequest(t *testing.T, h http.Handler, method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var raw bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&raw).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &raw)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthCookiesPreserveSecurityAttributes(t *testing.T) {
	svc := &service{
		cookieName: "savvy_session", cookieSecure: true,
		sessionTTL: time.Hour, oauthTTL: 10 * time.Minute,
	}
	expires := time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC)

	sessionResponse := httptest.NewRecorder()
	svc.setCookie(sessionResponse, "opaque-session-token", expires)
	sessionCookies := sessionResponse.Result().Cookies()
	if len(sessionCookies) != 1 || !sessionCookies[0].Secure || !sessionCookies[0].HttpOnly ||
		sessionCookies[0].SameSite != http.SameSiteLaxMode || sessionCookies[0].Path != "/" {
		t.Fatalf("session cookie security attributes=%+v", sessionCookies)
	}

	oauthResponse := httptest.NewRecorder()
	svc.setOAuthBindingCookie(oauthResponse, strings.Repeat("s", 43), "browser-binding", expires)
	oauthCookies := oauthResponse.Result().Cookies()
	if len(oauthCookies) != 1 || !oauthCookies[0].Secure || !oauthCookies[0].HttpOnly ||
		oauthCookies[0].SameSite != http.SameSiteLaxMode || oauthCookies[0].Path != "/auth/google/callback" {
		t.Fatalf("OAuth cookie security attributes=%+v", oauthCookies)
	}
}

func TestBootstrapIdentityGetsOnlyThePlatformRoleAndNoCRMScopes(t *testing.T) {
	svc := &service{platformAdminEmail: "support@savvyadvi.com"}
	expires := time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC)
	base := Membership{Role: "crm_admin"}

	admin, err := svc.envelope(
		User{ID: "usr_support", Email: "Support@SavvyAdvi.com"},
		Organization{ID: "org_savvy", Name: "Savvy CRM"}, base, expires, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(admin.Roles, []string{"platform_admin"}) || len(admin.Scopes) != 0 ||
		admin.Membership.Role != "crm_admin" {
		t.Fatalf("admin roles=%v scopes=%v membership=%q", admin.Roles, admin.Scopes, admin.Membership.Role)
	}

	member, err := svc.envelope(
		User{ID: "usr_other", Email: "person@example.com"},
		Organization{ID: "org_savvy", Name: "Savvy CRM"}, base, expires, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(member.Roles, "platform_admin") {
		t.Fatalf("ordinary member received platform role: %v", member.Roles)
	}
}

func requestChallenge(t *testing.T, h http.Handler, email, flow string) string {
	t.Helper()
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/request", map[string]string{"email": email, "flow": flow})
	if w.Code != http.StatusAccepted {
		t.Fatalf("request status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		ChallengeID string `json:"challengeId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ChallengeID == "" {
		t.Fatal("missing challengeId")
	}
	return out.ChallengeID
}

func TestEmailSignupIssuesOpaqueSessionAndCookie(t *testing.T) {
	repo := newMemoryRepository()
	mail := &captureMailer{}
	a, h := testAuth(t, repo, mail, nil)
	id := requestChallenge(t, h, "Person@Example.com", "signup")
	if mail.to != "person@example.com" || !strings.Contains(mail.body, "123456") {
		t.Fatalf("mail=%+v", mail)
	}
	if bytes.Contains(repo.challenges[id].CodeDigest, []byte("123456")) {
		t.Fatal("OTP was stored in plaintext")
	}
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]any{
		"challengeId": id, "code": "123456", "profile": map[string]string{
			"firstName": "Ada", "lastName": "Lovelace", "organizationName": "Analytical Engines",
			"jobRole": "CEO", "salesTeamSize": "0-10",
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", w.Code, w.Body.String())
	}
	var out Session
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Organization.ID != "org_1" || out.Membership.Role != "crm_admin" {
		t.Fatalf("response=%+v", out)
	}
	if len(out.Scopes) != 1 || out.Scopes[0] != "crm:*" {
		t.Fatalf("scopes=%v", out.Scopes)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie=%+v", cookies)
	}

	byCookie := jsonRequest(t, h, http.MethodGet, "/auth/session", nil, cookies[0])
	if byCookie.Code != http.StatusOK {
		t.Fatalf("cookie session=%d %s", byCookie.Code, byCookie.Body.String())
	}
	if strings.Contains(w.Body.String(), "sessionToken") {
		t.Fatal("verification response exposed an HttpOnly session token")
	}
	rawRequest := httptest.NewRequest(http.MethodGet, "/protected-export", nil)
	rawRequest.AddCookie(cookies[0])
	ctx, err := a.AuthenticateRequest(context.Background(), rawRequest)
	if err != nil {
		t.Fatalf("raw HTTP cookie auth: %v", err)
	}
	if principal, ok := FromContext(ctx); !ok || principal.TenantID != "org_1" || principal.Subject != "usr_1" {
		t.Fatalf("raw HTTP principal=%+v ok=%v", principal, ok)
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	r.Header.Set("Authorization", "Bearer "+cookies[0].Value)
	bearerResponse := httptest.NewRecorder()
	h.ServeHTTP(bearerResponse, r)
	if bearerResponse.Code != http.StatusOK {
		t.Fatalf("bearer session=%d", bearerResponse.Code)
	}
	if strings.Contains(bearerResponse.Body.String(), "sessionToken") {
		t.Fatal("GET /auth/session leaked token")
	}
}

func TestInvalidSignupProfileDoesNotConsumeOTP(t *testing.T) {
	repo := newMemoryRepository()
	_, h := testAuth(t, repo, &captureMailer{}, nil)
	id := requestChallenge(t, h, "a@example.com", "signup")
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]any{"challengeId": id, "code": "123456", "profile": map[string]string{"firstName": "A"}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.verifyCalls != 0 || repo.consumed[id] {
		t.Fatal("invalid onboarding consumed the OTP")
	}
	w = jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]any{"challengeId": id, "code": "123456", "profile": map[string]string{"firstName": "A", "lastName": "B", "organizationName": "Org"}})
	if w.Code != http.StatusOK {
		t.Fatalf("corrected profile status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestEmailJoinCreatesPendingRequestWithoutSession(t *testing.T) {
	repo := newMemoryRepository()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	repo.joinResult = joinResult{Request: JoinRequest{
		ID: "join_pending_1234", OrganizationID: "org_existing", OrganizationName: "Existing Org",
		UserID: "usr_applicant", Email: "member@example.com", FirstName: "New", LastName: "Member",
		Status: "pending", RequestedAt: now,
	}}
	_, h := testAuth(t, repo, &captureMailer{}, nil)
	id := requestChallenge(t, h, "Member@Example.com", "join")
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]any{
		"challengeId": id,
		"code":        "123456",
		"join": map[string]string{
			"firstName": "New", "lastName": "Member", "organizationCode": "savvy-abcd-efgh-jklm",
		},
	})
	if w.Code != http.StatusAccepted || len(w.Result().Cookies()) != 0 {
		t.Fatalf("join status=%d cookies=%v body=%s", w.Code, w.Result().Cookies(), w.Body.String())
	}
	if repo.joinEmail != "member@example.com" || repo.joinProfile.FirstName != "New" ||
		!hmac.Equal(repo.joinDigest, joinCodeDigest("SAVVY-ABCD-EFGH-JKLM")) {
		t.Fatalf("join input email=%q profile=%+v digest=%x", repo.joinEmail, repo.joinProfile, repo.joinDigest)
	}
	if strings.Contains(string(repo.joinDigest), "ABCD") || strings.Contains(w.Body.String(), "organizationCode") {
		t.Fatal("join response or persistence exposed the organization code")
	}
}

func TestInvalidJoinProfileDoesNotConsumeOTP(t *testing.T) {
	repo := newMemoryRepository()
	_, h := testAuth(t, repo, &captureMailer{}, nil)
	id := requestChallenge(t, h, "member@example.com", "join")
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]any{
		"challengeId": id, "code": "123456",
		"join": map[string]string{"firstName": "New", "lastName": "Member", "organizationCode": "short"},
	})
	if w.Code != http.StatusBadRequest || repo.verifyCalls != 0 || repo.consumed[id] {
		t.Fatalf("invalid join status=%d verifyCalls=%d consumed=%v body=%s",
			w.Code, repo.verifyCalls, repo.consumed[id], w.Body.String())
	}
}

func TestEmailChallengeIsSingleUseAndLoginDoesNotCreateAccount(t *testing.T) {
	repo := newMemoryRepository()
	_, h := testAuth(t, repo, &captureMailer{}, nil)
	id := requestChallenge(t, h, "missing@example.com", "login")
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]string{"challengeId": id, "code": "123456"})
	if w.Code != http.StatusUnauthorized || len(repo.accounts) != 0 {
		t.Fatalf("unknown login status=%d accounts=%d", w.Code, len(repo.accounts))
	}
	w = jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]string{"challengeId": id, "code": "123456"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d", w.Code)
	}
}

func TestLogoutRevokesBearerAndClearsCookie(t *testing.T) {
	repo := newMemoryRepository()
	repo.accounts["u@example.com"] = struct {
		u User
		o Organization
		m Membership
	}{User{ID: "u", Email: "u@example.com"}, Organization{ID: "o", Name: "O"}, Membership{Role: "crm_viewer"}}
	_, h := testAuth(t, repo, &captureMailer{}, nil)
	id := requestChallenge(t, h, "u@example.com", "login")
	w := jsonRequest(t, h, http.MethodPost, "/auth/email/verify", map[string]string{"challengeId": id, "code": "123456"})
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("login did not set a session cookie")
	}
	secondID := requestChallenge(t, h, "u@example.com", "login")
	second := jsonRequest(t, h, http.MethodPost, "/auth/email/verify",
		map[string]string{"challengeId": secondID, "code": "123456"})
	secondCookies := second.Result().Cookies()
	if len(secondCookies) != 1 {
		t.Fatal("second login did not set a session cookie")
	}
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer "+cookies[0].Value)
	r.AddCookie(secondCookies[0])
	logout := httptest.NewRecorder()
	h.ServeHTTP(logout, r)
	if logout.Code != http.StatusNoContent || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout=%d cookies=%+v", logout.Code, logout.Result().Cookies())
	}
	r = httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	r.Header.Set("Authorization", "Bearer "+cookies[0].Value)
	after := httptest.NewRecorder()
	h.ServeHTTP(after, r)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d", after.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	r.Header.Set("Authorization", "Bearer "+secondCookies[0].Value)
	after = httptest.NewRecorder()
	h.ServeHTTP(after, r)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("cookie credential supplied alongside bearer was not revoked: status=%d", after.Code)
	}
}

func TestLogoutDoesNotClearCookieWhenRevocationFails(t *testing.T) {
	repo := newMemoryRepository()
	repo.accounts["u@example.com"] = struct {
		u User
		o Organization
		m Membership
	}{User{ID: "u", Email: "u@example.com"}, Organization{ID: "o", Name: "O"}, Membership{Role: "crm_viewer"}}
	_, h := testAuth(t, repo, &captureMailer{}, nil)
	id := requestChallenge(t, h, "u@example.com", "login")
	login := jsonRequest(t, h, http.MethodPost, "/auth/email/verify",
		map[string]string{"challengeId": id, "code": "123456"})
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("login did not set a session cookie")
	}

	repo.revokeErr = errors.New("database unavailable")
	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	request.AddCookie(cookies[0])
	logout := httptest.NewRecorder()
	h.ServeHTTP(logout, request)
	if logout.Code != http.StatusServiceUnavailable {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
	}
	if got := logout.Result().Cookies(); len(got) != 0 {
		t.Fatalf("failed logout cleared or replaced cookie: %+v", got)
	}
	if strings.Contains(logout.Body.String(), repo.revokeErr.Error()) {
		t.Fatal("logout exposed repository error")
	}

	// The caller can safely retry because the browser credential was not
	// cleared and the repository never confirmed its revocation.
	repo.revokeErr = nil
	sessionRequest := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	sessionRequest.AddCookie(cookies[0])
	session := httptest.NewRecorder()
	h.ServeHTTP(session, sessionRequest)
	if session.Code != http.StatusOK {
		t.Fatalf("session after failed logout=%d", session.Code)
	}
}

type fakeGoogle struct {
	identity      externalIdentity
	authorization string
}

func (g *fakeGoogle) AuthorizationURL(state, nonce, challenge string) string {
	g.authorization = "https://accounts.example/auth?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce) + "&challenge=" + url.QueryEscape(challenge)
	return g.authorization
}
func (g *fakeGoogle) Exchange(context.Context, string, string) (externalIdentity, error) {
	return g.identity, nil
}

func TestGoogleStateNonceAndSignupBoundaries(t *testing.T) {
	repo := newMemoryRepository()
	google := &fakeGoogle{identity: externalIdentity{Subject: "google-1", Email: "g@example.com", EmailVerified: true, FirstName: "Grace", LastName: "Hopper"}}
	_, h := testAuth(t, repo, nil, google)
	start := jsonRequest(t, h, http.MethodGet, "/auth/google/start?flow=signup&organizationName=Navy", nil)
	if start.Code != http.StatusFound {
		t.Fatalf("start=%d %s", start.Code, start.Body.String())
	}
	location, _ := url.Parse(start.Header().Get("Location"))
	stateToken := location.Query().Get("state")
	nonce := location.Query().Get("nonce")
	startCookies := start.Result().Cookies()
	if len(startCookies) != 1 || !startCookies[0].HttpOnly || startCookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("OAuth binding cookie=%+v", startCookies)
	}
	google.identity.Nonce = "wrong"
	bad := jsonRequest(t, h, http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(stateToken)+"&code=x", nil, startCookies[0])
	if bad.Code != http.StatusUnauthorized || len(repo.accounts) != 0 {
		t.Fatalf("nonce mismatch status=%d accounts=%d", bad.Code, len(repo.accounts))
	}
	// The state was single use even though provider identity validation failed.
	google.identity.Nonce = nonce
	replay := jsonRequest(t, h, http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(stateToken)+"&code=x", nil, startCookies[0])
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("state replay=%d", replay.Code)
	}
}

func TestGoogleSignupIssuesCookieWithoutExposingCredential(t *testing.T) {
	repo := newMemoryRepository()
	google := &fakeGoogle{identity: externalIdentity{
		Subject: "google-2", Email: "google@example.com", EmailVerified: true,
		FirstName: "Grace", LastName: "Hopper",
	}}
	_, h := testAuth(t, repo, nil, google)
	start := jsonRequest(t, h, http.MethodGet,
		"/auth/google/start?flow=signup&organizationName=Navy&jobRole=CTO&salesTeamSize=10-20", nil)
	if start.Code != http.StatusFound {
		t.Fatalf("start=%d %s", start.Code, start.Body.String())
	}
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	stateToken := location.Query().Get("state")
	google.identity.Nonce = location.Query().Get("nonce")
	if stateToken == "" || google.identity.Nonce == "" || location.Query().Get("challenge") == "" {
		t.Fatalf("authorization URL lacks state/nonce/PKCE: %s", location)
	}
	if _, rawStateStored := repo.states[stateToken]; rawStateStored {
		t.Fatal("raw OAuth state token was stored instead of its digest")
	}
	startCookies := start.Result().Cookies()
	if len(startCookies) != 1 || startCookies[0].Value == "" || !startCookies[0].HttpOnly {
		t.Fatalf("OAuth binding cookie=%+v", startCookies)
	}
	stateKey := digestKey(tokenDigest(stateToken))
	if hmac.Equal(repo.states[stateKey].BindingDigest, []byte(startCookies[0].Value)) {
		t.Fatal("raw OAuth transaction binding was stored instead of its digest")
	}

	// A copied callback URL is insufficient: it lacks the HttpOnly transaction
	// cookie set in the browser that initiated the flow. Rejection does not
	// consume the state, so the initiating browser can still complete it.
	stolen := jsonRequest(t, h, http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(stateToken)+"&code=authorization-code", nil)
	if stolen.Code != http.StatusUnauthorized {
		t.Fatalf("callback without binding cookie=%d", stolen.Code)
	}
	stolenCookies := stolen.Result().Cookies()
	if len(stolenCookies) != 1 || stolenCookies[0].Name != startCookies[0].Name || stolenCookies[0].MaxAge != -1 {
		t.Fatalf("invalid callback did not clear OAuth binding cookie: %+v", stolenCookies)
	}
	if _, remains := repo.states[stateKey]; !remains {
		t.Fatal("callback without browser binding consumed OAuth state")
	}

	callback := jsonRequest(t, h, http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(stateToken)+"&code=authorization-code", nil,
		startCookies[0])
	if callback.Code != http.StatusOK {
		t.Fatalf("callback=%d %s", callback.Code, callback.Body.String())
	}
	if strings.Contains(callback.Body.String(), "sessionToken") {
		t.Fatal("Google callback exposed the HttpOnly session credential")
	}
	var session Session
	if err := json.Unmarshal(callback.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Organization.Name != "Navy" || session.Membership.JobRole != "CTO" ||
		session.Membership.SalesTeamSize != "10-20" {
		t.Fatalf("Google onboarding was not preserved: %+v", session)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == "savvy_session" && cookie.MaxAge != -1 {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.Value == "" {
		t.Fatalf("Google callback cookies=%+v", callback.Result().Cookies())
	}
	replay := jsonRequest(t, h, http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(stateToken)+"&code=authorization-code", nil,
		startCookies[0])
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("OAuth state replay status=%d", replay.Code)
	}
}

func TestGoogleJoinCreatesPendingRequestWithoutSession(t *testing.T) {
	repo := newMemoryRepository()
	repo.joinResult = joinResult{Request: JoinRequest{
		ID: "join_1", OrganizationID: "org_target", OrganizationName: "Target Org",
		Status: "pending",
	}}
	google := &fakeGoogle{identity: externalIdentity{
		Subject: "google-join-1", Email: "applicant@example.com", EmailVerified: true,
		FirstName: "Grace", LastName: "Hopper",
	}}
	authenticator, h := testAuth(t, repo, nil, google)
	authenticator.service.joinResultURL = "https://crm.savvyadvi.com/?auth=signup#top"

	const organizationCode = "SAVVY-ABCD-EFGH-JKLM"
	start := jsonRequest(t, h, http.MethodGet,
		"/auth/google/start?flow=join&organizationCode="+url.QueryEscape(organizationCode), nil)
	if start.Code != http.StatusFound {
		t.Fatalf("start=%d %s", start.Code, start.Body.String())
	}
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	stateToken := location.Query().Get("state")
	google.identity.Nonce = location.Query().Get("nonce")
	state, ok := repo.states[digestKey(tokenDigest(stateToken))]
	if !ok {
		t.Fatal("OAuth state was not stored")
	}
	expectedDigest := joinCodeDigest(organizationCode)
	if len(state.JoinCodeDigest) != sha256DigestBytes || !hmac.Equal(state.JoinCodeDigest, expectedDigest) {
		t.Fatalf("stored organization-code digest=%x", state.JoinCodeDigest)
	}
	if strings.Contains(fmt.Sprintf("%+v", state), organizationCode) {
		t.Fatal("OAuth state exposed the organization code")
	}

	startCookies := start.Result().Cookies()
	if len(startCookies) != 1 {
		t.Fatalf("OAuth binding cookies=%+v", startCookies)
	}
	callback := jsonRequest(t, h, http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(stateToken)+"&code=authorization-code", nil,
		startCookies[0])
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("callback=%d body=%s", callback.Code, callback.Body.String())
	}
	resultURL, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if resultURL.Query().Get("auth") != "signup" || resultURL.Query().Get("joinStatus") != "pending" || resultURL.Fragment != "top" {
		t.Fatalf("join result redirect=%s", resultURL)
	}
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == "savvy_session" && cookie.MaxAge != -1 {
			t.Fatalf("pending applicant received a CRM session cookie: %+v", cookie)
		}
	}
	if repo.joinEmail != "applicant@example.com" || repo.joinProfile.FirstName != "Grace" ||
		repo.joinProfile.LastName != "Hopper" || !hmac.Equal(repo.joinDigest, expectedDigest) {
		t.Fatalf("join request was not preserved: email=%q profile=%+v digest=%x",
			repo.joinEmail, repo.joinProfile, repo.joinDigest)
	}
}

func TestGoogleUnavailableIsClean503(t *testing.T) {
	_, h := testAuth(t, newMemoryRepository(), nil, nil)
	w := jsonRequest(t, h, http.MethodGet, "/auth/google/start?flow=login", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "service_unavailable") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGoogleStartRejectsUnboundedOnboardingMetadata(t *testing.T) {
	for name, query := range map[string]string{
		"missing organization": "flow=signup",
		"long organization":    "flow=signup&organizationName=" + url.QueryEscape(strings.Repeat("x", 161)),
		"job role line break":  "flow=signup&organizationName=Org&jobRole=" + url.QueryEscape("CEO\nInjected"),
		"team size line break": "flow=signup&organizationName=Org&salesTeamSize=" + url.QueryEscape("10-20\rInjected"),
	} {
		t.Run(name, func(t *testing.T) {
			repo := newMemoryRepository()
			_, h := testAuth(t, repo, nil, &fakeGoogle{})
			w := jsonRequest(t, h, http.MethodGet, "/auth/google/start?"+query, nil)
			if w.Code != http.StatusBadRequest || len(repo.states) != 0 || len(w.Result().Cookies()) != 0 {
				t.Fatalf("status=%d states=%d cookies=%v body=%s", w.Code, len(repo.states), w.Result().Cookies(), w.Body.String())
			}
		})
	}
}

func TestGlobalAuthIssuanceLimitCoversEmailAndGoogle(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	newFullRepo := func() *memoryRepository {
		repo := newMemoryRepository()
		for i := range maxGlobalRequests {
			id := fmt.Sprintf("existing-%d", i)
			repo.challenges[id] = challenge{
				ID: id, EmailNormalized: fmt.Sprintf("person-%d@example.com", i),
				Flow: "login", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			}
		}
		return repo
	}
	for name, request := range map[string]func(http.Handler) *httptest.ResponseRecorder{
		"email": func(h http.Handler) *httptest.ResponseRecorder {
			return jsonRequest(t, h, http.MethodPost, "/auth/email/request",
				map[string]string{"email": "new@example.com", "flow": "login"})
		},
		"Google": func(h http.Handler) *httptest.ResponseRecorder {
			return jsonRequest(t, h, http.MethodGet, "/auth/google/start?flow=login", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := newFullRepo()
			_, h := testAuth(t, repo, &captureMailer{}, &fakeGoogle{})
			w := request(h)
			if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "900" {
				t.Fatalf("status=%d retry=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
			}
		})
	}
}

func TestCapabilitiesReportsGoogleWithoutLeakingConfiguration(t *testing.T) {
	for name, google := range map[string]googleProvider{
		"disabled": nil,
		"enabled":  &fakeGoogle{},
	} {
		t.Run(name, func(t *testing.T) {
			_, h := testAuth(t, newMemoryRepository(), nil, google)
			w := jsonRequest(t, h, http.MethodGet, "/auth/capabilities", nil)
			if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
			}
			var got map[string]bool
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["google"] != (google != nil) || len(got) != 1 {
				t.Fatalf("capabilities=%v", got)
			}
		})
	}
}

func TestAuthenticatorCloseClosesSessionStoreAndReadinessFails(t *testing.T) {
	repo := newMemoryRepository()
	a, _ := testAuth(t, repo, nil, nil)
	if err := a.Ready(context.Background()); err != nil {
		t.Fatalf("ready before close: %v", err)
	}
	a.Close()
	a.Close()
	if !repo.closed {
		t.Fatal("Authenticator.Close did not close the session repository")
	}
	if err := a.Ready(context.Background()); err == nil {
		t.Fatal("closed authentication repository still reported ready")
	}
	if repo.closeCalls != 1 {
		t.Fatalf("authentication repository closed %d times, want exactly once", repo.closeCalls)
	}
}

func TestNativeRoleScopesMatchSharedContract(t *testing.T) {
	var contractPath string
	for _, candidate := range []string{
		"../../../../packages/contracts/rbac/crm-roles.json",
		"../../../packages/contracts/rbac/crm-roles.json",
		"packages/contracts/rbac/crm-roles.json",
	} {
		if _, err := os.Stat(candidate); err == nil {
			contractPath = filepath.Clean(candidate)
			break
		}
	}
	if contractPath == "" {
		t.Fatal("cannot find packages/contracts/rbac/crm-roles.json")
	}
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Roles []struct {
			Key    string   `json:"key"`
			Scopes []string `json:"scopes"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, role := range contract.Roles {
		got, ok := roleScopes(role.Key)
		if !ok {
			t.Errorf("shared role %q is unknown to native authentication", role.Key)
			continue
		}
		seen[role.Key] = true
		want := append([]string(nil), role.Scopes...)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("native scopes for %q differ from shared contract\n got: %v\nwant: %v", role.Key, got, want)
		}
	}
	for _, role := range []string{"crm_admin", "crm_manager", "crm_member", "crm_viewer"} {
		if !seen[role] {
			t.Errorf("native role %q is absent from shared contract", role)
		}
	}
}

func TestNativeAuthenticationConfigBoundsTTLs(t *testing.T) {
	base := Config{DatabaseURL: "postgres://unused", OTPSecret: strings.Repeat("p", 32)}
	for name, mutate := range map[string]func(*Config){
		"short OTP":     func(c *Config) { c.OTPTTL = 30 * time.Second },
		"long OTP":      func(c *Config) { c.OTPTTL = 31 * time.Minute },
		"short session": func(c *Config) { c.SessionTTL = 4 * time.Minute },
		"long session":  func(c *Config) { c.SessionTTL = 91 * 24 * time.Hour },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := normalizeConfig(cfg); err == nil {
				t.Fatal("out-of-bounds TTL was accepted by native auth constructor")
			}
		})
	}
}

func TestNativeAuthenticationConfigRequiresAbsoluteResultURLs(t *testing.T) {
	base := Config{DatabaseURL: "postgres://unused", OTPSecret: strings.Repeat("p", 32)}
	for name, mutate := range map[string]func(*Config){
		"relative login destination": func(c *Config) { c.LoginSuccessURL = "/dashboard" },
		"relative join destination":  func(c *Config) { c.JoinResultURL = "/?auth=signup" },
		"non HTTP join destination":  func(c *Config) { c.JoinResultURL = "javascript:alert(1)" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := normalizeConfig(cfg); err == nil {
				t.Fatal("unsafe authentication destination was accepted")
			}
		})
	}
}
