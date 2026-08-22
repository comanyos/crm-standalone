package identity_test

import (
	"encoding/json"
	"testing"

	"github.com/authincore/authincore/internal/domain/identity"
	"github.com/google/uuid"
)

func TestNew_DefaultsToActive(t *testing.T) {
	i := identity.New(uuid.New(), "user-v1", json.RawMessage(`{"email":"a@b.com"}`))
	if i.State != identity.StateActive {
		t.Errorf("expected StateActive, got %v", i.State)
	}
	if i.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}
}

func TestDeactivateReactivate(t *testing.T) {
	i := identity.New(uuid.New(), "user-v1", nil)
	i.Deactivate()
	if i.State != identity.StateInactive {
		t.Errorf("expected StateInactive after Deactivate")
	}
	i.Reactivate()
	if i.State != identity.StateActive {
		t.Errorf("expected StateActive after Reactivate")
	}
}

func TestAAL_PasswordOnly(t *testing.T) {
	i := identity.New(uuid.New(), "schema", nil)
	i.SetCredential(&identity.Credential{Type: identity.CredentialPassword})
	if i.AvailableAAL != identity.AAL1 {
		t.Errorf("expected AAL1 with password, got %v", i.AvailableAAL)
	}
}

func TestAAL_PasswordAndTOTP(t *testing.T) {
	i := identity.New(uuid.New(), "schema", nil)
	i.SetCredential(&identity.Credential{Type: identity.CredentialPassword})
	i.SetCredential(&identity.Credential{Type: identity.CredentialTOTP})
	if i.AvailableAAL != identity.AAL2 {
		t.Errorf("expected AAL2 with password+TOTP, got %v", i.AvailableAAL)
	}
}

func TestAAL_RemovesTOTP(t *testing.T) {
	i := identity.New(uuid.New(), "schema", nil)
	i.SetCredential(&identity.Credential{Type: identity.CredentialPassword})
	i.SetCredential(&identity.Credential{Type: identity.CredentialTOTP})
	i.RemoveCredential(identity.CredentialTOTP)
	if i.AvailableAAL != identity.AAL1 {
		t.Errorf("expected AAL1 after removing TOTP, got %v", i.AvailableAAL)
	}
}

func TestAAL_NoCredentials(t *testing.T) {
	i := identity.New(uuid.New(), "schema", nil)
	if i.AvailableAAL != identity.AAL0 {
		t.Errorf("expected AAL0 with no credentials, got %v", i.AvailableAAL)
	}
}
