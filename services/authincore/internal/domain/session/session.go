package session

import (
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

type AuthMethod struct {
	Method      string    `json:"method"`
	CompletedAt time.Time `json:"completed_at"`
	Provider    string    `json:"provider,omitempty"`
}

type Device struct {
	ID         uuid.UUID `json:"id"`
	IPAddress  string    `json:"ip_address"`
	UserAgent  string    `json:"user_agent"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type Session struct {
	ID                    uuid.UUID    `json:"id"`
	IdentityID            uuid.UUID    `json:"identity_id"`
	OrgID                 uuid.UUID    `json:"org_id"`
	State                 State        `json:"state"`
	AAL                   AAL          `json:"aal"`
	AuthenticationMethods []AuthMethod `json:"authentication_methods,omitempty"`
	Devices               []Device     `json:"devices,omitempty"`
	AuthenticatedAt       time.Time    `json:"authenticated_at"`
	IssuedAt              time.Time    `json:"issued_at"`
	ExpiresAt             time.Time    `json:"expires_at"`
}

func New(identityID, orgID uuid.UUID, methods []AuthMethod, ttl time.Duration) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:                    uuid.New(),
		IdentityID:            identityID,
		OrgID:                 orgID,
		State:                 StateActive,
		AuthenticationMethods: methods,
		AuthenticatedAt:       now,
		IssuedAt:              now,
		ExpiresAt:             now.Add(ttl),
	}
}

func (s *Session) IsExpired() bool {
	return time.Now().UTC().After(s.ExpiresAt)
}

func (s *Session) Revoke() {
	s.State = StateInactive
}

func (s *Session) Extend(ttl time.Duration) {
	s.ExpiresAt = time.Now().UTC().Add(ttl)
}
