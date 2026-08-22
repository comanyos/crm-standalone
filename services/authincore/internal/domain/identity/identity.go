package identity

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type State int

const (
	StateUnspecified State = iota
	StateActive
	StateInactive
)

type AAL int

const (
	AAL0 AAL = iota
	AAL1
	AAL2
)

type CredentialType string

const (
	CredentialPassword CredentialType = "password"
	CredentialOIDC     CredentialType = "oidc"
	CredentialTOTP     CredentialType = "totp"
	CredentialWebAuthn CredentialType = "webauthn"
	CredentialLookup   CredentialType = "lookup_secret"
	CredentialCode     CredentialType = "code"
)

// Credential holds the versioned, opaque payload for a single auth method.
type Credential struct {
	ID          uuid.UUID       `json:"id"`
	IdentityID  uuid.UUID       `json:"identity_id"`
	Type        CredentialType  `json:"type"`
	Identifiers []string        `json:"identifiers"`
	Config      json.RawMessage `json:"config,omitempty"`
	Version     string          `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Identity is the core user entity — JSON-schema-driven traits (Kratos pattern).
type Identity struct {
	ID             uuid.UUID                      `json:"id"`
	OrgID          uuid.UUID                      `json:"org_id"`
	ExternalID     *string                        `json:"external_id,omitempty"`
	SchemaID       string                         `json:"schema_id"`
	Traits         json.RawMessage                `json:"traits"`
	State          State                          `json:"state"`
	Credentials    map[CredentialType]*Credential `json:"credentials,omitempty"`
	AvailableAAL   AAL                            `json:"available_aal"`
	MetadataPublic json.RawMessage                `json:"metadata_public,omitempty"`
	MetadataAdmin  json.RawMessage                `json:"metadata_admin,omitempty"`
	CreatedAt      time.Time                      `json:"created_at"`
	UpdatedAt      time.Time                      `json:"updated_at"`
}

// New creates a minimal, active identity. Trait validation happens at the command layer.
func New(orgID uuid.UUID, schemaID string, traits json.RawMessage) *Identity {
	now := time.Now().UTC()
	return &Identity{
		ID:          uuid.New(),
		OrgID:       orgID,
		SchemaID:    schemaID,
		Traits:      traits,
		State:       StateActive,
		Credentials: make(map[CredentialType]*Credential),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func (i *Identity) Deactivate() {
	i.State = StateInactive
	i.UpdatedAt = time.Now().UTC()
}

func (i *Identity) Reactivate() {
	i.State = StateActive
	i.UpdatedAt = time.Now().UTC()
}

func (i *Identity) SetCredential(c *Credential) {
	i.Credentials[c.Type] = c
	i.recalcAAL()
	i.UpdatedAt = time.Now().UTC()
}

func (i *Identity) RemoveCredential(t CredentialType) {
	delete(i.Credentials, t)
	i.recalcAAL()
	i.UpdatedAt = time.Now().UTC()
}

func (i *Identity) recalcAAL() {
	_, hasMFA := i.Credentials[CredentialTOTP]
	if !hasMFA {
		_, hasMFA = i.Credentials[CredentialWebAuthn]
	}
	if hasMFA {
		i.AvailableAAL = AAL2
	} else if _, ok := i.Credentials[CredentialPassword]; ok {
		i.AvailableAAL = AAL1
	} else {
		i.AvailableAAL = AAL0
	}
}
