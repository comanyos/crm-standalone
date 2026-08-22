// Package webauthn implements WebAuthn (passkey) enrollment and assertion.
//
// Enrollment (settings flow):
//  1. Empty body → generate registration options → store challenge → return options.
//  2. Body with attestation JSON → verify → store credential.
//
// Assertion (login flow):
//  1. Empty body → generate assertion options → store challenge.
//  2. Body with assertion JSON → verify → return identity ID.
package webauthn

import (
	"context"
	"encoding/json"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

const ID = "passkey"

// CredentialStore persists WebAuthn credentials.
type CredentialStore interface {
	SaveCredential(ctx context.Context, identityID uuid.UUID, cred *webauthn.Credential) error
	CredentialsForIdentity(ctx context.Context, identityID uuid.UUID) ([]webauthn.Credential, error)
	UpdateSignCount(ctx context.Context, credentialID []byte, count uint32) error
}

// SessionStore persists WebAuthn challenge sessions keyed by flow ID.
type SessionStore interface {
	SaveSession(ctx context.Context, flowID uuid.UUID, data *webauthn.SessionData) error
	GetSession(ctx context.Context, flowID uuid.UUID) (*webauthn.SessionData, error)
	DeleteSession(ctx context.Context, flowID uuid.UUID) error
}

// Strategy handles passkey enrollment (settings) and assertion (login).
type Strategy struct {
	wa      *webauthn.WebAuthn
	creds   CredentialStore
	session SessionStore
}

func New(wa *webauthn.WebAuthn, creds CredentialStore, session SessionStore) *Strategy {
	return &Strategy{wa: wa, creds: creds, session: session}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	switch f.Type {
	case domflow.TypeSettings:
		f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
			Type:  "input",
			Group: "passkey",
			Attributes: mustJSON(map[string]any{
				"name":  "passkey_register",
				"type":  "submit",
				"value": "register",
				"label": "Register a passkey",
			}),
		})
	case domflow.TypeLogin:
		f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
			Type:  "input",
			Group: "passkey",
			Attributes: mustJSON(map[string]any{
				"name":  "passkey_assertion",
				"type":  "submit",
				"value": "assert",
				"label": "Sign in with passkey",
			}),
		})
	}
	return nil
}

type passkeyBody struct {
	Attestation json.RawMessage `json:"attestation"`
	Assertion   json.RawMessage `json:"assertion"`
}

func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	switch f.Type {
	case domflow.TypeSettings:
		return s.handleEnroll(ctx, f, body)
	case domflow.TypeLogin:
		return s.handleAssert(ctx, f, body)
	default:
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "passkey not applicable to this flow type")
	}
}

func (s *Strategy) handleEnroll(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	if f.IdentityID == nil {
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "enrollment requires active session")
	}
	var b passkeyBody
	_ = json.Unmarshal(body, &b)
	if len(b.Attestation) == 0 || string(b.Attestation) == "null" {
		return s.enrollBegin(ctx, f)
	}
	return s.enrollFinish(ctx, f, b.Attestation)
}

func (s *Strategy) enrollBegin(ctx context.Context, f *domflow.Flow) (*uuid.UUID, error) {
	user := flowUser{id: *f.IdentityID}
	if creds, err := s.creds.CredentialsForIdentity(ctx, *f.IdentityID); err == nil {
		user.creds = creds
	}
	creation, sessionData, err := s.wa.BeginRegistration(user)
	if err != nil {
		return nil, fmt.Errorf("webauthn begin registration: %w", err)
	}
	if err := s.session.SaveSession(ctx, f.ID, sessionData); err != nil {
		return nil, fmt.Errorf("webauthn save session: %w", err)
	}
	optJSON, _ := json.Marshal(creation)
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{Type: "passkey_options", Group: "passkey", Attributes: optJSON})
	addInfo(f, "Register a new passkey using your authenticator.")
	return nil, nil //nolint:nilnil
}

func (s *Strategy) enrollFinish(ctx context.Context, f *domflow.Flow, attestationJSON json.RawMessage) (*uuid.UUID, error) {
	sessionData, err := s.session.GetSession(ctx, f.ID)
	if err != nil {
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "passkey session expired — restart enrollment")
	}
	_ = s.session.DeleteSession(ctx, f.ID)

	user := flowUser{id: *f.IdentityID}
	ccr, err := protocol.ParseCredentialCreationResponseBody(jsonReader(attestationJSON))
	if err != nil {
		addError(f, "Invalid attestation response.")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "parse attestation: "+err.Error())
	}
	cred, err := s.wa.CreateCredential(user, *sessionData, ccr)
	if err != nil {
		addError(f, "Passkey registration failed.")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "create credential: "+err.Error())
	}
	if err := s.creds.SaveCredential(ctx, *f.IdentityID, cred); err != nil {
		return nil, fmt.Errorf("save webauthn credential: %w", err)
	}
	addInfo(f, "Passkey registered successfully.")
	return f.IdentityID, nil
}

func (s *Strategy) handleAssert(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	if f.IdentityID == nil {
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "identity not resolved for assertion")
	}
	var b passkeyBody
	_ = json.Unmarshal(body, &b)
	if len(b.Assertion) == 0 || string(b.Assertion) == "null" {
		return s.assertBegin(ctx, f)
	}
	return s.assertFinish(ctx, f, b.Assertion)
}

func (s *Strategy) assertBegin(ctx context.Context, f *domflow.Flow) (*uuid.UUID, error) {
	creds, err := s.creds.CredentialsForIdentity(ctx, *f.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("load credentials for assertion: %w", err)
	}
	user := flowUser{id: *f.IdentityID, creds: creds}
	assertion, sessionData, err := s.wa.BeginLogin(user)
	if err != nil {
		return nil, fmt.Errorf("webauthn begin login: %w", err)
	}
	if err := s.session.SaveSession(ctx, f.ID, sessionData); err != nil {
		return nil, fmt.Errorf("save webauthn assertion session: %w", err)
	}
	optJSON, _ := json.Marshal(assertion)
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{Type: "passkey_options", Group: "passkey", Attributes: optJSON})
	addInfo(f, "Authenticate with your passkey.")
	return nil, nil //nolint:nilnil
}

func (s *Strategy) assertFinish(ctx context.Context, f *domflow.Flow, assertionJSON json.RawMessage) (*uuid.UUID, error) {
	sessionData, err := s.session.GetSession(ctx, f.ID)
	if err != nil {
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "passkey session expired — restart login")
	}
	_ = s.session.DeleteSession(ctx, f.ID)

	creds, err := s.creds.CredentialsForIdentity(ctx, *f.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("load credentials for finish: %w", err)
	}
	user := flowUser{id: *f.IdentityID, creds: creds}
	car, err := protocol.ParseCredentialRequestResponseBody(jsonReader(assertionJSON))
	if err != nil {
		addError(f, "Invalid assertion response.")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "parse assertion: "+err.Error())
	}
	cred, err := s.wa.ValidateLogin(user, *sessionData, car)
	if err != nil {
		addError(f, "Passkey authentication failed.")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "validate login: "+err.Error())
	}
	_ = s.creds.UpdateSignCount(ctx, cred.ID, cred.Authenticator.SignCount)
	return f.IdentityID, nil
}

// flowUser adapts a uuid.UUID identity to the webauthn.User interface.
type flowUser struct {
	id    uuid.UUID
	creds []webauthn.Credential
}

func (u flowUser) WebAuthnID() []byte                         { return u.id[:] }
func (u flowUser) WebAuthnName() string                       { return u.id.String() }
func (u flowUser) WebAuthnDisplayName() string                { return u.id.String() }
func (u flowUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }
func (u flowUser) WebAuthnIcon() string                       { return "" }

func addError(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000020, Text: msg, Type: "error"})
}

func addInfo(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000010, Text: msg, Type: "info"})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
