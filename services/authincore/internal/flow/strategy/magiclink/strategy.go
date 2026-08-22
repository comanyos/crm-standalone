// Package magiclink implements passwordless email-code (magic link) login.
//
// Phase 1: user submits email → 6-digit code generated, queued via courier → flow advances.
// Phase 2: user submits code → validated → identity resolved → login complete.
//
// Enumeration prevention: unknown emails return the same "code sent" message.
package magiclink

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/courier"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

const (
	ID      = "code"
	codeTTL = 5 * time.Minute
)

// LoginCode is the persisted code record.
type LoginCode struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	IdentityID uuid.UUID
	Email      string
	Code       string
	ExpiresAt  time.Time
	Attempts   int
}

// Store persists and consumes login codes.
type Store interface {
	Create(ctx context.Context, lc *LoginCode) error
	// Consume atomically deletes + returns the code if valid; increments attempts otherwise.
	Consume(ctx context.Context, orgID uuid.UUID, email, code string) (*LoginCode, error)
}

// IdentityResolver finds or creates an identity by email.
type IdentityResolver interface {
	FindOrCreateByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error)
}

// Strategy implements the magic-link (email code) login strategy.
type Strategy struct {
	store    Store
	resolver IdentityResolver
	courier  *courier.Courier
}

func New(store Store, resolver IdentityResolver, c *courier.Courier) *Strategy {
	return &Strategy{store: store, resolver: resolver, courier: c}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	if f.Type != domflow.TypeLogin {
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
				"label": "Sign in with email code",
			}),
		},
	)
	return nil
}

func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var ec flowCtx
	_ = json.Unmarshal(f.InternalContext, &ec)

	if ec.Phase == 2 {
		return s.handleVerify(ctx, f, body, ec.Email)
	}
	return s.handleRequest(ctx, f, body)
}

type requestBody struct {
	Email string `json:"email"`
}

type verifyBody struct {
	Code string `json:"code"`
}

type flowCtx struct {
	Phase int    `json:"phase"`
	Email string `json:"email"`
}

func (s *Strategy) handleRequest(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b requestBody
	if err := json.Unmarshal(body, &b); err != nil || b.Email == "" {
		f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000030, Text: "Email address is required.", Type: "error"})
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "email required")
	}

	identityID, err := s.resolver.FindOrCreateByEmail(ctx, f.OrgID, b.Email)
	if err != nil {
		// Advance flow regardless to prevent enumeration.
		s.advanceToVerify(f, b.Email)
		f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000020, Text: "If that email is registered, a login code has been sent.", Type: "info"})
		return nil, nil //nolint:nilnil
	}

	code, err := generateSixDigit()
	if err != nil {
		return nil, fmt.Errorf("generate login code: %w", err)
	}

	lc := &LoginCode{
		ID:         uuid.New(),
		OrgID:      f.OrgID,
		IdentityID: identityID,
		Email:      b.Email,
		Code:       code,
		ExpiresAt:  time.Now().UTC().Add(codeTTL),
	}
	if err := s.store.Create(ctx, lc); err != nil {
		return nil, fmt.Errorf("store login code: %w", err)
	}

	if s.courier != nil {
		_ = s.courier.Send(ctx, f.OrgID, courier.TypeLoginCode, b.Email, map[string]any{
			"code":       code,
			"expires_in": "5 minutes",
		})
	}

	s.advanceToVerify(f, b.Email)
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000020, Text: "A login code has been sent to your email. It expires in 5 minutes.", Type: "info"})
	return nil, nil //nolint:nilnil
}

func (s *Strategy) handleVerify(ctx context.Context, f *domflow.Flow, body json.RawMessage, email string) (*uuid.UUID, error) {
	var b verifyBody
	if err := json.Unmarshal(body, &b); err != nil || b.Code == "" {
		f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000030, Text: "Login code is required.", Type: "error"})
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "code required")
	}

	lc, err := s.store.Consume(ctx, f.OrgID, email, b.Code)
	if err != nil {
		f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000031, Text: "Invalid or expired login code.", Type: "error"})
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid code")
	}

	return &lc.IdentityID, nil
}

func (s *Strategy) advanceToVerify(f *domflow.Flow, email string) {
	f.SetState(domflow.StateSentEmail)
	ctx, _ := json.Marshal(flowCtx{Phase: 2, Email: email})
	f.InternalContext = ctx
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "input",
		Group: "code",
		Attributes: mustJSON(map[string]any{
			"name":        "code",
			"type":        "text",
			"required":    true,
			"placeholder": "6-digit code",
			"maxlength":   "6",
		}),
	})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
