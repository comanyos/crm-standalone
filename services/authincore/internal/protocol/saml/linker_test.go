package saml

import (
	"testing"
)

func TestBuildTraitsFromSAML(t *testing.T) {
	tests := []struct {
		name      string
		attrs     map[string]string
		nameID    string
		wantKeys  []string
		wantEmail string
	}{
		{
			name: "with email attribute",
			attrs: map[string]string{
				"email":     "alice@example.com",
				"firstName": "Alice",
				"lastName":  "Smith",
			},
			nameID:    "alice@example.com",
			wantKeys:  []string{"email", "firstName", "lastName"},
			wantEmail: "alice@example.com",
		},
		{
			name:      "without email — uses nameID",
			attrs:     map[string]string{"displayName": "Bob"},
			nameID:    "bob@idp.example.com",
			wantKeys:  []string{"displayName", "email"},
			wantEmail: "bob@idp.example.com",
		},
		{
			name:      "empty attrs — uses nameID as email",
			attrs:     map[string]string{},
			nameID:    "user123",
			wantKeys:  []string{"email"},
			wantEmail: "user123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			traits := buildTraitsFromSAML(tt.attrs, tt.nameID)

			// Check all expected keys are present.
			for _, key := range tt.wantKeys {
				if _, ok := traits[key]; !ok {
					t.Errorf("expected key %q in traits, got %v", key, traits)
				}
			}

			// Check email value.
			if traits["email"] != tt.wantEmail {
				t.Errorf("email = %q, want %q", traits["email"], tt.wantEmail)
			}
		})
	}
}

func TestBuildTraitsFromSAML_PreservesAllAttributes(t *testing.T) {
	attrs := map[string]string{
		"email":      "user@example.com",
		"givenName":  "User",
		"sn":         "Example",
		"department": "Engineering",
		"employeeId": "12345",
	}

	traits := buildTraitsFromSAML(attrs, "user@example.com")

	if len(traits) != len(attrs) {
		t.Errorf("traits length = %d, want %d", len(traits), len(attrs))
	}

	for k, v := range attrs {
		if traits[k] != v {
			t.Errorf("traits[%q] = %q, want %q", k, traits[k], v)
		}
	}
}
