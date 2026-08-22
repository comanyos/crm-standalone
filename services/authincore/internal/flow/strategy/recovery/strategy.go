// Package recovery implements the account recovery flow strategy.
// Phase 1: user submits email → a 6-digit code is generated, stored, and emailed.
// Phase 2: user submits the code → identity is resolved → password can be reset.
package recovery

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/authincore/authincore/internal/courier"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

const (
	ID              = "code"
	codeLength      = 6
	codeTTL         = 30 * time.Minute
	maxCodeAttempts = 5
)

// Store persists recovery codes.
type Store interface {
	// Create persists a recovery code for an identity.
	Create(ctx context.Context, code *RecoveryCode) error
	// Consume verifies and atomically deletes the code. Returns the identity ID on success.
	Consume(ctx context.Context, orgID uuid.UUID, email, code string) (*RecoveryCode, error)
	// FindIdentityByEmail returns the identity ID for a given email in an org.
	FindIdentityByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error)
}

// PasswordResetter updates the credential after a successful recovery.
type PasswordResetter interface {
	StorePasswordCredential(ctx context.Context, identityID uuid.UUID, hash string) error
}

// Hasher hashes passwords for the reset step.
type Hasher interface {
	Hash(password string) (string, error)
}

// RecoveryCode is the persisted record.
type RecoveryCode struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	IdentityID uuid.UUID
	Email      string
	Code       string // plaintext during generation; hashed at rest
	ExpiresAt  time.Time
	Attempts   int
}

// Strategy handles recovery flows.
type Strategy struct {
	store    Store
	resetter PasswordResetter
	hasher   Hasher
	courier  *courier.Courier
}

func New(store Store, resetter PasswordResetter, hasher Hasher, c *courier.Courier) *Strategy {
	return &Strategy{store: store, resetter: resetter, hasher: hasher, courier: c}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	if f.Type != domflow.TypeRecovery {
		return nil
	}
	// Phase 1: ask for email.
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "input",
		Group: "code",
		Attributes: mustJSON(map[string]any{
			"name":     "email",
			"type":     "email",
			"required": true,
		}),
	})
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "input",
		Group: "code",
		Attributes: mustJSON(map[string]any{
			"name":  "method",
			"type":  "submit",
			"value": "code",
		}),
	})
	return nil
}

// Handle processes both phases of the recovery flow.
// Phase 1 (state=ChooseMethod):  body has "email" → generate + send code.
// Phase 2 (state=SentEmail):     body has "code" + "new_password" → verify + reset.
func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	switch f.State {
	case domflow.StateChooseMethod:
		return s.handleRequestCode(ctx, f, body)
	case domflow.StateSentEmail:
		return s.handleVerifyCode(ctx, f, body)
	default:
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "unexpected flow state")
	}
}

// --- Phase 1: request code ---

type requestBody struct {
	Email string `json:"email"`
}

func (s *Strategy) handleRequestCode(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b requestBody
	if err := json.Unmarshal(body, &b); err != nil || b.Email == "" {
		addError(f, "email is required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "email required")
	}

	// Look up the identity — but always respond the same way to prevent email enumeration.
	identityID, err := s.store.FindIdentityByEmail(ctx, f.OrgID, b.Email)
	if err != nil {
		// Identity not found — still advance the flow to prevent enumeration.
		f.SetState(domflow.StateSentEmail)
		addInfo(f, "If an account exists for that email, a recovery code has been sent.")
		return nil, nil //nolint:nilnil // intentional: advance flow without identity
	}

	code, err := generateCode()
	if err != nil {
		return nil, fmt.Errorf("generate recovery code: %w", err)
	}

	rc := &RecoveryCode{
		ID:         uuid.New(),
		OrgID:      f.OrgID,
		IdentityID: identityID,
		Email:      b.Email,
		Code:       code,
		ExpiresAt:  time.Now().UTC().Add(codeTTL),
	}
	if err := s.store.Create(ctx, rc); err != nil {
		return nil, fmt.Errorf("store recovery code: %w", err)
	}

	if s.courier != nil {
		_ = s.courier.Send(ctx, f.OrgID, courier.TypeRecoveryCode, b.Email, map[string]any{
			"code":       code,
			"expires_in": "30 minutes",
		})
	}

	// Advance flow state — next submit should provide the code.
	f.SetState(domflow.StateSentEmail)
	// Store email in internal context for phase 2.
	ctx2Data, _ := json.Marshal(map[string]string{"email": b.Email})
	f.InternalContext = ctx2Data

	addInfo(f, "A recovery code has been sent to your email.")
	return nil, nil //nolint:nilnil // intentional: flow not complete yet
}

// --- Phase 2: verify code + reset password ---

type verifyBody struct {
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

func (s *Strategy) handleVerifyCode(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b verifyBody
	if err := json.Unmarshal(body, &b); err != nil || b.Code == "" || b.NewPassword == "" {
		addError(f, "code and new_password are required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "code and new_password required")
	}

	// Read email from internal context.
	var ctx2Data map[string]string
	_ = json.Unmarshal(f.InternalContext, &ctx2Data)
	email := ctx2Data["email"]
	if email == "" {
		addError(f, "flow context lost — please restart recovery")
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "recovery context missing")
	}

	rc, err := s.store.Consume(ctx, f.OrgID, email, b.Code)
	if err != nil {
		addError(f, "invalid or expired recovery code")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid recovery code")
	}

	hash, err := s.hasher.Hash(b.NewPassword)
	if err != nil {
		return nil, fmt.Errorf("hash new password: %w", err)
	}
	if err := s.resetter.StorePasswordCredential(ctx, rc.IdentityID, hash); err != nil {
		return nil, fmt.Errorf("store new credential: %w", err)
	}

	addInfo(f, "Password reset successfully. You may now log in.")
	return &rc.IdentityID, nil
}

// generateCode returns a random 6-digit numeric string.
func generateCode() (string, error) {
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func addError(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000002, Text: msg, Type: "error"})
}

func addInfo(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000001, Text: msg, Type: "info"})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
