package flow

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Type int

const (
	TypeUnspecified Type = iota
	TypeLogin
	TypeRegistration
	TypeRecovery
	TypeVerification
	TypeSettings
	TypeLogout
)

type State int

const (
	StateUnspecified State = iota
	StateChooseMethod
	StateSentEmail
	StatePassedChallenge
)

// UINode represents a rendered form element (Kratos UINode pattern).
type UINode struct {
	Type       string
	Group      string
	Attributes json.RawMessage
	Messages   []UIMessage
	Meta       json.RawMessage
}

type UIMessage struct {
	ID      int64
	Text    string
	Type    string // info | error | success
	Context json.RawMessage
}

type UI struct {
	Action   string
	Method   string
	Nodes    []UINode
	Messages []UIMessage
}

// Flow is an in-flight self-service interaction (Kratos flow model).
type Flow struct {
	ID                   uuid.UUID
	Type                 Type
	State                State
	OrgID                uuid.UUID
	IdentityID           *uuid.UUID // set after identity resolved
	ReturnTo             string
	OAuth2LoginChallenge string
	UI                   UI
	InternalContext      json.RawMessage // persisted flow state, not exposed to user
	IssuedAt             time.Time
	ExpiresAt            time.Time
	UpdatedAt            time.Time
}

const DefaultFlowTTL = 30 * time.Minute

func New(flowType Type, orgID uuid.UUID, returnTo string) *Flow {
	now := time.Now().UTC()
	return &Flow{
		ID:        uuid.New(),
		Type:      flowType,
		State:     StateChooseMethod,
		OrgID:     orgID,
		ReturnTo:  returnTo,
		UI:        UI{Method: "POST"},
		IssuedAt:  now,
		ExpiresAt: now.Add(DefaultFlowTTL),
		UpdatedAt: now,
	}
}

func (f *Flow) IsExpired() bool {
	return time.Now().UTC().After(f.ExpiresAt)
}

func (f *Flow) SetState(s State) {
	f.State = s
	f.UpdatedAt = time.Now().UTC()
}

func (f *Flow) AddMessage(msg UIMessage) {
	f.UI.Messages = append(f.UI.Messages, msg)
	f.UpdatedAt = time.Now().UTC()
}
