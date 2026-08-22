// Package settings implements the settings flow strategies.
// Two methods are supported:
//   - "password": change the current password (requires current password verification)
//   - "profile":  update identity traits (name, avatar, etc.)
package settings

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/authincore/authincore/internal/command"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

const (
	IDPassword = "settings_password"
	IDProfile  = "profile"
)

// Hasher verifies and hashes passwords.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, hash string) (bool, error)
}

// CredentialStore reads and writes password credentials.
type CredentialStore interface {
	FindIdentityByIdentifier(ctx context.Context, orgID uuid.UUID, identifier string) (uuid.UUID, string, error)
	StorePasswordCredential(ctx context.Context, identityID uuid.UUID, hash string) error
	GetHashByIdentityID(ctx context.Context, identityID uuid.UUID) (string, error)
}

// PasswordStrategy handles password changes within a settings flow.
type PasswordStrategy struct {
	hasher Hasher
	store  CredentialStore
}

func NewPasswordStrategy(hasher Hasher, store CredentialStore) *PasswordStrategy {
	return &PasswordStrategy{hasher: hasher, store: store}
}

func (s *PasswordStrategy) ID() string { return IDPassword }

func (s *PasswordStrategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	if f.Type != domflow.TypeSettings {
		return nil
	}
	f.UI.Nodes = append(f.UI.Nodes,
		domflow.UINode{
			Type:  "input",
			Group: "password",
			Attributes: mustJSON(map[string]any{
				"name":     "current_password",
				"type":     "password",
				"required": true,
			}),
		},
		domflow.UINode{
			Type:  "input",
			Group: "password",
			Attributes: mustJSON(map[string]any{
				"name":     "new_password",
				"type":     "password",
				"required": true,
			}),
		},
	)
	return nil
}

type changePasswordBody struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *PasswordStrategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	if f.IdentityID == nil {
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "settings flow requires an active session")
	}
	var b changePasswordBody
	if err := json.Unmarshal(body, &b); err != nil || b.CurrentPassword == "" || b.NewPassword == "" {
		addError(f, "current_password and new_password are required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "missing fields")
	}
	if b.CurrentPassword == b.NewPassword {
		addError(f, "new password must differ from current password")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "passwords match")
	}

	currentHash, err := s.store.GetHashByIdentityID(ctx, *f.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("settings password: fetch current hash: %w", err)
	}

	ok, err := s.hasher.Verify(b.CurrentPassword, currentHash)
	if err != nil {
		return nil, fmt.Errorf("settings password: verify: %w", err)
	}
	if !ok {
		addError(f, "current password is incorrect")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "wrong current password")
	}

	newHash, err := s.hasher.Hash(b.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("settings password: hash new: %w", err)
	}
	if err := s.store.StorePasswordCredential(ctx, *f.IdentityID, newHash); err != nil {
		return nil, fmt.Errorf("settings password: store: %w", err)
	}

	addInfo(f, "Password updated successfully.")
	return f.IdentityID, nil
}

// ProfileStrategy handles trait updates within a settings flow.
type ProfileStrategy struct {
	commands *command.Commands
}

func NewProfileStrategy(commands *command.Commands) *ProfileStrategy {
	return &ProfileStrategy{commands: commands}
}

func (s *ProfileStrategy) ID() string { return IDProfile }

func (s *ProfileStrategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	if f.Type != domflow.TypeSettings {
		return nil
	}
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "input",
		Group: "profile",
		Attributes: mustJSON(map[string]any{
			"name":     "traits",
			"type":     "hidden",
			"required": false,
		}),
	})
	return nil
}

type updateProfileBody struct {
	Traits json.RawMessage `json:"traits"`
}

func (s *ProfileStrategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	if f.IdentityID == nil {
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "settings flow requires an active session")
	}
	var b updateProfileBody
	if err := json.Unmarshal(body, &b); err != nil || len(b.Traits) == 0 {
		addError(f, "traits are required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "traits required")
	}

	_, err := s.commands.UpdateIdentity(ctx, command.UpdateIdentityCommand{
		ID:     *f.IdentityID,
		OrgID:  f.OrgID,
		Traits: b.Traits,
	})
	if err != nil {
		addError(f, "profile update failed")
		return nil, fmt.Errorf("settings profile update: %w", err)
	}

	addInfo(f, "Profile updated successfully.")
	return f.IdentityID, nil
}

func addError(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000004, Text: msg, Type: "error"})
}

func addInfo(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000003, Text: msg, Type: "info"})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
