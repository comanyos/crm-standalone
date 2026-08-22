// Package password implements the password credential strategy for login and registration flows.
package password

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alexedwards/argon2id"
	"github.com/authincore/authincore/internal/account/lockout"
	"github.com/authincore/authincore/internal/command"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

const ID = "password"

const msgInvalidCredentials = "invalid credentials" // #nosec G101 -- public authentication error text, not a credential.

// Hasher hashes and verifies passwords.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, hash string) (bool, error)
}

// CredentialStore reads/writes password credentials.
type CredentialStore interface {
	FindIdentityByIdentifier(ctx context.Context, orgID uuid.UUID, identifier string) (identityID uuid.UUID, passwordHash string, err error)
	StorePasswordCredential(ctx context.Context, identityID uuid.UUID, hash string) error
	AddIdentifier(ctx context.Context, identityID uuid.UUID, identifier string) error
}

// Strategy handles both login and registration password flows.
type Strategy struct {
	hasher   Hasher
	store    CredentialStore
	commands *command.Commands
	lockout  *lockout.Service // nil disables lockout
}

func New(hasher Hasher, store CredentialStore, commands *command.Commands, lockoutSvc *lockout.Service) *Strategy {
	return &Strategy{
		hasher:   hasher,
		store:    store,
		commands: commands,
		lockout:  lockoutSvc,
	}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	nodes := []domflow.UINode{
		{
			Type:  "input",
			Group: "password",
			Attributes: mustJSON(map[string]any{
				"name":     "identifier",
				"type":     "email",
				"required": true,
			}),
		},
		{
			Type:  "input",
			Group: "password",
			Attributes: mustJSON(map[string]any{
				"name":     "password",
				"type":     "password",
				"required": true,
			}),
		},
	}
	if f.Type == domflow.TypeRegistration {
		nodes = append(nodes, domflow.UINode{
			Type:  "input",
			Group: "password",
			Attributes: mustJSON(map[string]any{
				"name":     "traits.email",
				"type":     "email",
				"required": true,
			}),
		})
	}
	nodes = append(nodes, domflow.UINode{
		Type:  "input",
		Group: "password",
		Attributes: mustJSON(map[string]any{
			"name":  "method",
			"type":  "submit",
			"value": "password",
		}),
	})
	f.UI.Nodes = append(f.UI.Nodes, nodes...)
	return nil
}

// Handle dispatches to login or registration based on flow type.
func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	switch f.Type {
	case domflow.TypeLogin:
		return s.handleLogin(ctx, f, body)
	case domflow.TypeRegistration:
		return s.handleRegistration(ctx, f, body)
	default:
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "password strategy does not handle this flow type")
	}
}

// --- Login ---

type loginBody struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

func (s *Strategy) handleLogin(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b loginBody
	if err := json.Unmarshal(body, &b); err != nil || b.Identifier == "" || b.Password == "" {
		addError(f, "identifier and password are required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "missing credentials")
	}

	identityID, hash, err := s.store.FindIdentityByIdentifier(ctx, f.OrgID, b.Identifier)
	if err != nil {
		// Always hash to prevent timing-based username enumeration.
		_, _ = s.hasher.Verify("dummy", "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		addError(f, msgInvalidCredentials)
		return nil, domainerr.New(domainerr.CodeUnauthenticated, msgInvalidCredentials)
	}

	if s.lockout != nil {
		if err := s.lockout.CheckLocked(ctx, f.OrgID, identityID); err != nil {
			if le, ok := err.(*lockout.LockedError); ok {
				addError(f, fmt.Sprintf("account locked. Retry after %s.", le.RetryAfter))
			}
			return nil, domainerr.New(domainerr.CodeUnauthenticated, err.Error())
		}
	}

	ok, err := s.hasher.Verify(b.Password, hash)
	if err != nil {
		return nil, fmt.Errorf("password verify: %w", err)
	}
	if !ok {
		if s.lockout != nil {
			_ = s.lockout.RecordFailure(ctx, f.OrgID, identityID)
		}
		addError(f, msgInvalidCredentials)
		return nil, domainerr.New(domainerr.CodeUnauthenticated, msgInvalidCredentials)
	}

	if s.lockout != nil {
		_ = s.lockout.RecordSuccess(ctx, f.OrgID, identityID)
	}
	return &identityID, nil
}

// --- Registration ---

type registrationBody struct {
	Traits   json.RawMessage `json:"traits"`
	Password string          `json:"password"`
	SchemaID string          `json:"schema_id"`
}

func (s *Strategy) handleRegistration(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b registrationBody
	if err := json.Unmarshal(body, &b); err != nil {
		addError(f, "invalid request body")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "invalid body")
	}
	if b.Password == "" {
		addError(f, "password is required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "password is required")
	}

	schemaID := b.SchemaID
	if schemaID == "" {
		schemaID = "default"
	}
	traits := b.Traits
	if len(traits) == 0 {
		traits = json.RawMessage(`{}`)
	}

	var traitMap map[string]any
	_ = json.Unmarshal(traits, &traitMap)
	identifier, _ := traitMap["email"].(string)
	if identifier == "" {
		addError(f, "traits.email is required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "traits.email required")
	}

	hash, err := s.hasher.Hash(b.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	identity, err := s.commands.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID:    f.OrgID,
		SchemaID: schemaID,
		Traits:   traits,
	})
	if err != nil {
		if domainerr.IsAlreadyExists(err) {
			addError(f, "an account with that email already exists")
		} else {
			addError(f, "registration failed")
		}
		return nil, err
	}

	if err := s.store.StorePasswordCredential(ctx, identity.ID, hash); err != nil {
		addError(f, "registration failed")
		return nil, fmt.Errorf("store credential: %w", err)
	}

	if err := s.store.AddIdentifier(ctx, identity.ID, identifier); err != nil {
		// Identifier linkage failure is non-fatal for the registration itself.
		_ = err
	}

	return &identity.ID, nil
}

// helpers

func addError(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{
		ID:   4000001,
		Text: msg,
		Type: "error",
	})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// --- Argon2id hasher (OWASP recommended params) ---

type Argon2idHasher struct {
	params *argon2id.Params
}

func NewArgon2idHasher() *Argon2idHasher {
	return &Argon2idHasher{
		params: &argon2id.Params{
			Memory:      64 * 1024,
			Iterations:  1,
			Parallelism: 4,
			SaltLength:  16,
			KeyLength:   32,
		},
	}
}

func (h *Argon2idHasher) Hash(password string) (string, error) {
	hash, err := argon2id.CreateHash(password, h.params)
	if err != nil {
		return "", fmt.Errorf("argon2id hash: %w", err)
	}
	return hash, nil
}

func (h *Argon2idHasher) Verify(password, hash string) (bool, error) {
	match, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return false, fmt.Errorf("argon2id verify: %w", err)
	}
	return match, nil
}
