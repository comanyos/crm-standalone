package totp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/authincore/authincore/internal/flow/strategy/totp"
	"github.com/google/uuid"
	otplib "github.com/pquerna/otp/totp"
)

// --- fakes ---

type fakeStore struct {
	pendingSecret string
	activeSecret  string
	activated     bool
	backupCodes   []string
	getErr        error
}

func (s *fakeStore) GetSecret(_ context.Context, _ uuid.UUID) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.activeSecret, nil
}
func (s *fakeStore) StorePending(_ context.Context, _ uuid.UUID, secret string) error {
	s.pendingSecret = secret
	return nil
}
func (s *fakeStore) Activate(_ context.Context, _ uuid.UUID) error {
	s.activated = true
	s.activeSecret = s.pendingSecret
	return nil
}
func (s *fakeStore) SaveBackupCodes(_ context.Context, _ uuid.UUID, codes []string) error {
	s.backupCodes = codes
	return nil
}

func newSettingsFlow(t *testing.T) *domflow.Flow {
	t.Helper()
	identityID := uuid.New()
	f := domflow.New(domflow.TypeSettings, uuid.New(), "")
	f.IdentityID = &identityID
	return f
}

func newLoginFlow(t *testing.T, identityID uuid.UUID) *domflow.Flow {
	t.Helper()
	f := domflow.New(domflow.TypeLogin, uuid.New(), "")
	f.IdentityID = &identityID
	return f
}

// --- tests ---

func TestTOTP_Enrollment_Phase1_GeneratesSecret(t *testing.T) {
	store := &fakeStore{}
	strat := totp.New(store, "AuthinCore")

	f := newSettingsFlow(t)
	_, err := strat.Handle(context.Background(), f, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("phase 1 unexpected error: %v", err)
	}
	if store.pendingSecret == "" {
		t.Error("expected pending secret to be stored")
	}
	if len(store.backupCodes) == 0 {
		t.Error("expected backup codes to be saved")
	}
	// InternalContext must encode the phase and secret for phase 2.
	var ic map[string]any
	if err := json.Unmarshal(f.InternalContext, &ic); err != nil {
		t.Fatalf("InternalContext not valid JSON: %v", err)
	}
	if ic["phase"] != float64(2) {
		t.Errorf("expected phase=2 in InternalContext, got %v", ic["phase"])
	}
}

func TestTOTP_Enrollment_Phase2_ValidCode_Activates(t *testing.T) {
	store := &fakeStore{}
	strat := totp.New(store, "AuthinCore")

	// Run phase 1 to get a real secret.
	f := newSettingsFlow(t)
	_, _ = strat.Handle(context.Background(), f, json.RawMessage(`{}`))

	secret := store.pendingSecret
	code, err := otplib.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate TOTP code: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"totp_code": code})
	gotID, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("phase 2 unexpected error: %v", err)
	}
	if gotID == nil {
		t.Error("expected identityID returned after activation")
	}
	if !store.activated {
		t.Error("expected store.Activate to be called")
	}
}

func TestTOTP_Enrollment_Phase2_WrongCode_Error(t *testing.T) {
	store := &fakeStore{}
	strat := totp.New(store, "AuthinCore")

	f := newSettingsFlow(t)
	_, _ = strat.Handle(context.Background(), f, json.RawMessage(`{}`))

	body, _ := json.Marshal(map[string]string{"totp_code": "000000"})
	_, err := strat.Handle(context.Background(), f, body)
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeUnauthenticated {
		t.Errorf("expected CodeUnauthenticated for wrong code, got %v", err)
	}
	if store.activated {
		t.Error("store should not be activated on wrong code")
	}
}

func TestTOTP_Enrollment_Phase2_EmptyCode_Error(t *testing.T) {
	store := &fakeStore{}
	strat := totp.New(store, "AuthinCore")

	f := newSettingsFlow(t)
	_, _ = strat.Handle(context.Background(), f, json.RawMessage(`{}`))

	_, err := strat.Handle(context.Background(), f, json.RawMessage(`{}`))
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument for empty code, got %v", err)
	}
}

func TestTOTP_Verify_ValidCode(t *testing.T) {
	identityID := uuid.New()
	// Generate a real TOTP secret and code.
	key, err := otplib.Generate(otplib.GenerateOpts{Issuer: "test", AccountName: "u@t.com"})
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	code, _ := otplib.GenerateCode(key.Secret(), time.Now())

	store := &fakeStore{activeSecret: key.Secret()}
	strat := totp.New(store, "AuthinCore")

	f := newLoginFlow(t, identityID)
	body, _ := json.Marshal(map[string]string{"totp_code": code})
	gotID, err := strat.Handle(context.Background(), f, body)
	if err != nil {
		t.Fatalf("verify unexpected error: %v", err)
	}
	if gotID == nil || *gotID != identityID {
		t.Errorf("expected identityID %s, got %v", identityID, gotID)
	}
}

func TestTOTP_Verify_WrongCode_Error(t *testing.T) {
	identityID := uuid.New()
	key, _ := otplib.Generate(otplib.GenerateOpts{Issuer: "test", AccountName: "u@t.com"})
	store := &fakeStore{activeSecret: key.Secret()}
	strat := totp.New(store, "AuthinCore")

	f := newLoginFlow(t, identityID)
	body, _ := json.Marshal(map[string]string{"totp_code": "000000"})
	_, err := strat.Handle(context.Background(), f, body)
	var de *domainerr.Error
	if !errors.As(err, &de) || de.Code != domainerr.CodeUnauthenticated {
		t.Errorf("expected CodeUnauthenticated, got %v", err)
	}
}

func TestTOTP_Verify_NoSecret_NotFound(t *testing.T) {
	identityID := uuid.New()
	store := &fakeStore{getErr: domainerr.New(domainerr.CodeNotFound, "no totp enrolled")}
	strat := totp.New(store, "AuthinCore")

	f := newLoginFlow(t, identityID)
	body, _ := json.Marshal(map[string]string{"totp_code": "123456"})
	_, err := strat.Handle(context.Background(), f, body)
	if err == nil {
		t.Error("expected error when TOTP not enrolled")
	}
}

func TestTOTP_BackupCodes_Count(t *testing.T) {
	store := &fakeStore{}
	strat := totp.New(store, "AuthinCore")
	f := newSettingsFlow(t)
	_, _ = strat.Handle(context.Background(), f, json.RawMessage(`{}`))
	if len(store.backupCodes) != 8 {
		t.Errorf("expected 8 backup codes, got %d", len(store.backupCodes))
	}
	for i, c := range store.backupCodes {
		if len(c) != 11 { // XXXXX-XXXXX = 11 chars
			t.Errorf("backup code %d has unexpected format: %q", i, c)
		}
	}
}
