package config

import (
	"net/url"
	"strings"
)

// knownDevPasswords are the exact default passwords documented in
// .env.example and compose.yaml. A production/staging boot must never see
// one of these unchanged.
var knownDevPasswords = map[string]struct{}{
	"authincore_dev": {},
	"crm_admin_dev":  {},
	"crm_app":        {},
	"crm_drain":      {},
	"postgres":       {},
	"password":       {},
	"changeme":       {},
	"change-me":      {},
	"dev":            {},
	"test":           {},
}

// weakPassword reports whether pw is an obvious placeholder: empty, a known
// checked-in development default, or trivially short. It exists to catch an
// operator who deploys with the documented local defaults unchanged — it is
// not a substitute for a real secret-strength policy.
func weakPassword(pw string) bool {
	if pw == "" {
		return true
	}
	if _, known := knownDevPasswords[strings.ToLower(pw)]; known {
		return true
	}
	return len(pw) < 12
}

// dsnPassword extracts the password component of a Postgres connection
// string. Returns "" if the DSN has no embedded password or fails to parse.
func dsnPassword(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return ""
	}
	pw, _ := u.User.Password()
	return pw
}
