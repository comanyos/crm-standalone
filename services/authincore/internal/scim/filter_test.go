package scim

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ParseFilter
// ---------------------------------------------------------------------------

func TestParseFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantNil   bool
		wantAttr  string
		wantOp    string
		wantValue string
	}{
		{
			name:      "userName eq",
			input:     `userName eq "john"`,
			wantAttr:  "username",
			wantOp:    "eq",
			wantValue: "john",
		},
		{
			name:      "emails.value co",
			input:     `emails.value co "@company.com"`,
			wantAttr:  "emails.value",
			wantOp:    "co",
			wantValue: "@company.com",
		},
		{
			name:      "active eq true (unquoted bool)",
			input:     `active eq true`,
			wantAttr:  "active",
			wantOp:    "eq",
			wantValue: "true",
		},
		{
			name:      "externalId eq",
			input:     `externalId eq "00u1abc"`,
			wantAttr:  "externalid",
			wantOp:    "eq",
			wantValue: "00u1abc",
		},
		{
			name:      "attr is lowercased",
			input:     `UserName eq "Alice"`,
			wantAttr:  "username",
			wantOp:    "eq",
			wantValue: "Alice",
		},
		{
			name:      "op is lowercased",
			input:     `userName EQ "alice"`,
			wantAttr:  "username",
			wantOp:    "eq",
			wantValue: "alice",
		},
		{
			name:      "sw operator",
			input:     `userName sw "a"`,
			wantAttr:  "username",
			wantOp:    "sw",
			wantValue: "a",
		},
		{
			name:      "ew operator",
			input:     `userName ew ".com"`,
			wantAttr:  "username",
			wantOp:    "ew",
			wantValue: ".com",
		},
		{
			name:      "ne operator",
			input:     `active ne true`,
			wantAttr:  "active",
			wantOp:    "ne",
			wantValue: "true",
		},
		{
			name:    "empty string returns nil",
			input:   "",
			wantNil: true,
		},
		{
			name:    "whitespace-only returns nil",
			input:   "   ",
			wantNil: true,
		},
		{
			name:    "single word returns nil",
			input:   "userName",
			wantNil: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := ParseFilter(tc.input)
			if tc.wantNil {
				assert.Nil(t, f)
				return
			}
			require.NotNil(t, f)
			assert.Equal(t, tc.wantAttr, f.Attr)
			assert.Equal(t, tc.wantOp, f.Op)
			assert.Equal(t, tc.wantValue, f.Value)
		})
	}
}

// ---------------------------------------------------------------------------
// SQLClause
// ---------------------------------------------------------------------------

func TestSQLClause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		filter     string
		startArg   int
		wantClause string
		wantArgs   []any
	}{
		{
			name:       "userName eq → LOWER comparison",
			filter:     `userName eq "john"`,
			startArg:   4,
			wantClause: "AND LOWER(username) = LOWER($4)",
			wantArgs:   []any{"john"},
		},
		{
			name:       "externalId eq → direct match",
			filter:     `externalId eq "00u1abc"`,
			startArg:   4,
			wantClause: "AND external_id = $4",
			wantArgs:   []any{"00u1abc"},
		},
		{
			name:       "active eq true → bool arg true",
			filter:     `active eq true`,
			startArg:   4,
			wantClause: "AND active = $4",
			wantArgs:   []any{true},
		},
		{
			name:       "active eq false → bool arg false",
			filter:     `active eq false`,
			startArg:   4,
			wantClause: "AND active = $4",
			wantArgs:   []any{false},
		},
		{
			name:       "emails.value eq → jsonb operator",
			filter:     `emails.value eq "alice@example.com"`,
			startArg:   4,
			wantClause: "AND emails @> $4::jsonb",
			wantArgs:   []any{`[{"value":"alice@example.com"}]`},
		},
		{
			name:       "nil filter returns empty",
			filter:     "",
			startArg:   4,
			wantClause: "",
			wantArgs:   nil,
		},
		{
			name:       "unsupported op returns empty",
			filter:     `userName sw "alice"`,
			startArg:   4,
			wantClause: "",
			wantArgs:   nil,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := ParseFilter(tc.filter)
			clause, args := f.SQLClause(tc.startArg)
			assert.Equal(t, tc.wantClause, clause)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}

// ---------------------------------------------------------------------------
// MatchesUser
// ---------------------------------------------------------------------------

func TestMatchesUser(t *testing.T) {
	t.Parallel()

	u := &User{
		UserName:   "alice@example.com",
		ExternalID: "ext-123",
		Active:     true,
		Emails: []Email{
			{Value: "alice@example.com", Primary: true},
			{Value: "alice.work@company.com"},
		},
	}

	tests := []struct {
		name   string
		filter string
		want   bool
	}{
		{"userName eq match", `userName eq "alice@example.com"`, true},
		{"userName eq no match", `userName eq "bob@example.com"`, false},
		{"userName eq case insensitive", `userName eq "ALICE@EXAMPLE.COM"`, true},
		{"externalId eq match", `externalId eq "ext-123"`, true},
		{"externalId eq no match", `externalId eq "other"`, false},
		{"active eq true match", `active eq true`, true},
		{"active eq false no match", `active eq false`, false},
		{"emails.value eq primary", `emails.value eq "alice@example.com"`, true},
		{"emails.value eq secondary", `emails.value eq "alice.work@company.com"`, true},
		{"emails.value eq no match", `emails.value eq "nobody@example.com"`, false},
		{"emails.value co domain", `emails.value co "@company.com"`, true},
		{"emails.value co no match", `emails.value co "@other.org"`, false},
		{"userName sw prefix", `userName sw "alice"`, true},
		{"userName sw no match", `userName sw "bob"`, false},
		{"userName ew suffix", `userName ew ".com"`, true},
		{"userName ew no match", `userName ew ".org"`, false},
		{"nil filter matches everything", ``, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := ParseFilter(tc.filter)
			got := f.MatchesUser(u)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMatchesUser_NilFilter(t *testing.T) {
	var f *Filter
	u := &User{UserName: "x@example.com"}
	assert.True(t, f.MatchesUser(u), "nil filter must match all users")
}

func TestMatchesUser_InactiveUser(t *testing.T) {
	f := ParseFilter(`active eq false`)
	require.NotNil(t, f)
	inactive := &User{Active: false}
	active := &User{Active: true}
	assert.True(t, f.MatchesUser(inactive))
	assert.False(t, f.MatchesUser(active))
}

func TestMatchesUser_NoEmails(t *testing.T) {
	f := ParseFilter(`emails.value eq "a@b.com"`)
	require.NotNil(t, f)
	u := &User{UserName: "u@u.com", Emails: nil}
	assert.False(t, f.MatchesUser(u), "user with no emails must not match email filter")
}
