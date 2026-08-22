package config

import "testing"

func setSafeProdEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://authincore:a-sufficiently-long-generated-password@db.internal:5432/authincore?sslmode=verify-full")
	t.Setenv("ISSUER", "https://auth.example.com")
	t.Setenv("RSA_PRIVATE_KEY_PEM", "")
	t.Setenv("RSA_PRIVATE_KEY_FILE", "/run/secrets/jwt_private_key.pem")
	t.Setenv("RSA_PRIVATE_KEYS_JSON", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
}

func TestEnvironmentFromEnv_DefaultsToDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "")
	if got := EnvironmentFromEnv(); got != EnvDevelopment {
		t.Fatalf("expected development default, got %q", got)
	}
}

func TestValidateProductionSafety_DevelopmentIsNoOp(t *testing.T) {
	if err := ValidateProductionSafety(EnvDevelopment); err != nil {
		t.Fatalf("development should not enforce production safety: %v", err)
	}
}

func TestValidateProductionSafety_UnknownEnvironmentRejected(t *testing.T) {
	if err := ValidateProductionSafety("prod"); err == nil {
		t.Fatal("expected error for unrecognized APP_ENV")
	}
}

func TestValidateProductionSafety_RequiresDatabaseURL(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("DATABASE_URL", "")
	if err := ValidateProductionSafety(EnvProduction); err == nil {
		t.Fatal("expected error for missing DATABASE_URL in production")
	}
}

func TestValidateProductionSafety_RejectsDevDefaultPassword(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("DATABASE_URL", "postgres://authincore:authincore_dev@db.internal:5432/authincore?sslmode=verify-full")
	if err := ValidateProductionSafety(EnvProduction); err == nil {
		t.Fatal("expected error for development-default database password in production")
	}
}

func TestValidateProductionSafety_RejectsSSLModeDisable(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("DATABASE_URL", "postgres://authincore:a-sufficiently-long-generated-password@db.internal:5432/authincore?sslmode=disable")
	if err := ValidateProductionSafety(EnvProduction); err == nil {
		t.Fatal("expected error for sslmode=disable in production")
	}
}

func TestValidateProductionSafety_RequiresHTTPSIssuer(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("ISSUER", "http://auth.example.com")
	if err := ValidateProductionSafety(EnvProduction); err == nil {
		t.Fatal("expected error for non-https ISSUER in production")
	}
}

func TestValidateProductionSafety_RequiresPersistentSigningKey(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("RSA_PRIVATE_KEY_FILE", "")
	if err := ValidateProductionSafety(EnvProduction); err == nil {
		t.Fatal("expected error when no persistent signing key is configured in production")
	}
}

func TestValidateProductionSafety_AcceptsPEMOrJSONKeySource(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("RSA_PRIVATE_KEY_FILE", "")
	t.Setenv("RSA_PRIVATE_KEY_PEM", "-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----")
	if err := ValidateProductionSafety(EnvProduction); err != nil {
		t.Fatalf("RSA_PRIVATE_KEY_PEM alone should satisfy the signing-key requirement: %v", err)
	}
}

func TestValidateProductionSafety_RejectsNonHTTPSCORSOrigin(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000")
	if err := ValidateProductionSafety(EnvProduction); err == nil {
		t.Fatal("expected error for non-https CORS origin in production")
	}
}

func TestValidateProductionSafety_StagingAppliesSameRulesAsProduction(t *testing.T) {
	setSafeProdEnv(t)
	t.Setenv("ISSUER", "http://auth.example.com")
	if err := ValidateProductionSafety(EnvStaging); err == nil {
		t.Fatal("expected staging to enforce the same https requirement as production")
	}
}

func TestValidateProductionSafety_AcceptsSafeConfig(t *testing.T) {
	setSafeProdEnv(t)
	if err := ValidateProductionSafety(EnvProduction); err != nil {
		t.Fatalf("expected safe production config to validate, got: %v", err)
	}
}
