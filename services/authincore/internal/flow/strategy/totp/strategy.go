// Package totp implements TOTP enrollment (settings flow) and second-factor verification.
//
// Enrollment is a two-phase settings flow:
//  1. Empty body → generate secret, return QR URI + backup codes, advance to phase 2.
//  2. Body with totp_code → validate first code → activate secret.
//
// Verification (login MFA) uses Handle directly when IdentityID is pre-set and
// InternalContext has no pending phase.
package totp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
)

const (
	ID           = "totp"
	backupCodeN  = 8
	backupRawLen = 5
)

// SecretStore reads and writes TOTP secrets and backup codes.
type SecretStore interface {
	// GetSecret returns the active TOTP secret for an identity (verification path).
	GetSecret(ctx context.Context, identityID uuid.UUID) (string, error)
	// StorePending persists an unactivated secret (enrollment phase 1).
	StorePending(ctx context.Context, identityID uuid.UUID, secret string) error
	// Activate promotes the pending secret to active (enrollment phase 2).
	Activate(ctx context.Context, identityID uuid.UUID) error
	// SaveBackupCodes stores hashed backup codes for an identity.
	SaveBackupCodes(ctx context.Context, identityID uuid.UUID, codes []string) error
}

// UsedCodeTracker prevents TOTP replay by recording each (identityID, period) pair atomically.
// Satisfied by *cache.Client via MarkTOTPUsed.
type UsedCodeTracker interface {
	MarkTOTPUsed(ctx context.Context, identityID fmt.Stringer, period int64) (bool, error)
}

// Strategy handles both TOTP enrollment (settings) and TOTP verification (login MFA).
type Strategy struct {
	store   SecretStore
	issuer  string
	tracker UsedCodeTracker // optional; nil disables replay protection
}

func New(store SecretStore, issuer string) *Strategy {
	return &Strategy{store: store, issuer: issuer}
}

// WithTracker enables TOTP replay prevention using the given UsedCodeTracker.
func (s *Strategy) WithTracker(t UsedCodeTracker) *Strategy {
	s.tracker = t
	return s
}

func (s *Strategy) ID() string { return ID }

func (s *Strategy) PopulateFlow(_ context.Context, f *domflow.Flow) error {
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "input",
		Group: "totp",
		Attributes: mustJSON(map[string]any{
			"name":        "totp_code",
			"type":        "text",
			"required":    true,
			"maxlength":   "6",
			"pattern":     `\d{6}`,
			"placeholder": "6-digit code",
		}),
	})
	return nil
}

type submitBody struct {
	TOTPCode string `json:"totp_code"`
}

type enrollCtx struct {
	Phase  int    `json:"phase"`
	Secret string `json:"secret"`
}

func (s *Strategy) Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (*uuid.UUID, error) {
	if f.IdentityID == nil {
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "identity not resolved")
	}

	// Decode optional enrollment context.
	var ec enrollCtx
	_ = json.Unmarshal(f.InternalContext, &ec)

	var b submitBody
	_ = json.Unmarshal(body, &b)

	switch {
	case f.Type == domflow.TypeSettings && ec.Phase == 0:
		// Enrollment phase 1: generate and return secret.
		return s.handleEnrollGenerate(ctx, f)
	case f.Type == domflow.TypeSettings && ec.Phase == 2:
		// Enrollment phase 2: verify first code and activate.
		return s.handleEnrollActivate(ctx, f, b.TOTPCode, ec.Secret)
	default:
		// MFA verification during login.
		return s.handleVerify(ctx, f, b.TOTPCode)
	}
}

func (s *Strategy) handleEnrollGenerate(ctx context.Context, f *domflow.Flow) (*uuid.UUID, error) {
	accountName := f.IdentityID.String()
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      s.issuer,
		AccountName: accountName,
		SecretSize:  20,
	})
	if err != nil {
		return nil, fmt.Errorf("totp generate key: %w", err)
	}

	if err := s.store.StorePending(ctx, *f.IdentityID, key.Secret()); err != nil {
		return nil, fmt.Errorf("totp store pending: %w", err)
	}

	backupCodes, err := generateBackupCodes(backupCodeN)
	if err != nil {
		return nil, fmt.Errorf("totp backup codes: %w", err)
	}
	if err := s.store.SaveBackupCodes(ctx, *f.IdentityID, backupCodes); err != nil {
		return nil, fmt.Errorf("totp save backup codes: %w", err)
	}

	// Advance to phase 2: caller must resubmit with a valid code.
	ec := enrollCtx{Phase: 2, Secret: key.Secret()}
	// #nosec G117 -- this marshals into server-only flow InternalContext; the public flow serializer never exposes it.
	ctxData, _ := json.Marshal(ec)
	f.InternalContext = ctxData

	addInfo(f, "Scan the QR code with your authenticator app, then submit your first 6-digit code.")
	f.UI.Nodes = append(f.UI.Nodes, domflow.UINode{
		Type:  "img",
		Group: "totp",
		Attributes: mustJSON(map[string]any{
			"id":           "totp_qr",
			"src":          key.URL(),
			"backup_codes": backupCodes,
		}),
	})
	return nil, nil //nolint:nilnil
}

func (s *Strategy) handleEnrollActivate(ctx context.Context, f *domflow.Flow, code, secret string) (*uuid.UUID, error) {
	if code == "" {
		addError(f, "Enter the 6-digit code from your authenticator app.")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "totp_code required")
	}
	if !totp.Validate(code, secret) {
		addError(f, "Invalid TOTP code — please try again.")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid totp code")
	}
	if err := s.store.Activate(ctx, *f.IdentityID); err != nil {
		return nil, fmt.Errorf("totp activate: %w", err)
	}
	addInfo(f, "Two-factor authentication enabled successfully.")
	return f.IdentityID, nil
}

func (s *Strategy) handleVerify(ctx context.Context, f *domflow.Flow, code string) (*uuid.UUID, error) {
	if code == "" {
		addError(f, "TOTP code required")
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "totp_code required")
	}
	secret, err := s.store.GetSecret(ctx, *f.IdentityID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	valid, _ := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period: 30,
		Skew:   1,
		Digits: 6,
	})
	if !valid {
		addError(f, "Invalid TOTP code.")
		return nil, domainerr.New(domainerr.CodeUnauthenticated, "invalid totp code")
	}

	// Replay prevention: each (identity, TOTP period) pair may only be used once.
	// We must track the period of the *accepted* code, not the server's current
	// period. With Skew=1, a device ahead by one window validates period N+1 while
	// the server is still in period N — using the server period leaves a replay gap.
	if s.tracker != nil {
		matchedPeriod := matchedTOTPPeriod(code, secret, now)
		first, err := s.tracker.MarkTOTPUsed(ctx, f.IdentityID, matchedPeriod)
		if err != nil {
			return nil, fmt.Errorf("totp replay check: %w", err)
		}
		if !first {
			addError(f, "TOTP code already used. Please wait for the next code.")
			return nil, domainerr.New(domainerr.CodeUnauthenticated, "totp code already used")
		}
	}

	return f.IdentityID, nil
}

func generateBackupCodes(n int) ([]string, error) {
	codes := make([]string, n)
	buf := make([]byte, backupRawLen)
	for i := range codes {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		// Encode to uppercase hex: 10 chars, split as XXXXX-XXXXX.
		s := strings.ToUpper(fmt.Sprintf("%x", buf))
		codes[i] = s[:5] + "-" + s[5:]
	}
	return codes, nil
}

func addError(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 4000010, Text: msg, Type: "error"})
}

func addInfo(f *domflow.Flow, msg string) {
	f.UI.Messages = append(f.UI.Messages, domflow.UIMessage{ID: 1000005, Text: msg, Type: "info"})
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// matchedTOTPPeriod returns the TOTP period number that the accepted code
// belongs to. ValidateCustom with Skew=1 accepts period-1, period, period+1;
// we probe each window with Skew=0 to find the exact match. Falls back to the
// current server period if none of the three windows match (should not happen).
func matchedTOTPPeriod(code, secret string, now time.Time) int64 {
	const periodSec = int64(30)
	opts := totp.ValidateOpts{Period: 30, Skew: 0, Digits: 6}
	for delta := int64(-1); delta <= 1; delta++ {
		t := now.Add(time.Duration(delta*periodSec) * time.Second)
		if ok, _ := totp.ValidateCustom(code, secret, t, opts); ok {
			return t.Unix() / periodSec
		}
	}
	return now.Unix() / periodSec
}
