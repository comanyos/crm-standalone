// Package config centralizes the deployment-environment concept and
// production-safety startup validation for AuthinCore. It does not replace
// the ad hoc os.Getenv/envOr reads scattered through cmd/authincore — those
// still own their own defaults — it only gates whether the process is
// allowed to start at all with an unsafe configuration.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Environment is the deployment environment. It gates production-safety
// checks in ValidateProductionSafety — it is not a general feature flag and
// must not be used to branch other application behavior.
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

// EnvironmentFromEnv reads APP_ENV, defaulting to development. An unset,
// empty, or unrecognized value other than the four known ones is returned
// verbatim so ValidateProductionSafety can reject it with a clear error
// rather than silently defaulting a typo'd production deploy to development.
func EnvironmentFromEnv() Environment {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	if v == "" {
		return EnvDevelopment
	}
	return Environment(v)
}

// ValidateProductionSafety rejects a startup configuration that is unsafe for
// staging/production: a development-default or weak database password,
// sslmode=disable, no persistent JWT signing key (an ephemeral key breaks
// tokens on every restart and across replicas), a non-TLS issuer URL, or a
// non-HTTPS CORS origin. It is a no-op for development/test.
func ValidateProductionSafety(env Environment) error {
	if !env.valid() {
		return fmt.Errorf("config: APP_ENV must be development|test|staging|production, got %q", env)
	}
	if !env.IsProdLike() {
		return nil
	}

	var problems []string

	if dsn := os.Getenv("DATABASE_URL"); dsn == "" {
		problems = append(problems, "DATABASE_URL is required")
	} else {
		if pw := dsnPassword(dsn); weakPassword(pw) {
			problems = append(problems, "DATABASE_URL uses a development-default or weak password")
		}
		if strings.Contains(dsn, "sslmode=disable") {
			problems = append(problems, "DATABASE_URL must not use sslmode=disable")
		}
	}

	issuer := os.Getenv("ISSUER")
	if issuer == "" {
		problems = append(problems, "ISSUER is required")
	} else if !strings.HasPrefix(issuer, "https://") {
		problems = append(problems, "ISSUER must be an https:// URL")
	}

	if os.Getenv("RSA_PRIVATE_KEY_PEM") == "" &&
		os.Getenv("RSA_PRIVATE_KEY_FILE") == "" &&
		os.Getenv("RSA_PRIVATE_KEYS_JSON") == "" {
		problems = append(problems, "a persistent signing key is required "+
			"(set RSA_PRIVATE_KEY_PEM, RSA_PRIVATE_KEY_FILE, or RSA_PRIVATE_KEYS_JSON) "+
			"— an ephemeral key breaks tokens on every restart and across replicas")
	}

	for _, origin := range splitOrigins(os.Getenv("CORS_ALLOWED_ORIGINS")) {
		if !strings.HasPrefix(origin, "https://") {
			problems = append(problems, fmt.Sprintf("CORS_ALLOWED_ORIGINS entry %q must be an https:// origin", origin))
		}
	}

	// A scrape credential is mandatory outside development, matching
	// crm-api's CRM_METRICS_TOKEN rule. Without ADMIN_TOKEN the only way to
	// reach /metrics is an interactive session carrying metrics:read, which
	// no Prometheus collector has — so the practical effect of leaving it
	// unset in production is an endpoint nothing can scrape.
	if token := os.Getenv("ADMIN_TOKEN"); len(token) < 16 {
		problems = append(problems, "ADMIN_TOKEN is required and must be at least 16 characters")
	}

	if len(problems) > 0 {
		return fmt.Errorf("config: unsafe for %s:\n  - %s", env, strings.Join(problems, "\n  - "))
	}
	return nil
}

func splitOrigins(v string) []string {
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
