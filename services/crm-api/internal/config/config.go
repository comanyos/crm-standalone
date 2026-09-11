// Package config holds CRM runtime configuration, switched via environment
// variables (config, not code) per the Module Contract.
package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mode is the deployment mode.
type Mode string

// The deployment modes a module can run in.
const (
	ModePlug       Mode = "plug"
	ModeStandalone Mode = "standalone"
)

// Environment is the deployment environment. It gates production-safety
// checks in Validate — it is not a feature flag and must not be used to
// branch application behavior beyond that.
type Environment string

// The deployment environments this service recognizes.
const (
	EnvDevelopment Environment = "development"
	EnvTest        Environment = "test"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

func (e Environment) valid() bool {
	switch e {
	case EnvDevelopment, EnvTest, EnvStaging, EnvProduction:
		return true
	}
	return false
}

// IsProdLike reports whether e requires production-grade security posture.
// Staging is treated the same as production so it exercises the same gate.
func (e Environment) IsProdLike() bool {
	return e == EnvStaging || e == EnvProduction
}

// Config holds runtime configuration.
type Config struct {
	Addr        string      // listen address, e.g. ":9431"
	Mode        Mode        // plug | standalone
	Edition     string      // OSS | PRO | ENTERPRISE
	Environment Environment // development | test | staging | production
	NATSURL     string      // event bus (empty = events logged locally)
	DatabaseURL string      // Postgres metadata store (empty = in-memory)
	// MetricsToken protects /metrics when set. It is mandatory in staging and
	// production; an empty value keeps the explicitly documented local/internal
	// development scrape endpoint unauthenticated.
	MetricsToken string

	// OutboxDrainURL is the DSN the background event-outbox dispatcher
	// connects with (Track A3). It must bypass row-level security —
	// DueOutboxTenants has no tenant to bind, only "which tenants have work
	// waiting". Empty means the dispatcher shares the application pool, which
	// is correct only while that pool's role also bypasses RLS.
	OutboxDrainURL string

	// CORSOrigins is the browser-origin allowlist for cross-origin Connect-RPC
	// calls (apps/web). Empty means no CORS headers are sent at all — safe by
	// default; same-origin/non-browser clients are unaffected either way.
	CORSOrigins []string
	// AuthCORSOrigins is a narrower allowlist for the static marketing site's
	// login/signup UI. It may call only the public authentication initiation
	// routes; it never grants access to CRM RPCs, session inspection, or logout.
	AuthCORSOrigins []string

	// First-party passwordless authentication. OTP challenges and opaque
	// sessions live in the same Postgres database as the CRM, in tables added
	// by migration 0033. The random session token is stored only as SHA-256;
	// OTPPepper keys the digest of the low-entropy six-digit code.
	OTPPepper              string
	AuthOTPTTL             time.Duration
	AuthSessionTTL         time.Duration
	AuthCookieName         string
	AuthCookieSecure       bool
	AuthDevOTP             string
	SMTPHost               string
	SMTPPort               int
	SMTPUsername           string
	SMTPPassword           string
	SMTPFrom               string
	SMTPTLS                bool
	SMTPRequireSTARTTLS    bool
	AuthGoogleClientID     string
	AuthGoogleClientSecret string
	AuthGoogleRedirectURL  string
	AuthLoginSuccessURL    string
	AuthJoinResultURL      string
	AuthBootstrapEmail     string
	AuthBootstrapOrgName   string
	AuthBootstrapOrgID     string

	// GoogleOAuth is optional until the Google communication integration is
	// enabled. When any setting is supplied, Load/Validate require the complete
	// set so a partially configured client can never start an OAuth flow.
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	GoogleOAuthRedirectURL  string
	GoogleOAuthTokenKey     string
	// LeadCaptureSecretKey seals inbound lead-capture signing secrets
	// (docs/LEAD_CAPTURE.md). Deliberately its own key rather than reusing
	// GOOGLE_OAUTH_TOKEN_KEY: lead capture must not require a configured
	// Google OAuth client, and two purposes sharing one key means rotating
	// either forces re-encrypting both.
	//
	// Absent means the feature is off — the route is not registered and the
	// administration RPCs refuse — rather than degraded. An endpoint that
	// cannot check a signature is an open lead-injection hole, so there is no
	// unsealed mode to fall back to.
	LeadCaptureSecretKey string

	// Native Facebook and Instagram Lead Ads. These are one configuration
	// group: OAuth, webhook verification and encrypted token storage must all
	// be available together or the connector is disabled.
	MetaAppID              string
	MetaAppSecret          string
	MetaOAuthRedirectURL   string
	MetaWebhookVerifyToken string
	MetaTokenKey           string
	MetaGraphVersion       string

	// WebAppBaseURL is where the OAuth callback sends the browser once it has
	// finished. It is configuration rather than something read from the
	// request because the callback's final redirect is the one part of the
	// flow an attacker can influence; guessing an origin from a Referer or a
	// Host header is how an open redirect gets shipped. Empty means the
	// callback renders a self-contained result page instead, which is correct
	// for a deployment that has not said where its web application lives.
	WebAppBaseURL string
	// PublicBaseURL is this API's own public origin, used to assemble the
	// lead-capture delivery URLs an administrator hands to a third party
	// (docs/LEAD_CAPTURE.md).
	//
	// Configured rather than derived from a request's Host header: an origin
	// taken from a header is attacker-controlled, and this one ends up pasted
	// into somebody's integration settings, where a poisoned value would
	// silently redirect their leads.
	PublicBaseURL string

	// SyncRetentionDays bounds how long synced communication content is kept.
	// Zero — the default — means keep it until somebody asks for it to go,
	// which is the only honest default: a CRM that silently deleted a
	// customer's correspondence history after ninety days because nobody set
	// this would be losing the organization's records without being told to.
	//
	// When set, each sync pass erases its own mailbox's synced content older
	// than the window, using the same redacting erasure a request does.
	SyncRetentionDays int
}

// Load reads configuration from environment with sensible defaults.
func Load() Config {
	environment := Environment(env("APP_ENV", string(EnvDevelopment)))
	return Config{
		Addr:                    env("CRM_ADDR", ":9431"),
		Mode:                    Mode(env("MODE", string(ModePlug))),
		Edition:                 env("CRM_EDITION", "OSS"),
		Environment:             environment,
		NATSURL:                 env("NATS_URL", ""),
		DatabaseURL:             env("DATABASE_URL", ""),
		MetricsToken:            env("CRM_METRICS_TOKEN", ""),
		OutboxDrainURL:          env("OUTBOX_DRAIN_DATABASE_URL", ""),
		CORSOrigins:             splitCSV(env("CORS_ALLOWED_ORIGINS", "")),
		AuthCORSOrigins:         splitCSV(env("AUTH_CORS_ALLOWED_ORIGINS", "")),
		OTPPepper:               env("OTP_PEPPER", ""),
		AuthOTPTTL:              envDuration("AUTH_OTP_TTL", 10*time.Minute),
		AuthSessionTTL:          envDuration("AUTH_SESSION_TTL", 30*24*time.Hour),
		AuthCookieName:          env("AUTH_COOKIE_NAME", "savvy_session"),
		AuthCookieSecure:        envBool("AUTH_COOKIE_SECURE", environment.IsProdLike()),
		AuthDevOTP:              env("AUTH_DEV_OTP", ""),
		SMTPHost:                env("SMTP_HOST", ""),
		SMTPPort:                envInt("SMTP_PORT", 587),
		SMTPUsername:            env("SMTP_USERNAME", ""),
		SMTPPassword:            env("SMTP_PASSWORD", ""),
		SMTPFrom:                env("SMTP_FROM", "Savvy CRM <support@savvyadvi.com>"),
		SMTPTLS:                 envBool("SMTP_TLS", false),
		SMTPRequireSTARTTLS:     envBool("SMTP_REQUIRE_STARTTLS", true),
		AuthGoogleClientID:      env("AUTH_GOOGLE_CLIENT_ID", ""),
		AuthGoogleClientSecret:  env("AUTH_GOOGLE_CLIENT_SECRET", ""),
		AuthGoogleRedirectURL:   env("AUTH_GOOGLE_REDIRECT_URL", ""),
		AuthLoginSuccessURL:     strings.TrimRight(env("AUTH_LOGIN_SUCCESS_URL", ""), "/"),
		AuthJoinResultURL:       strings.TrimRight(env("AUTH_JOIN_RESULT_URL", ""), "/"),
		AuthBootstrapEmail:      env("AUTH_BOOTSTRAP_EMAIL", ""),
		AuthBootstrapOrgName:    env("AUTH_BOOTSTRAP_ORG_NAME", "Savvy CRM"),
		AuthBootstrapOrgID:      env("AUTH_BOOTSTRAP_ORG_ID", ""),
		GoogleOAuthClientID:     env("GOOGLE_OAUTH_CLIENT_ID", ""),
		GoogleOAuthClientSecret: env("GOOGLE_OAUTH_CLIENT_SECRET", ""),
		GoogleOAuthRedirectURL:  env("GOOGLE_OAUTH_REDIRECT_URL", ""),
		GoogleOAuthTokenKey:     env("GOOGLE_OAUTH_TOKEN_KEY", ""),
		LeadCaptureSecretKey:    env("LEAD_CAPTURE_SECRET_KEY", ""),
		MetaAppID:               env("META_APP_ID", ""),
		MetaAppSecret:           env("META_APP_SECRET", ""),
		MetaOAuthRedirectURL:    env("META_OAUTH_REDIRECT_URL", ""),
		MetaWebhookVerifyToken:  env("META_WEBHOOK_VERIFY_TOKEN", ""),
		MetaTokenKey:            env("META_TOKEN_KEY", ""),
		MetaGraphVersion:        env("META_GRAPH_VERSION", "v25.0"),
		WebAppBaseURL:           strings.TrimRight(env("CRM_WEB_APP_BASE_URL", ""), "/"),
		PublicBaseURL:           strings.TrimRight(env("CRM_PUBLIC_BASE_URL", ""), "/"),
		SyncRetentionDays:       envInt("SYNC_RETENTION_DAYS", 0),
	}
}

// Validate checks the config and returns a clear error for bad values. In
// staging/production it additionally rejects the unsafe defaults that are
// fine for local development: weak auth/SMTP settings, no database (the
// in-memory store), a development-default or weak database password, an
// outbox drain connection that silently falls back to the application pool,
// and sslmode=disable.
func (c Config) Validate() error {
	switch c.Edition {
	case "OSS", "PRO", "ENTERPRISE":
	default:
		return fmt.Errorf("config: CRM_EDITION must be OSS|PRO|ENTERPRISE, got %q", c.Edition)
	}
	if c.Mode != ModePlug && c.Mode != ModeStandalone {
		return fmt.Errorf("config: MODE must be plug|standalone, got %q", c.Mode)
	}
	if !c.Environment.valid() {
		return fmt.Errorf("config: APP_ENV must be development|test|staging|production, got %q", c.Environment)
	}
	if err := c.validateGoogleOAuth(); err != nil {
		return err
	}
	if err := c.validateMeta(); err != nil {
		return err
	}
	if c.SyncRetentionDays < 0 {
		return fmt.Errorf("config: SYNC_RETENTION_DAYS must not be negative, got %d", c.SyncRetentionDays)
	}
	if c.AuthOTPTTL < 0 || c.AuthOTPTTL > 30*time.Minute {
		return fmt.Errorf("config: AUTH_OTP_TTL must be zero/default or between 1m and 30m")
	}
	if c.AuthOTPTTL > 0 && c.AuthOTPTTL < time.Minute {
		return fmt.Errorf("config: AUTH_OTP_TTL must be zero/default or between 1m and 30m")
	}
	if c.AuthSessionTTL < 0 || c.AuthSessionTTL > 90*24*time.Hour {
		return fmt.Errorf("config: AUTH_SESSION_TTL must be zero/default or between 5m and 2160h")
	}
	if c.AuthSessionTTL > 0 && c.AuthSessionTTL < 5*time.Minute {
		return fmt.Errorf("config: AUTH_SESSION_TTL must be zero/default or between 5m and 2160h")
	}
	if c.AuthDevOTP != "" && c.Environment != EnvTest {
		return fmt.Errorf("config: AUTH_DEV_OTP is allowed only when APP_ENV=test")
	}
	if err := c.validateLoginGoogle(); err != nil {
		return err
	}
	if !c.Environment.IsProdLike() {
		return nil
	}

	var problems []string
	if len(c.OTPPepper) < 32 {
		problems = append(problems, "OTP_PEPPER is required and must be at least 32 characters")
	}
	if !c.AuthCookieSecure {
		problems = append(problems, "AUTH_COOKIE_SECURE must be true")
	}
	if c.SMTPHost == "" || c.SMTPFrom == "" {
		problems = append(problems, "SMTP_HOST and SMTP_FROM are required for passwordless login")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		problems = append(problems, "SMTP_PORT must be between 1 and 65535")
	}
	if c.SMTPTLS == c.SMTPRequireSTARTTLS {
		problems = append(problems, "exactly one of SMTP_TLS or SMTP_REQUIRE_STARTTLS must be true")
	}
	if c.SMTPUsername == "" || c.SMTPPassword == "" {
		problems = append(problems, "SMTP_USERNAME and SMTP_PASSWORD are required")
	}
	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required (the in-memory store is a development default)")
	} else {
		if pw := dsnPassword(c.DatabaseURL); weakPassword(pw) {
			problems = append(problems, "DATABASE_URL uses a development-default or weak password")
		}
		if strings.Contains(c.DatabaseURL, "sslmode=disable") {
			problems = append(problems, "DATABASE_URL must not use sslmode=disable")
		}
		if c.OutboxDrainURL == "" {
			problems = append(problems, "OUTBOX_DRAIN_DATABASE_URL is required alongside DATABASE_URL "+
				"(without it the outbox dispatcher shares the RLS-scoped application pool and drains nothing)")
		} else if pw := dsnPassword(c.OutboxDrainURL); weakPassword(pw) {
			problems = append(problems, "OUTBOX_DRAIN_DATABASE_URL uses a development-default or weak password")
		}
	}
	if len(c.MetricsToken) < 16 {
		problems = append(problems, "CRM_METRICS_TOKEN is required and must be at least 16 characters")
	}
	for _, origin := range c.CORSOrigins {
		if !strings.HasPrefix(origin, "https://") {
			problems = append(problems, fmt.Sprintf("CORS_ALLOWED_ORIGINS entry %q must be an https:// origin", origin))
		}
	}
	for _, origin := range c.AuthCORSOrigins {
		if !strings.HasPrefix(origin, "https://") {
			problems = append(problems, fmt.Sprintf("AUTH_CORS_ALLOWED_ORIGINS entry %q must be an https:// origin", origin))
		}
	}
	if c.WebAppBaseURL != "" && !strings.HasPrefix(c.WebAppBaseURL, "https://") {
		problems = append(problems, "CRM_WEB_APP_BASE_URL must be an https:// origin")
	}
	if c.AuthLoginSuccessURL != "" && !strings.HasPrefix(c.AuthLoginSuccessURL, "https://") {
		problems = append(problems, "AUTH_LOGIN_SUCCESS_URL must be an https:// URL")
	}
	if c.AuthJoinResultURL != "" && !strings.HasPrefix(c.AuthJoinResultURL, "https://") {
		problems = append(problems, "AUTH_JOIN_RESULT_URL must be an https:// URL")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config: unsafe for %s environment:\n  - %s", c.Environment, strings.Join(problems, "\n  - "))
	}
	return nil
}

func (c Config) validateLoginGoogle() error {
	values := []string{c.AuthGoogleClientID, c.AuthGoogleClientSecret, c.AuthGoogleRedirectURL}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured != 0 && configured != len(values) {
		return fmt.Errorf("config: Google login is partially configured; AUTH_GOOGLE_CLIENT_ID, AUTH_GOOGLE_CLIENT_SECRET and AUTH_GOOGLE_REDIRECT_URL are one group")
	}
	if configured > 0 && c.Environment.IsProdLike() && !strings.HasPrefix(c.AuthGoogleRedirectURL, "https://") {
		return fmt.Errorf("config: AUTH_GOOGLE_REDIRECT_URL must be an https:// URL in %s", c.Environment)
	}
	return nil
}

func (c Config) validateGoogleOAuth() error {
	values := map[string]string{
		"GOOGLE_OAUTH_CLIENT_ID":     c.GoogleOAuthClientID,
		"GOOGLE_OAUTH_CLIENT_SECRET": c.GoogleOAuthClientSecret,
		"GOOGLE_OAUTH_REDIRECT_URL":  c.GoogleOAuthRedirectURL,
		"GOOGLE_OAUTH_TOKEN_KEY":     c.GoogleOAuthTokenKey,
	}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}

	var missing []string
	for key, value := range values {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: Google OAuth is partially configured; missing %s", strings.Join(missing, ", "))
	}
	if c.Environment.IsProdLike() && !strings.HasPrefix(c.GoogleOAuthRedirectURL, "https://") {
		return fmt.Errorf("config: GOOGLE_OAUTH_REDIRECT_URL must be an https:// URL in %s", c.Environment)
	}
	return nil
}

func (c Config) validateMeta() error {
	values := map[string]string{
		"META_APP_ID":               c.MetaAppID,
		"META_APP_SECRET":           c.MetaAppSecret,
		"META_OAUTH_REDIRECT_URL":   c.MetaOAuthRedirectURL,
		"META_WEBHOOK_VERIFY_TOKEN": c.MetaWebhookVerifyToken,
		"META_TOKEN_KEY":            c.MetaTokenKey,
	}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	var missing []string
	for key, value := range values {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("config: Meta Lead Ads is partially configured; missing %s", strings.Join(missing, ", "))
	}
	if len(c.MetaWebhookVerifyToken) < 16 {
		return fmt.Errorf("config: META_WEBHOOK_VERIFY_TOKEN must be at least 16 characters")
	}
	if c.Environment.IsProdLike() && !strings.HasPrefix(c.MetaOAuthRedirectURL, "https://") {
		return fmt.Errorf("config: META_OAUTH_REDIRECT_URL must be an https:// URL in %s", c.Environment)
	}
	return nil
}

// envInt reads a whole number, treating an unparseable value as the default
// rather than as zero. A typo in SYNC_RETENTION_DAYS silently meaning "keep
// forever" is survivable; silently meaning "delete everything older than
// now" would not be.
func envInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return value
}

func envBool(key string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return value
}

func envDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return value
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
