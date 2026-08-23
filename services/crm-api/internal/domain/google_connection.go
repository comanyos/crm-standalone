package domain

import (
	"fmt"
	"strings"
	"time"
)

// The Google integration's durable state (docs/GOOGLE_INTEGRATION.md).
//
// This file owns the shape and the invariants; memstore_google.go and
// pgstore_google.go own the two implementations, and neither is allowed to
// disagree with the other — google_connection_parity_test.go asserts that.
//
// Two things are deliberately absent from every type here:
//
//  1. Any plaintext credential. The provider's access token, refresh token
//     and the PKCE verifier reach this package only as ciphertext sealed by
//     internal/integration/googleoauth. The store persists opaque bytes and
//     has no way to open them.
//  2. Any provider secret. GOOGLE_OAUTH_CLIENT_SECRET never enters the domain
//     layer, the database, or a response body.

// The lifecycle states a Google connection can be in.
const (
	// GoogleStatusConnected means the stored token set is believed usable.
	GoogleStatusConnected = "connected"
	// GoogleStatusReauthorizationRequired means the credential no longer
	// works — consent withdrawn at Google, the refresh token expired, or the
	// granted scopes were narrowed. The row is kept so the UI can ask the
	// user to reconnect instead of silently going quiet.
	GoogleStatusReauthorizationRequired = "reauthorization_required"
	// GoogleStatusDisconnected is a tombstone: the credential has been
	// revoked at Google and erased here, and the row survives only so the
	// audit trail can answer "who disconnected this, and when".
	GoogleStatusDisconnected = "disconnected"
)

// googleStatuses mirrors the CHECK constraint in
// migrations/0019_google_connections.sql. A value valid in one and not the
// other means a write that passes Go validation dies in the database.
var googleStatuses = map[string]bool{
	GoogleStatusConnected:               true,
	GoogleStatusReauthorizationRequired: true,
	GoogleStatusDisconnected:            true,
}

// GoogleConnection is one CRM user's link to one Google account.
//
// It is keyed on the owner, not on the tenant, because Gmail and Calendar
// consent is granted by a person over their own mailbox. A tenant-wide
// connection would make one user's consent expose their mail to their whole
// organization.
type GoogleConnection struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
	// GoogleAccountEmail is the account the user actually consented with,
	// read back from Google rather than typed by the user. It is shown in the
	// UI so somebody who authorized the wrong account can see that they did.
	GoogleAccountEmail string `json:"google_account_email"`
	Status             string `json:"status"`
	// GrantedScopes is what Google returned, not what was requested. A
	// narrower list than requested is the normal shape of partial consent.
	GrantedScopes []string `json:"granted_scopes"`
	// TokenCiphertext is the sealed TokenSet. It is nil for a disconnected
	// connection and must never be serialized to a browser-facing response —
	// GoogleConnectionView is the type the API layer is allowed to return.
	TokenCiphertext      []byte    `json:"-"`
	AccessTokenExpiresAt time.Time `json:"access_token_expires_at"`
	ConnectedBy          string    `json:"connected_by"`
	ConnectedAt          time.Time `json:"connected_at"`
	LastRefreshedAt      time.Time `json:"last_refreshed_at"`
	DisconnectedAt       time.Time `json:"disconnected_at"`
	// LastError is a bounded reason code, never a provider response body:
	// those carry token and authorization material.
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate enforces the invariants the database also enforces, so a bad write
// fails the same way against either store.
func (c *GoogleConnection) Validate() error {
	if strings.TrimSpace(c.OwnerID) == "" {
		return fmt.Errorf("%w: google connection has no owner", ErrInvalid)
	}
	if !googleStatuses[c.Status] {
		return fmt.Errorf("%w: unknown google connection status %q", ErrInvalid, c.Status)
	}
	if c.Status == GoogleStatusDisconnected && len(c.TokenCiphertext) > 0 {
		return fmt.Errorf("%w: a disconnected google connection must hold no token material", ErrInvalid)
	}
	if c.Status == GoogleStatusConnected && len(c.TokenCiphertext) == 0 {
		return fmt.Errorf("%w: a connected google connection must hold a sealed token set", ErrInvalid)
	}
	if len(c.LastError) > maxGoogleErrorLength {
		return fmt.Errorf("%w: google connection error reason exceeds %d characters",
			ErrInvalid, maxGoogleErrorLength)
	}
	return nil
}

// maxGoogleErrorLength bounds LastError. It is a reason code, so the limit is
// generous for one; anything approaching it is a provider body that should
// never have been stored.
const maxGoogleErrorLength = 200

// NeedsRefresh reports whether the access token is expired, or close enough
// that a call started now would probably outlive it.
func (c *GoogleConnection) NeedsRefresh(now time.Time) bool {
	if c.Status != GoogleStatusConnected {
		return false
	}
	if c.AccessTokenExpiresAt.IsZero() {
		return true
	}
	return !now.UTC().Add(googleRefreshSkew).Before(c.AccessTokenExpiresAt.UTC())
}

// googleRefreshSkew is how long before real expiry a token is treated as
// already expired. Google issues one-hour access tokens; a minute of slack
// costs nothing and removes a whole class of "expired between the check and
// the call" failures.
const googleRefreshSkew = time.Minute

// GoogleConnectionView is the browser-facing projection: connection status
// without any credential material. The API layer returns this type; returning
// GoogleConnection itself would make leaking the ciphertext a one-line
// mistake.
type GoogleConnectionView struct {
	ID                   string    `json:"id"`
	OwnerID              string    `json:"owner_id"`
	GoogleAccountEmail   string    `json:"google_account_email"`
	Status               string    `json:"status"`
	GrantedScopes        []string  `json:"granted_scopes"`
	AccessTokenExpiresAt time.Time `json:"access_token_expires_at"`
	ConnectedBy          string    `json:"connected_by"`
	ConnectedAt          time.Time `json:"connected_at"`
	LastRefreshedAt      time.Time `json:"last_refreshed_at"`
	DisconnectedAt       time.Time `json:"disconnected_at"`
	LastError            string    `json:"last_error"`
}

// View projects the connection for a browser-facing response.
func (c *GoogleConnection) View() GoogleConnectionView {
	if c == nil {
		return GoogleConnectionView{}
	}
	return GoogleConnectionView{
		ID:                   c.ID,
		OwnerID:              c.OwnerID,
		GoogleAccountEmail:   c.GoogleAccountEmail,
		Status:               c.Status,
		GrantedScopes:        append([]string(nil), c.GrantedScopes...),
		AccessTokenExpiresAt: c.AccessTokenExpiresAt,
		ConnectedBy:          c.ConnectedBy,
		ConnectedAt:          c.ConnectedAt,
		LastRefreshedAt:      c.LastRefreshedAt,
		DisconnectedAt:       c.DisconnectedAt,
		LastError:            c.LastError,
	}
}

// GoogleOAuthState is the one-time record that ties an OAuth callback back to
// the authenticated tenant and actor who started the flow.
//
// The callback arrives as a plain browser redirect with no Authorization
// header, so this record is the *only* thing that can answer "whose
// authorization is this?". That is why it is written server-side before the
// redirect, why State is 256 bits of entropy, and why consuming it has to be
// atomic: a state that can be redeemed twice is a replayable authorization.
type GoogleOAuthState struct {
	State    string `json:"state"`
	ActorID  string `json:"actor_id"`
	OwnerID  string `json:"owner_id"`
	TenantID string `json:"tenant_id"`
	// VerifierCiphertext is the sealed PKCE verifier, bound to
	// (tenant, state) as associated data.
	VerifierCiphertext []byte `json:"-"`
	// ReturnPath is a validated same-origin path in the web application. It
	// is checked on the way in (ValidateGoogleReturnPath) rather than on the
	// way out, so a stored value can never become an open redirect.
	ReturnPath string    `json:"return_path"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	ConsumedAt time.Time `json:"consumed_at"`
}

// Validate enforces what the database also enforces.
func (s *GoogleOAuthState) Validate() error {
	switch {
	case strings.TrimSpace(s.State) == "":
		return fmt.Errorf("%w: oauth state has no state value", ErrInvalid)
	case strings.TrimSpace(s.ActorID) == "":
		return fmt.Errorf("%w: oauth state has no actor", ErrInvalid)
	case len(s.VerifierCiphertext) == 0:
		return fmt.Errorf("%w: oauth state has no sealed PKCE verifier", ErrInvalid)
	case s.ExpiresAt.IsZero():
		return fmt.Errorf("%w: oauth state has no expiry", ErrInvalid)
	case !s.CreatedAt.IsZero() && !s.ExpiresAt.After(s.CreatedAt):
		return fmt.Errorf("%w: oauth state expires before it was created", ErrInvalid)
	}
	if err := ValidateGoogleReturnPath(s.ReturnPath); err != nil {
		return err
	}
	return nil
}

// ValidateGoogleReturnPath accepts only a same-origin absolute path.
//
// The callback finishes by sending the browser somewhere, and "somewhere" is
// the one part of the flow an attacker gets to influence. Requiring a single
// leading slash rejects both absolute URLs ("https://evil.example") and
// protocol-relative ones ("//evil.example"), which browsers treat as absolute
// — the second is the form that gets missed. A backslash is rejected too:
// some clients normalize it to a slash, so "/\evil.example" is
// "//evil.example" by the time it is followed.
func ValidateGoogleReturnPath(path string) error {
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "\\\r\n\t ") || strings.Contains(path, "://") {
		return fmt.Errorf("%w: google return path must be a same-origin absolute path", ErrInvalid)
	}
	if len(path) > 512 {
		return fmt.Errorf("%w: google return path exceeds 512 characters", ErrInvalid)
	}
	return nil
}

// Usable reports whether the state may still be redeemed.
func (s *GoogleOAuthState) Usable(now time.Time) bool {
	return s != nil && s.ConsumedAt.IsZero() && now.UTC().Before(s.ExpiresAt.UTC())
}

// splitScopes parses the space-separated scope string Google returns.
func splitScopes(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// joinScopes renders scopes back into the provider's wire form.
func joinScopes(scopes []string) string { return strings.Join(scopes, " ") }

func cloneGoogleConnection(c *GoogleConnection) *GoogleConnection {
	if c == nil {
		return nil
	}
	out := *c
	out.GrantedScopes = append([]string(nil), c.GrantedScopes...)
	out.TokenCiphertext = append([]byte(nil), c.TokenCiphertext...)
	return &out
}

func cloneGoogleOAuthState(s *GoogleOAuthState) *GoogleOAuthState {
	if s == nil {
		return nil
	}
	out := *s
	out.VerifierCiphertext = append([]byte(nil), s.VerifierCiphertext...)
	return &out
}
