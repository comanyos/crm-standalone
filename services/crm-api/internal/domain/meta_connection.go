package domain

import (
	"fmt"
	"strings"
	"time"
)

// Meta Lead Ads connection lifecycle. selection_required is a successful
// OAuth grant that has not yet been bound to a Page and Instant Form.
const (
	MetaStatusSelectionRequired       = "selection_required"
	MetaStatusConnected               = "connected"
	MetaStatusReauthorizationRequired = "reauthorization_required"
	MetaStatusDisconnected            = "disconnected"
)

var metaStatuses = map[string]bool{
	MetaStatusSelectionRequired:       true,
	MetaStatusConnected:               true,
	MetaStatusReauthorizationRequired: true,
	MetaStatusDisconnected:            true,
}

// MetaConnection is the tenant-wide Facebook/Instagram Lead Ads link. Token
// material is always ciphertext and is deliberately absent from View.
type MetaConnection struct {
	ID                   string
	MetaUserID           string
	MetaUserName         string
	Status               string
	GrantedPermissions   []string
	UserTokenCiphertext  []byte
	PageTokenCiphertext  []byte
	AccessTokenExpiresAt time.Time
	BusinessID           string
	BusinessName         string
	PageID               string
	PageName             string
	FormID               string
	FormName             string
	ConnectedBy          string
	ConnectedAt          time.Time
	LastWebhookAt        time.Time
	LastImportAt         time.Time
	DisconnectedAt       time.Time
	LastError            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (c *MetaConnection) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: meta connection has no id", ErrInvalid)
	}
	if !metaStatuses[c.Status] {
		return fmt.Errorf("%w: unknown meta connection status %q", ErrInvalid, c.Status)
	}
	if len(c.LastError) > 200 {
		return fmt.Errorf("%w: meta connection error reason exceeds 200 characters", ErrInvalid)
	}
	switch c.Status {
	case MetaStatusSelectionRequired:
		if len(c.UserTokenCiphertext) == 0 {
			return fmt.Errorf("%w: a pending Meta selection must hold a sealed user token", ErrInvalid)
		}
	case MetaStatusConnected:
		if len(c.UserTokenCiphertext) == 0 || len(c.PageTokenCiphertext) == 0 {
			return fmt.Errorf("%w: a connected Meta account must hold sealed user and Page tokens", ErrInvalid)
		}
		if strings.TrimSpace(c.BusinessID) == "" || strings.TrimSpace(c.PageID) == "" || strings.TrimSpace(c.FormID) == "" {
			return fmt.Errorf("%w: a connected Meta account must select a Business, Page and form", ErrInvalid)
		}
	case MetaStatusReauthorizationRequired, MetaStatusDisconnected:
		if len(c.UserTokenCiphertext) > 0 || len(c.PageTokenCiphertext) > 0 {
			return fmt.Errorf("%w: an inactive Meta connection must hold no token material", ErrInvalid)
		}
	}
	return nil
}

type MetaConnectionView struct {
	ID                   string
	MetaUserID           string
	MetaUserName         string
	Status               string
	GrantedPermissions   []string
	AccessTokenExpiresAt time.Time
	BusinessID           string
	BusinessName         string
	PageID               string
	PageName             string
	FormID               string
	FormName             string
	ConnectedBy          string
	ConnectedAt          time.Time
	LastWebhookAt        time.Time
	LastImportAt         time.Time
	DisconnectedAt       time.Time
	LastError            string
}

func (c *MetaConnection) View() MetaConnectionView {
	if c == nil {
		return MetaConnectionView{}
	}
	return MetaConnectionView{
		ID: c.ID, MetaUserID: c.MetaUserID, MetaUserName: c.MetaUserName,
		Status: c.Status, GrantedPermissions: append([]string(nil), c.GrantedPermissions...),
		AccessTokenExpiresAt: c.AccessTokenExpiresAt,
		BusinessID:           c.BusinessID, BusinessName: c.BusinessName,
		PageID: c.PageID, PageName: c.PageName, FormID: c.FormID, FormName: c.FormName,
		ConnectedBy: c.ConnectedBy, ConnectedAt: c.ConnectedAt,
		LastWebhookAt: c.LastWebhookAt, LastImportAt: c.LastImportAt,
		DisconnectedAt: c.DisconnectedAt, LastError: c.LastError,
	}
}

type MetaOAuthState struct {
	State      string
	TenantID   string
	ActorID    string
	ReturnPath string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt time.Time
}

func (s *MetaOAuthState) Validate() error {
	switch {
	case strings.TrimSpace(s.State) == "":
		return fmt.Errorf("%w: Meta OAuth state is blank", ErrInvalid)
	case strings.TrimSpace(s.ActorID) == "":
		return fmt.Errorf("%w: Meta OAuth state has no actor", ErrInvalid)
	case s.ExpiresAt.IsZero():
		return fmt.Errorf("%w: Meta OAuth state has no expiry", ErrInvalid)
	case !s.CreatedAt.IsZero() && !s.ExpiresAt.After(s.CreatedAt):
		return fmt.Errorf("%w: Meta OAuth state expires before creation", ErrInvalid)
	}
	return ValidateGoogleReturnPath(s.ReturnPath)
}

func (s *MetaOAuthState) Usable(now time.Time) bool {
	return s != nil && s.ConsumedAt.IsZero() && now.UTC().Before(s.ExpiresAt.UTC())
}

// MetaConnectionRef is the minimum cross-tenant webhook routing result. The
// ciphertext is useless without the deployment key and its tenant/id binding.
type MetaConnectionRef struct {
	TenantID            string
	ConnectionID        string
	PageTokenCiphertext []byte
	BusinessID          string
	PageID              string
	FormID              string
	Status              string
}

// MetaLeadAttribution is the provider identity and campaign lineage attached
// to a Lead. ProviderLeadID is the idempotency key for webhook retries and
// historical-import overlap.
type MetaLeadAttribution struct {
	LeadID            string
	ConnectionID      string
	ProviderLeadID    string
	Platform          string
	PageID            string
	FormID            string
	AdID              string
	AdName            string
	AdSetID           string
	AdSetName         string
	CampaignID        string
	CampaignName      string
	ProviderCreatedAt time.Time
	CapturedAt        time.Time
	RawPayload        string
}

func (a *MetaLeadAttribution) Validate() error {
	if a == nil || strings.TrimSpace(a.ProviderLeadID) == "" || strings.TrimSpace(a.ConnectionID) == "" {
		return fmt.Errorf("%w: Meta attribution needs connection and provider lead ids", ErrInvalid)
	}
	if len(a.RawPayload) > MaxCaptureBodyBytes {
		return fmt.Errorf("%w: Meta lead payload exceeds %d bytes", ErrInvalid, MaxCaptureBodyBytes)
	}
	return nil
}

func cloneMetaConnection(v *MetaConnection) *MetaConnection {
	if v == nil {
		return nil
	}
	out := *v
	out.GrantedPermissions = append([]string(nil), v.GrantedPermissions...)
	out.UserTokenCiphertext = append([]byte(nil), v.UserTokenCiphertext...)
	out.PageTokenCiphertext = append([]byte(nil), v.PageTokenCiphertext...)
	return &out
}

func cloneMetaOAuthState(v *MetaOAuthState) *MetaOAuthState {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneMetaAttribution(v *MetaLeadAttribution) *MetaLeadAttribution {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
