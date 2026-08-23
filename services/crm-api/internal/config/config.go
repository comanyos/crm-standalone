// Package config holds CRM runtime configuration, switched via environment
// variables (config, not code) per the Module Contract.
package config

import (
	"fmt"
	"os"
	"strings"
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
	Addr         string      // listen address, e.g. ":9431"
	Mode         Mode        // plug | standalone
	Edition      string      // OSS | PRO | ENTERPRISE
	Environment  Environment // development | test | staging | production
	AuthIssuer   string      // AuthinCore issuer URL
	AuthAudience string      // required JWT audience
	AuthJWKSURL  string      // AuthinCore JWKS endpoint (empty = auth disabled in dev)
	NATSURL      string      // event bus (empty = events logged locally)
	DatabaseURL  string      // Postgres metadata store (empty = in-memory)
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

	// GoogleOAuth is optional until the Google communication integration is
	// enabled. When any setting is supplied, Load/Validate require the complete
	// set so a partially configured client can never start an OAuth flow.
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	GoogleOAuthRedirectURL  string
	GoogleOAuthTokenKey     string
}

// Load reads configuration from environment with sensible defaults.
func Load() Config {
	return Config{
		Addr:                    env("CRM_ADDR", ":9431"),
		Mode:                    Mode(env("MODE", string(ModePlug))),
		Edition:                 env("CRM_EDITION", "OSS"),
		Environment:             Environment(env("APP_ENV", string(EnvDevelopment))),
		AuthIssuer:              env("AUTH_ISSUER", ""),
		AuthAudience:            env("AUTH_AUDIENCE", ""),
		AuthJWKSURL:             env("AUTH_JWKS_URL", ""),
		NATSURL:                 env("NATS_URL", ""),
		DatabaseURL:             env("DATABASE_URL", ""),
		MetricsToken:            env("CRM_METRICS_TOKEN", ""),
		OutboxDrainURL:          env("OUTBOX_DRAIN_DATABASE_URL", ""),
		CORSOrigins:             splitCSV(env("CORS_ALLOWED_ORIGINS", "")),
		GoogleOAuthClientID:     env("GOOGLE_OAUTH_CLIENT_ID", ""),
		GoogleOAuthClientSecret: env("GOOGLE_OAUTH_CLIENT_SECRET", ""),
		GoogleOAuthRedirectURL:  env("GOOGLE_OAUTH_REDIRECT_URL", ""),
		GoogleOAuthTokenKey:     env("GOOGLE_OAUTH_TOKEN_KEY", ""),
	}
}

// Validate checks the config and returns a clear error for bad values. In
// staging/production it additionally rejects the unsafe defaults that are
// fine for local development: a non-TLS auth issuer/JWKS URL, no database (the
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
	if !c.Environment.IsProdLike() {
		return nil
	}

	var problems []string
	if c.AuthIssuer == "" {
		problems = append(problems, "AUTH_ISSUER is required")
	} else if !strings.HasPrefix(c.AuthIssuer, "https://") {
		problems = append(problems, "AUTH_ISSUER must be an https:// URL")
	}
	if c.AuthJWKSURL == "" {
		problems = append(problems, "AUTH_JWKS_URL is required")
	} else if !strings.HasPrefix(c.AuthJWKSURL, "https://") {
		problems = append(problems, "AUTH_JWKS_URL must be an https:// URL")
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

	if len(problems) > 0 {
		return fmt.Errorf("config: unsafe for %s environment:\n  - %s", c.Environment, strings.Join(problems, "\n  - "))
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
