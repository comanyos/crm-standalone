package main

import "testing"

func TestBootstrapConfigFromEnv(t *testing.T) {
	values := map[string]string{
		"BOOTSTRAP_ORG_NAME": "Acme", "BOOTSTRAP_ORG_DOMAIN": "ACME.TEST:443",
		"BOOTSTRAP_ADMIN_EMAIL": "ADMIN@ACME.TEST", "BOOTSTRAP_ADMIN_PASSWORD": "a-safe-password",
	}
	cfg, err := bootstrapConfigFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OrgDomain != "acme.test" || cfg.AdminEmail != "admin@acme.test" {
		t.Fatalf("normalization = domain %q email %q", cfg.OrgDomain, cfg.AdminEmail)
	}
	if cfg.ClientID != "crm-web" || cfg.RedirectURI != "http://localhost:3000/auth/callback" {
		t.Fatalf("defaults = client %q callback %q", cfg.ClientID, cfg.RedirectURI)
	}
}

func TestBootstrapConfigFromEnvRejectsUnsafeInput(t *testing.T) {
	_, err := bootstrapConfigFromEnv(func(key string) string {
		if key == "BOOTSTRAP_ADMIN_PASSWORD" {
			return "short"
		}
		return "value"
	})
	if err == nil {
		t.Fatal("expected password validation error")
	}
}
