package scim

// filter.go implements a minimal SCIM filter parser covering the subset that
// Okta and Azure AD actually send:
//
//   userName eq "john@example.com"
//   emails.value eq "john@example.com"
//   externalId eq "00u1abc"
//   active eq true
//
// Grammar handled:
//   filter = attr SP op SP value
//   attr   = string (may contain ".")
//   op     = "eq" | "ne" | "co" | "sw" | "ew" | "pr"
//   value  = quoted-string | "true" | "false"

import (
	"encoding/json"
	"strings"
)

// Filter is a parsed SCIM filter expression.
type Filter struct {
	Attr  string // e.g. "userName", "emails.value"
	Op    string // "eq", "co", "sw", "pr", etc.
	Value string // unquoted
}

// ParseFilter parses a simple SCIM filter string.
// Returns nil if the filter is empty or unsupported.
func ParseFilter(raw string) *Filter {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.SplitN(raw, " ", 3)
	if len(parts) < 2 {
		return nil
	}
	f := &Filter{
		Attr: strings.ToLower(parts[0]),
		Op:   strings.ToLower(parts[1]),
	}
	if len(parts) == 3 {
		v := strings.TrimSpace(parts[2])
		// Strip surrounding quotes.
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		f.Value = v
	}
	return f
}

// MatchesUser reports whether a SCIM User satisfies the filter.
// Used by in-memory fakes and tests; the Postgres store passes the filter
// to the DB layer via matchUserSQL.
func (f *Filter) MatchesUser(u *User) bool {
	if f == nil {
		return true
	}
	switch f.Attr {
	case "username":
		return f.compare(u.UserName)
	case "externalid":
		return f.compare(u.ExternalID)
	case "active":
		active := f.Value == "true"
		if f.Op == "eq" {
			return u.Active == active
		}
	case "emails.value", "emails":
		for _, e := range u.Emails {
			if f.compare(e.Value) {
				return true
			}
		}
	}
	return false
}

func (f *Filter) compare(actual string) bool {
	switch f.Op {
	case "eq":
		return strings.EqualFold(actual, f.Value)
	case "ne":
		return !strings.EqualFold(actual, f.Value)
	case "co":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(f.Value))
	case "sw":
		return strings.HasPrefix(strings.ToLower(actual), strings.ToLower(f.Value))
	case "ew":
		return strings.HasSuffix(strings.ToLower(actual), strings.ToLower(f.Value))
	case "pr":
		return actual != ""
	}
	return false
}

// SQLClause translates the filter into a Postgres WHERE fragment and args.
// Returns ("", nil) for unsupported or empty filters.
func (f *Filter) SQLClause(startArg int) (clause string, args []any) {
	if f == nil {
		return "", nil
	}
	ph := func() string {
		s := "$" + itoa(startArg)
		startArg++
		return s
	}
	switch f.Attr {
	case "username":
		if f.Op == "eq" {
			return "AND LOWER(username) = LOWER(" + ph() + ")", []any{f.Value}
		}
	case "externalid":
		if f.Op == "eq" {
			return "AND external_id = " + ph(), []any{f.Value}
		}
	case "active":
		if f.Op == "eq" {
			return "AND active = " + ph(), []any{f.Value == "true"}
		}
	case "emails.value", "emails":
		if f.Op == "eq" {
			type emailEntry struct {
				Value string `json:"value"`
			}
			b, err := json.Marshal([]emailEntry{{Value: f.Value}})
			if err != nil {
				return "", nil
			}
			return "AND emails @> " + ph() + "::jsonb", []any{string(b)}
		}
	}
	return "", nil
}

func itoa(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return string(digits[n/10]) + string(digits[n%10])
}
