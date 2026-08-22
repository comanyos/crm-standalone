// Package smsmfa implements SMS-based MFA as a second factor in the login flow.
//
// Phase 1 (TypeLogin): after password succeeds, this strategy sends a 6-digit OTP
// to the identity's phone number and advances the flow to StateSentSMS.
// Phase 2: user submits the OTP; on success the login completes.
package smsmfa

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/courier/sms"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
)

const (
	ID      = "sms_mfa"
	codeTTL = 5 * time.Minute
)

// OTPRecord holds a pending SMS OTP.
type OTPRecord struct {
	OrgID      uuid.UUID
	IdentityID uuid.UUID
	Phone      string
	Code       string
	ExpiresAt  time.Time
	Attempts   int
}

// Store persists SMS OTP records.
type Store interface {
	Save(ctx context.Context, r *OTPRecord) error
	Consume(ctx context.Context, orgID, identityID uuid.UUID, code string) error
}

// PhoneResolver returns the verified phone number for an identity.
type PhoneResolver interface {
	PhoneForIdentity(ctx context.Context, orgID, identityID uuid.UUID) (string, error)
}

// Strategy implements SMS MFA as a flow strategy.
type Strategy struct {
	store    Store
	resolver PhoneResolver
	sender   sms.Sender
}

func New(store Store, resolver PhoneResolver, sender sms.Sender) *Strategy {
	return &Strategy{store: store, resolver: resolver, sender: sender}
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, _ *domflow.Flow) error { return nil }

type flowCtx struct {
	Phase      int       `json:"phase"`
	IdentityID uuid.UUID `json:"identity_id"`
}

func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var ec flowCtx
	_ = json.Unmarshal(f.InternalContext, &ec)

	if ec.Phase == 2 {
		return s.handleVerify(ctx, f.OrgID, body, ec.IdentityID)
	}
	return s.handleSend(ctx, f, body)
}

type sendBody struct {
	IdentityID uuid.UUID `json:"identity_id"`
}

type verifyBody struct {
	Code string `json:"code"`
}

func (s *Strategy) handleSend(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	var b sendBody
	if err := json.Unmarshal(body, &b); err != nil || b.IdentityID == uuid.Nil {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "identity_id required")
	}

	phone, err := s.resolver.PhoneForIdentity(ctx, f.OrgID, b.IdentityID)
	if err != nil {
		return nil, fmt.Errorf("resolve phone: %w", err)
	}

	code, err := generateOTP()
	if err != nil {
		return nil, fmt.Errorf("generate OTP: %w", err)
	}

	if err := s.store.Save(ctx, &OTPRecord{
		OrgID:      f.OrgID,
		IdentityID: b.IdentityID,
		Phone:      phone,
		Code:       code,
		ExpiresAt:  time.Now().UTC().Add(codeTTL),
	}); err != nil {
		return nil, fmt.Errorf("save OTP: %w", err)
	}

	if s.sender != nil {
		msg := fmt.Sprintf("Your AuthinCore verification code is %s. It expires in 5 minutes.", code)
		_ = s.sender.Send(ctx, phone, msg)
	}

	// Advance flow to phase 2.
	ic, _ := json.Marshal(flowCtx{Phase: 2, IdentityID: b.IdentityID})
	f.InternalContext = ic
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "input",
		Group: "sms_mfa",
		Attributes: mustJSON(map[string]any{
			"name":      "code",
			"type":      "text",
			"required":  true,
			"maxlength": "6",
		}),
	})
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{
		ID:   1000021,
		Text: "An SMS verification code has been sent to your phone.",
		Type: "info",
	})
	return nil, nil //nolint:nilnil
}

func (s *Strategy) handleVerify(ctx context.Context, orgID uuid.UUID, body json.RawMessage, identityID uuid.UUID) (*uuid.UUID, error) {
	var b verifyBody
	if err := json.Unmarshal(body, &b); err != nil || b.Code == "" {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "code required")
	}

	if err := s.store.Consume(ctx, orgID, identityID, b.Code); err != nil {
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid or expired SMS code")
	}
	return &identityID, nil
}

func generateOTP() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := (int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])) & 0x7FFFFFFF
	return fmt.Sprintf("%06d", n%1000000), nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
