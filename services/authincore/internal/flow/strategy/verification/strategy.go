// Package verification implements the email verification flow strategy.
// After registration, a 6-digit code is sent to the user's email.
// Submitting the correct code marks the identity's email as verified.
package verification

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
	ID       = "code"
	codeTTL  = 30 * time.Minute
	maxTries = 5
)

// Store persists verification codes and marks emails as verified.
type Store interface {
	Create(ctx context.Context, code *VerificationCode) error
	Consume(ctx context.Context, orgID uuid.UUID, email, code string) (*VerificationCode, error)
	MarkVerified(ctx context.Context, identityID uuid.UUID, email string) error
}

// IdentityEmailResolver finds the identity ID for a given email in an org.
type IdentityEmailResolver interface {
	FindIdentityByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error)
}

// VerificationCode is the persisted record.
type VerificationCode struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	IdentityID uuid.UUID
	Email      string
	Code       string
	ExpiresAt  time.Time
	Attempts   int
}

// Strategy handles the verification flow.
type Strategy struct {
	store    Store
	resolver IdentityEmailResolver
	courier  *courier.Courier
}

func New(store Store, resolver IdentityEmailResolver, c *courier.Courier) *Strategy {
	return &Strategy{store: store, resolver: resolver, courier: c}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	if f.Type != domflow.TypeVerification {
		return nil
	}
	f.UI.Nodes = append(f.UI.Nodes,
		domflow.UINode{
			Type:  "input",
			Group: "code",
			Attributes: mustJSON(map[string]any{
				"name":     "email",
				"type":     "email",
				"required": true,
			}),
		},
		domflow.UINode{
			Type:  "input",
			Group: "code",
			Attributes: mustJSON(map[string]any{
				"name":  "method",
				"type":  "submit",
				"value": "code",
			}),
		},
	)
	return nil
}

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

type requestBody struct {
	Email string `json:"email"`
}

func (s *Strategy) handleRequestCode(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b requestBody
	if err := json.Unmarshal(body, &b); err != nil || b.Email == "" {
		addError(f, "email is required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "email required")
	}

	identityID, err := s.resolver.FindIdentityByEmail(ctx, f.OrgID, b.Email)
	if err != nil {
		// Same response regardless — prevent enumeration.
		f.SetState(domflow.StateSentEmail)
		addInfo(f, "If that email is registered, a verification code has been sent.")
		return nil, nil //nolint:nilnil
	}

	code, err := generateCode()
	if err != nil {
		return nil, fmt.Errorf("generate verification code: %w", err)
	}

	vc := &VerificationCode{
		ID:         uuid.New(),
		OrgID:      f.OrgID,
		IdentityID: identityID,
		Email:      b.Email,
		Code:       code,
		ExpiresAt:  time.Now().UTC().Add(codeTTL),
	}
	if err := s.store.Create(ctx, vc); err != nil {
		return nil, fmt.Errorf("store verification code: %w", err)
	}

	if s.courier != nil {
		_ = s.courier.Send(ctx, f.OrgID, courier.TypeVerificationCode, b.Email, map[string]any{
			"code":       code,
			"expires_in": "30 minutes",
		})
	}

	f.SetState(domflow.StateSentEmail)
	ctxData, _ := json.Marshal(map[string]string{"email": b.Email})
	f.InternalContext = ctxData
	addInfo(f, "A verification code has been sent to your email.")
	return nil, nil //nolint:nilnil
}

type verifyBody struct {
	Code string `json:"code"`
}

func (s *Strategy) handleVerifyCode(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b verifyBody
	if err := json.Unmarshal(body, &b); err != nil || b.Code == "" {
		addError(f, "verification code is required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "code required")
	}

	var ctxData map[string]string
	_ = json.Unmarshal(f.InternalContext, &ctxData)
	email := ctxData["email"]
	if email == "" {
		addError(f, "flow context lost — please restart verification")
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "context missing")
	}

	vc, err := s.store.Consume(ctx, f.OrgID, email, b.Code)
	if err != nil {
		addError(f, "invalid or expired verification code")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid code")
	}

	if err := s.store.MarkVerified(ctx, vc.IdentityID, email); err != nil {
		return nil, fmt.Errorf("mark verified: %w", err)
	}

	addInfo(f, "Email verified successfully.")
	return &vc.IdentityID, nil
}

func generateCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func addError(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000003, Text: msg, Type: "error"})
}

func addInfo(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000002, Text: msg, Type: "info"})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
