package config

import "testing"

func validConfig() Config {
	return Config{
		Mode:           ModeStandalone,
		Edition:        "OSS",
		Environment:    EnvDevelopment,
		AuthIssuer:     "https://auth.example.com",
		AuthJWKSURL:    "https://auth.example.com/.well-known/jwks.json",
		DatabaseURL:    "postgres://crm_app:a-sufficiently-long-generated-password@db.internal:5432/crm?sslmode=verify-full",
		OutboxDrainURL: "postgres://crm_drain:another-long-generated-password@db.internal:5432/crm?sslmode=verify-full",
		MetricsToken:   "a-sufficiently-long-metrics-token",
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

func TestValidate_ProductionRequiresHTTPSIssuer(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.AuthIssuer = "http://auth.example.com"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for non-https AUTH_ISSUER in production")
	}
}

func TestValidate_ProductionRequiresJWKSURL(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.AuthJWKSURL = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing AUTH_JWKS_URL in production")
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
	c.AuthIssuer = "http://auth.example.com"
	if err := c.Validate(); err == nil {
		t.Fatal("expected staging to enforce the same https requirement as production")
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

func TestValidate_ProductionRequiresMetricsToken(t *testing.T) {
	c := validConfig()
	c.Environment = EnvProduction
	c.MetricsToken = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("expected production to reject a missing or weak CRM_METRICS_TOKEN")
	}
}
