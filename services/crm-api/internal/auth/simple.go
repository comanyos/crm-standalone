package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultOTPTTL      = 10 * time.Minute
	defaultSessionTTL  = 30 * 24 * time.Hour
	defaultOAuthTTL    = 10 * time.Minute
	minOTPTTL          = time.Minute
	maxOTPTTL          = 30 * time.Minute
	minSessionTTL      = 5 * time.Minute
	maxSessionTTL      = 90 * 24 * time.Hour
	defaultCookieName  = "savvy_session"
	maxOTPAttempts     = 5
	maxEmailRequests   = 5
	maxGlobalRequests  = 300
	emailRequestWindow = 15 * time.Minute
	cleanupInterval    = time.Hour
)

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

// Config is the first-party authentication configuration. Google login uses
// its own credentials and redirect URL; it never reuses the Gmail/Calendar
// integration grant.
type Config struct {
	DatabaseURL  string
	OTPSecret    string
	OTPTTL       time.Duration
	SessionTTL   time.Duration
	CookieName   string
	CookieSecure bool
	DevOTP       string

	SMTP SMTPConfig

	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	LoginSuccessURL    string
	JoinResultURL      string

	BootstrapEmail   string
	BootstrapOrgName string
	BootstrapOrgID   string
}

// SMTPConfig deliberately mirrors the established SMTP_* environment
// contract so the existing Savvy mailbox can be moved without changing
// provider configuration.
type SMTPConfig struct {
	Host            string
	Port            int
	Username        string
	Password        string
	From            string
	UseTLS          bool
	RequireSTARTTLS bool
	Timeout         time.Duration
}

type User struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Plan string `json:"plan"`
}

type Membership struct {
	Role          string `json:"role"`
	JobRole       string `json:"jobRole,omitempty"`
	SalesTeamSize string `json:"salesTeamSize,omitempty"`
}

type Session struct {
	User         User         `json:"user"`
	Organization Organization `json:"organization"`
	Membership   Membership   `json:"membership"`
	Roles        []string     `json:"roles"`
	Scopes       []string     `json:"scopes"`
	ExpiresAt    time.Time    `json:"expiresAt"`
}

type sessionEnvelope struct {
	Session
	token string
}

type Profile struct {
	FirstName        string `json:"firstName"`
	LastName         string `json:"lastName"`
	OrganizationName string `json:"organizationName"`
	JobRole          string `json:"jobRole"`
	SalesTeamSize    string `json:"salesTeamSize"`
}

type challenge struct {
	ID              string
	EmailNormalized string
	Flow            string
	CodeDigest      []byte
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

type oauthState struct {
	Digest           []byte
	BindingDigest    []byte
	Flow             string
	Nonce            string
	PKCEVerifier     string
	OrganizationName string
	JobRole          string
	SalesTeamSize    string
	JoinCodeDigest   []byte
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

type externalIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	FirstName     string
	LastName      string
	Nonce         string
}

var (
	ErrInvalidChallenge = errors.New("invalid or expired verification code")
	ErrRateLimited      = errors.New("too many verification requests")
	ErrLoginUnavailable = errors.New("email delivery unavailable")
	ErrNoAccount        = errors.New("no active account for this email")
	ErrInvalidSession   = errors.New("invalid or expired session")
	ErrOAuthUnavailable = errors.New("google login unavailable")
	ErrOAuthState       = errors.New("invalid or expired OAuth state")
	ErrApprovalPending  = errors.New("organization access is awaiting administrator approval")
	ErrInvalidJoinCode  = errors.New("organization code is invalid or disabled")
	ErrJoinConflict     = errors.New("this account cannot request access to that organization")
)

type repository interface {
	Ping(context.Context) error
	Close()
	CreateChallenge(context.Context, challenge, int, int, time.Duration) error
	RetireChallenge(context.Context, string, time.Time) error
	ChallengeFlow(context.Context, string, time.Time) (string, error)
	VerifyChallenge(context.Context, string, []byte, time.Time) (email, flow string, err error)
	Login(context.Context, string) (User, Organization, Membership, error)
	Signup(context.Context, string, Profile, time.Time) (User, Organization, Membership, error)
	CreateSession(context.Context, string, string, []byte, time.Time, time.Time) error
	Session(context.Context, []byte, time.Time) (User, Organization, Membership, time.Time, error)
	RevokeSession(context.Context, []byte, time.Time) error
	Bootstrap(context.Context, string, string, string, time.Time) (User, Organization, error)
	CreateOAuthState(context.Context, oauthState, int, time.Duration) error
	ConsumeOAuthState(context.Context, []byte, []byte, time.Time) (oauthState, error)
	LoginExternal(context.Context, string, externalIdentity) (User, Organization, Membership, error)
	SignupExternal(context.Context, string, externalIdentity, Profile, time.Time) (User, Organization, Membership, error)
	RequestJoin(context.Context, string, JoinProfile, []byte, time.Time) (joinResult, error)
	RequestJoinExternal(context.Context, string, externalIdentity, JoinProfile, []byte, time.Time) (joinResult, error)
	Cleanup(context.Context, time.Time, time.Duration) error
}

type mailer interface {
	Send(context.Context, string, string, string) error
}

type googleProvider interface {
	AuthorizationURL(state, nonce, codeChallenge string) string
	Exchange(context.Context, string, string) (externalIdentity, error)
}

// service owns the public login flows; the Authenticator remains the small
// middleware surface consumed by the rest of the CRM.
type service struct {
	repo   repository
	mailer mailer
	google googleProvider
	// The bootstrap address is the operator-owned break-glass identity. It is
	// promoted at session materialization time rather than stored as a tenant
	// membership role, keeping platform access separate from workspace RBAC.
	platformAdminEmail string
	otpSecret          []byte
	otpTTL             time.Duration
	sessionTTL         time.Duration
	oauthTTL           time.Duration
	cookieName         string
	cookieSecure       bool
	devOTP             string
	successURL         string
	joinResultURL      string
	now                func() time.Time
	cleanupMu          sync.Mutex
	nextCleanup        time.Time
	closeOnce          sync.Once
}

func normalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return cfg, errors.New("auth: DATABASE_URL is required")
	}
	if len(cfg.OTPSecret) < 32 {
		return cfg, errors.New("auth: OTP_PEPPER must be at least 32 characters")
	}
	if cfg.OTPTTL <= 0 {
		cfg.OTPTTL = defaultOTPTTL
	}
	if cfg.OTPTTL < minOTPTTL || cfg.OTPTTL > maxOTPTTL {
		return cfg, errors.New("auth: AUTH_OTP_TTL must be between 1m and 30m")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	if cfg.SessionTTL < minSessionTTL || cfg.SessionTTL > maxSessionTTL {
		return cfg, errors.New("auth: AUTH_SESSION_TTL must be between 5m and 2160h")
	}
	if cfg.CookieName == "" {
		cfg.CookieName = defaultCookieName
	}
	if strings.ContainsAny(cfg.CookieName, " \t\r\n;=,") {
		return cfg, errors.New("auth: invalid cookie name")
	}
	if cfg.DevOTP != "" && !sixDigits.MatchString(cfg.DevOTP) {
		return cfg, errors.New("auth: AUTH_DEV_OTP must contain exactly six digits")
	}
	if (cfg.GoogleClientID == "") != (cfg.GoogleClientSecret == "") ||
		(cfg.GoogleClientID == "") != (cfg.GoogleRedirectURL == "") {
		return cfg, errors.New("auth: Google login is partially configured")
	}
	for name, rawURL := range map[string]string{
		"AUTH_LOGIN_SUCCESS_URL": cfg.LoginSuccessURL,
		"AUTH_JOIN_RESULT_URL":   cfg.JoinResultURL,
	} {
		if rawURL == "" {
			continue
		}
		destination, err := url.Parse(rawURL)
		if err != nil || (destination.Scheme != "http" && destination.Scheme != "https") || destination.Host == "" {
			return cfg, fmt.Errorf("auth: %s must be an absolute http(s) URL", name)
		}
	}
	if strings.TrimSpace(cfg.BootstrapEmail) != "" {
		email, err := normalizeEmail(cfg.BootstrapEmail)
		if err != nil {
			return cfg, errors.New("auth: AUTH_BOOTSTRAP_EMAIL is invalid")
		}
		cfg.BootstrapEmail = email
	}
	return cfg, nil
}

func normalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid email")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.Count(parsed.Address, "@") != 1 {
		return "", errors.New("invalid email")
	}
	return strings.ToLower(parsed.Address), nil
}

func validFlow(flow string) bool { return flow == "login" || flow == "signup" || flow == "join" }

func validateProfile(p Profile) error {
	p.FirstName = strings.TrimSpace(p.FirstName)
	p.LastName = strings.TrimSpace(p.LastName)
	p.OrganizationName = strings.TrimSpace(p.OrganizationName)
	if p.FirstName == "" || p.LastName == "" || p.OrganizationName == "" {
		return errors.New("firstName, lastName and organizationName are required for signup")
	}
	for name, value := range map[string]string{
		"firstName": p.FirstName, "lastName": p.LastName,
		"organizationName": p.OrganizationName, "jobRole": p.JobRole,
		"salesTeamSize": p.SalesTeamSize,
	} {
		if len(value) > 160 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func validateOAuthSignupMetadata(organizationName, jobRole, salesTeamSize string) error {
	if strings.TrimSpace(organizationName) == "" {
		return errors.New("organizationName is required for Google signup")
	}
	for name, value := range map[string]string{
		"organizationName": organizationName,
		"jobRole":          jobRole,
		"salesTeamSize":    salesTeamSize,
	} {
		if len(value) > 160 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func randomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newRecordID(prefix string) (string, error) {
	raw, err := randomToken(18)
	if err != nil {
		return "", err
	}
	return prefix + "_" + raw, nil
}

func generateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func otpDigest(secret []byte, challengeID, code string) []byte {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(challengeID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(code))
	return h.Sum(nil)
}

func tokenDigest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func roleScopes(role string) ([]string, bool) {
	// This is server-owned policy. Signup always creates crm_admin; clients
	// cannot submit a role or a scope. Membership administration may choose
	// one of these fixed roles later.
	scopes := map[string][]string{
		"crm_admin": {"crm:*"},
		"crm_manager": {
			"crm:account:read", "crm:account:write", "crm:contact:read", "crm:contact:write",
			"crm:lead:read", "crm:lead:write", "crm:opportunity:read", "crm:opportunity:write",
			"crm:pipeline:read", "crm:pipeline:write", "crm:activity:read", "crm:activity:write",
			"crm:task:read", "crm:task:write", "crm:note:read", "crm:note:write",
			"crm:summary:read", "crm:search:read", "crm:integration:read", "crm:integration:write",
			"crm:workflow:read", "crm:workflow:write", "crm:notification:read", "crm:notification:write",
			"crm:tag:read", "crm:tag:write", "crm:saved_view:read", "crm:saved_view:write",
			"crm:consent:read", "crm:consent:write", "crm:template:read", "crm:template:write",
			"crm:capture:read", "crm:capture:write", "crm:message:read", "crm:message:write",
		},
		"crm_member": {
			"crm:account:read", "crm:contact:read", "crm:contact:write", "crm:lead:read", "crm:lead:write",
			"crm:opportunity:read", "crm:opportunity:write", "crm:pipeline:read", "crm:activity:read",
			"crm:activity:write", "crm:task:read", "crm:task:write", "crm:note:read", "crm:note:write",
			"crm:summary:read", "crm:search:read", "crm:integration:read", "crm:integration:write",
			"crm:workflow:read", "crm:notification:read", "crm:notification:write", "crm:tag:read",
			"crm:tag:write", "crm:saved_view:read", "crm:saved_view:write", "crm:consent:read",
			"crm:consent:write", "crm:template:read", "crm:template:write", "crm:message:read", "crm:message:write",
		},
		"crm_viewer": {
			"crm:account:read", "crm:contact:read", "crm:lead:read", "crm:opportunity:read",
			"crm:pipeline:read", "crm:activity:read", "crm:task:read", "crm:note:read",
			"crm:summary:read", "crm:search:read", "crm:notification:read", "crm:tag:read",
			"crm:consent:read", "crm:template:read", "crm:message:read",
		},
	}
	value, ok := scopes[role]
	return append([]string(nil), value...), ok
}

func principalForSession(u User, org Organization, membership Membership) (*Principal, error) {
	scopes, ok := roleScopes(membership.Role)
	if !ok {
		return nil, ErrInvalidSession
	}
	return &Principal{
		Subject: u.ID, TenantID: org.ID, OrgID: org.ID,
		Roles: []string{membership.Role}, Scopes: scopes,
	}, nil
}

func envelope(u User, org Organization, m Membership, expires time.Time, token string) (sessionEnvelope, error) {
	p, err := principalForSession(u, org, m)
	if err != nil {
		return sessionEnvelope{}, err
	}
	return sessionEnvelope{Session: Session{
		User: u, Organization: org, Membership: m,
		Roles: p.Roles, Scopes: p.Scopes, ExpiresAt: expires,
	}, token: token}, nil
}
