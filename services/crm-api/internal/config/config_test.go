package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Mode:             ModeStandalone,
		Edition:          "OSS",
		Environment:      EnvDevelopment,
		DatabaseURL:      "postgres://crm_app:a-sufficiently-long-generated-password@db.internal:5432/crm?sslmode=verify-full",
		OutboxDrainURL:   "postgres://crm_drain:another-long-generated-password@db.internal:5432/crm?sslmode=verify-full",
		MetricsToken:     "a-sufficiently-long-metrics-token",
		OTPPepper:        strings.Repeat("p", 32),
		AuthCookieSecure: true,
		SMTPHost:         "smtp.example.com",
		SMTPPort:         465,
		SMTPUsername:     "mailer",
		SMTPPassword:     "provider-secret",
		SMTPFrom:         "Savvy CRM <support@savvyadvi.com>",
		SMTPTLS:          true,
	}
}

func TestValidate_DevelopmentAllowsUnsafeDefaults(t *testing.T) {
	c := Config{Mode: ModeStandalone, Edition: "OSS", Environment: EnvDevelopment}
	if err := c.Validate(); err != nil {
		t.Fatalf("development config should not require prod-safety fields: %v", err)
	}
}

func TestValidate_UnknownEnvironmentRejected(t *testing.T) {
	c := validConfig()
	c.Environment = "prod" // not one of the recognized values
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unrecognized APP_ENV")
	}
}

func TestValidate_ProductionRequiresOTPPepper(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.OTPPepper = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for weak OTP_PEPPER in production")
	}
}

func TestValidate_DevOTPIsTestOnly(t *testing.T) {
	for _, environment := range []Environment{EnvDevelopment, EnvStaging, EnvProduction} {
		c := validConfig()
		c.Environment = environment
		c.AuthDevOTP = "123456"
		if err := c.Validate(); err == nil {
			t.Errorf("expected AUTH_DEV_OTP to be rejected in %s", environment)
		}
	}
	c := validConfig()
	c.Environment = EnvTest
	c.AuthDevOTP = "123456"
	if err := c.Validate(); err != nil {
		t.Fatalf("test environment rejected AUTH_DEV_OTP: %v", err)
	}
}

func TestValidate_BoundsAuthenticationTTLs(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"short OTP":     func(c *Config) { c.AuthOTPTTL = 30 * time.Second },
		"long OTP":      func(c *Config) { c.AuthOTPTTL = 31 * time.Minute },
		"short session": func(c *Config) { c.AuthSessionTTL = 4 * time.Minute },
		"long session":  func(c *Config) { c.AuthSessionTTL = 91 * 24 * time.Hour },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("out-of-bounds authentication TTL was accepted")
			}
		})
	}
}

func TestValidate_BoundsLeadSLA(t *testing.T) {
	for _, value := range []time.Duration{30 * time.Second, 8 * 24 * time.Hour} {
		c := validConfig()
		c.LeadSLA = value
		if err := c.Validate(); err == nil {
			t.Fatalf("out-of-bounds LEAD_SLA %s was accepted", value)
		}
	}
}

func TestValidate_ProdLikeEnvironmentsRequireSecureCookie(t *testing.T) {
	for _, environment := range []Environment{EnvStaging, EnvProduction} {
		c := validConfig()
		c.Environment = environment
		c.AuthCookieSecure = false
		if err := c.Validate(); err == nil {
			t.Errorf("expected error for insecure session cookie in %s", environment)
		}
	}
}

func TestValidate_ProductionRejectsInMemoryStore(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.DatabaseURL = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for empty DATABASE_URL (in-memory store) in production")
	}
}

func TestValidate_ProductionRejectsDevDefaultPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://crm_app:crm_app@db.internal:5432/crm?sslmode=verify-full",
		"postgres://crm_app:password@db.internal:5432/crm?sslmode=verify-full",
		"postgres://crm_app:short1@db.internal:5432/crm?sslmode=verify-full",
	} {
		c := validConfig()
		c.Environment = EnvStaging
		c.DatabaseURL = dsn
		if err := c.Validate(); err == nil {
			t.Fatalf("expected error for weak DATABASE_URL password in %q", dsn)
		}
	}
}

func TestValidate_ProductionRejectsSSLModeDisable(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.DatabaseURL = "postgres://crm_app:a-sufficiently-long-generated-password@db.internal:5432/crm?sslmode=disable"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for sslmode=disable in production")
	}
}

func TestValidate_ProductionRequiresOutboxDrainURL(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.OutboxDrainURL = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing OUTBOX_DRAIN_DATABASE_URL alongside DATABASE_URL in production")
	}
}

func TestValidate_StagingAppliesSameRulesAsProduction(t *testing.T) {
	c := validConfig()
	c.Environment = EnvStaging
	c.SMTPRequireSTARTTLS = true
	if err := c.Validate(); err == nil {
		t.Fatal("expected staging to reject two SMTP TLS modes")
	}
}

func TestValidate_ProductionAcceptsSafeConfig(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	if err := c.Validate(); err != nil {
		t.Fatalf("expected safe production config to validate, got: %v", err)
	}
}

func TestValidate_ProductionRejectsNonHTTPSCORSOrigin(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.CORSOrigins = []string{"http://app.example.com"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for non-https CORS origin in production")
	}
}

func TestValidate_ProductionAcceptsHTTPSCORSOrigin(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.CORSOrigins = []string{"https://app.example.com"}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected https CORS origin to validate, got: %v", err)
	}
}

func TestValidate_ProductionRequiresHTTPSAuthCORSOrigin(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.AuthCORSOrigins = []string{"http://www.example.com"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for non-https auth CORS origin in production")
	}
	c.AuthCORSOrigins = []string{"https://www.example.com"}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected https auth CORS origin to validate, got: %v", err)
	}
}

func TestValidate_ProductionRequiresHTTPSAuthDestinations(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"login success": func(c *Config) { c.AuthLoginSuccessURL = "http://crmapp.example.com" },
		"join result":   func(c *Config) { c.AuthJoinResultURL = "http://crm.example.com/?auth=signup" },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			c.Environment = EnvProduction
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected insecure authentication destination to be rejected")
			}
		})
	}

	c := validConfig()
	c.Environment = EnvProduction
	c.AuthLoginSuccessURL = "https://crmapp.example.com"
	c.AuthJoinResultURL = "https://crm.example.com/?auth=signup#top"
	if err := c.Validate(); err != nil {
		t.Fatalf("expected HTTPS authentication destinations to validate, got: %v", err)
	}
}

func TestValidate_ProductionRequiresMetricsToken(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.MetricsToken = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("expected production to reject a missing or weak CRM_METRICS_TOKEN")
	}
}

func TestValidate_GoogleOAuthRequiresCompleteConfiguration(t *testing.T) {
	c := validConfig()
	c.GoogleOAuthClientID = "client-id.apps.googleusercontent.com"
	if err := c.Validate(); err == nil {
		t.Fatal("expected partial Google OAuth configuration to be rejected")
	}
}

func TestValidate_ProductionRequiresHTTPSGoogleRedirect(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.GoogleOAuthClientID = "client-id.apps.googleusercontent.com"
	c.GoogleOAuthClientSecret = "secret"
	c.GoogleOAuthRedirectURL = "http://localhost:19431/integrations/google/callback"
	c.GoogleOAuthTokenKey = "base64-encoded-32-byte-token-encryption-key"
	if err := c.Validate(); err == nil {
		t.Fatal("expected an insecure Google OAuth redirect URL to be rejected in production")
	}
}

func TestValidate_MetaRequiresOneCompleteConfigurationGroup(t *testing.T) {
	c := validConfig()
	c.MetaAppID = "app-id"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "Meta Lead Ads is partially configured") {
		t.Fatalf("partial Meta configuration error = %v", err)
	}

	c.MetaAppSecret = "app-secret"
	c.MetaOAuthRedirectURL = "http://localhost:19431/integrations/meta/callback"
	c.MetaWebhookVerifyToken = "long-random-verify-token"
	c.MetaTokenKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if err := c.Validate(); err != nil {
		t.Fatalf("complete development Meta configuration: %v", err)
	}
}

func TestValidate_MetaRequiresStrongVerifyTokenAndHTTPSInProduction(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.MetaAppID = "app-id"
	c.MetaAppSecret = "app-secret"
	c.MetaOAuthRedirectURL = "http://crm.example/integrations/meta/callback"
	c.MetaWebhookVerifyToken = "short"
	c.MetaTokenKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "META_WEBHOOK_VERIFY_TOKEN") {
		t.Fatalf("weak webhook token error = %v", err)
	}
	c.MetaWebhookVerifyToken = "long-random-verify-token"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "META_OAUTH_REDIRECT_URL") {
		t.Fatalf("insecure Meta redirect error = %v", err)
	}
	c.MetaOAuthRedirectURL = "https://crm.example/integrations/meta/callback"
	if err := c.Validate(); err != nil {
		t.Fatalf("safe production Meta configuration: %v", err)
	}
}

// TestValidate_ProductionRequiresHTTPSWebAppBaseURL. The OAuth callback sends
// the browser to this origin carrying the outcome of an authorization; over
// plain HTTP that redirect is interceptable on the network.
func TestValidate_ProductionRequiresHTTPSWebAppBaseURL(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.WebAppBaseURL = "http://crm.example.com"
	if err := c.Validate(); err == nil {
		t.Fatal("expected an http:// CRM_WEB_APP_BASE_URL to be rejected in production")
	}

	c.WebAppBaseURL = "https://crm.example.com"
	if err := c.Validate(); err != nil {
		t.Fatalf("an https web app origin was rejected: %v", err)
	}
}

// TestLoad_WebAppBaseURLTrailingSlashIsNormalized: the callback appends a
// stored path beginning with "/", so a configured trailing slash would build
// "//settings" — which a browser reads as protocol-relative and resolves
// off-site.
func TestLoad_WebAppBaseURLTrailingSlashIsNormalized(t *testing.T) {
	t.Setenv("CRM_WEB_APP_BASE_URL", "https://crm.example.com/")
	if got := Load().WebAppBaseURL; got != "https://crm.example.com" {
		t.Errorf("WebAppBaseURL = %q, want the trailing slash removed", got)
	}
}

// TestSyncRetentionDefaultsToKeepingEverything. Silently deleting an
// organization's correspondence history because nobody set this would be
// losing records it was never told to lose.
func TestSyncRetentionDefaultsToKeepingEverything(t *testing.T) {
	if got := Load().SyncRetentionDays; got != 0 {
		t.Errorf("SyncRetentionDays = %d with nothing configured, want 0 (keep everything)", got)
	}
}

// TestSyncRetentionIgnoresAnUnreadableValue. A typo meaning "keep forever" is
// survivable; a typo meaning "delete everything older than now" is not.
func TestSyncRetentionIgnoresAnUnreadableValue(t *testing.T) {
	t.Setenv("SYNC_RETENTION_DAYS", "ninety")
	if got := Load().SyncRetentionDays; got != 0 {
		t.Errorf("SyncRetentionDays = %d for an unparseable value, want the default 0", got)
	}

	t.Setenv("SYNC_RETENTION_DAYS", "90")
	if got := Load().SyncRetentionDays; got != 90 {
		t.Errorf("SyncRetentionDays = %d, want 90", got)
	}
}

// TestNegativeRetentionIsRejected: a negative window would erase content
// dated in the future, which is every meeting not yet held.
func TestNegativeRetentionIsRejected(t *testing.T) {
	c := validConfig()
	c.SyncRetentionDays = -1
	if err := c.Validate(); err == nil {
		t.Fatal("a negative SYNC_RETENTION_DAYS was accepted")
	}
}

// TestGoogleOAuthGroupIsGoogleOnly is a regression test for a real outage in
// development: LEAD_CAPTURE_SECRET_KEY was added to validateGoogleOAuth's
// value map, which made setting it look like a half-configured Google client
// and crash-looped the service on startup.
//
// The check is all-or-nothing by design — a client ID without a token key is
// a CRM that offers a Connect button and cannot seal what it receives — so
// every value in that group must genuinely belong to Google. Adding an
// unrelated key to it is easy, silent in review, and immediately fatal.
func TestGoogleOAuthGroupIsGoogleOnly(t *testing.T) {
	// Lead capture configured, Google not configured at all. This is the
	// ordinary state of a deployment that accepts inbound leads and has not
	// connected any mailboxes.
	cfg := Config{LeadCaptureSecretKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	if err := cfg.validateGoogleOAuth(); err != nil {
		t.Fatalf("setting a non-Google key tripped the Google configuration check: %v", err)
	}

	// The check itself still works: a partial Google configuration is refused.
	partial := Config{GoogleOAuthClientID: "id-only"}
	if err := partial.validateGoogleOAuth(); err == nil {
		t.Error("a half-configured Google client was accepted; the all-or-nothing check is not working")
	}
}
