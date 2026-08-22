package domain

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// AccountFilter narrows ListAccounts. The zero value matches every account in
// the tenant. Every field is optional and combines with AND.
type AccountFilter struct {
	// NameContains matches Name case-insensitively, substring anywhere.
	NameContains string
	// Tier, when set, matches Tier exactly.
	Tier string
	// OwnerID, when set, matches OwnerID exactly (e.g. "my accounts").
	OwnerID string
	// ParentID, when set, matches ParentID exactly — the "child accounts of
	// this account" query an account detail page's hierarchy view needs.
	ParentID string
}

// AccountSort selects ListAccounts' order. This is an allowlist, not a
// free-form column name: the zero value and every other valid value are
// enumerated below, and ParseAccountSort rejects anything else at the RPC
// boundary rather than passing an arbitrary string down to a query.
type AccountSort string

const (
	// SortAccountName orders by Name ascending, tie-broken by ID — the
	// default. Random-order IDs (see id.go) are not a meaningful sort on
	// their own, so ListAccounts never falls back to "order by id" alone.
	SortAccountName AccountSort = ""
	// SortAccountCreatedDesc orders newest-first, tie-broken by ID descending.
	SortAccountCreatedDesc AccountSort = "-created_at"
)

// ParseAccountSort validates a raw sort string from a request against the
// allowlist above. Called at the RPC boundary (internal/api) so an unknown
// value is rejected as ErrInvalid before it reaches a store.
func ParseAccountSort(raw string) (AccountSort, error) {
	switch AccountSort(raw) {
	case SortAccountName, SortAccountCreatedDesc:
		return AccountSort(raw), nil
	default:
		return "", fmt.Errorf("%w: unknown sort %q", ErrInvalid, raw)
	}
}

// ContactFilter narrows ListContacts. The zero value matches every contact
// in the tenant. Every field is optional and combines with AND.
type ContactFilter struct {
	// AccountID, when set, matches AccountID exactly — the "contacts of this
	// account" query an account detail page needs.
	AccountID string
	// NameContains matches FirstName or LastName case-insensitively.
	NameContains string
	// EmailContains matches Email case-insensitively, substring anywhere.
	EmailContains string
}

// ContactSort selects ListContacts' order — see AccountSort's doc comment
// for why this is an allowlist rather than a free-form column name.
type ContactSort string

const (
	// SortContactCreatedDesc orders newest-first, tie-broken by ID
	// descending — the default.
	SortContactCreatedDesc ContactSort = ""
	// SortContactEmail orders by Email ascending, tie-broken by ID.
	SortContactEmail ContactSort = "email"
)

// ParseContactSort validates a raw sort string from a request against the
// allowlist above.
func ParseContactSort(raw string) (ContactSort, error) {
	switch ContactSort(raw) {
	case SortContactCreatedDesc, SortContactEmail:
		return ContactSort(raw), nil
	default:
		return "", fmt.Errorf("%w: unknown sort %q", ErrInvalid, raw)
	}
}

// cursorSep separates a keyset cursor's sort-value from its tie-breaking ID.
// IDs are hex (id.go) and sort values are base64url-encoded, so neither side
// can contain this byte — safe to split on the last occurrence.
const cursorSep = "."

// encodeCursor packs a keyset cursor's sort value and tie-breaking ID into
// an opaque page token.
func encodeCursor(sortValue, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(sortValue)) + cursorSep + id
}

// decodeCursor unpacks a page token produced by encodeCursor. An empty token
// (the first page) decodes to ("", "", true) — both fields absent, no error.
func decodeCursor(token string) (sortValue, id string, ok bool) {
	if token == "" {
		return "", "", true
	}
	i := strings.LastIndex(token, cursorSep)
	if i < 0 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token[:i])
	if err != nil {
		return "", "", false
	}
	return string(raw), token[i+1:], true
}
