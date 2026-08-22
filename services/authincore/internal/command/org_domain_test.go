package command

import (
	"context"
	"strings"
	"testing"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/google/uuid"
)

// CreateOrg must reject a malformed domain before it reaches the database.
//
// Commands is constructed with a nil pool here on purpose: if validation ever
// moves below the first query, these cases stop returning an error and panic
// instead, which is a much louder signal than a silently stored bad value.
func TestCreateOrgRejectsUncanonicalisableDomains(t *testing.T) {
	c := &Commands{}

	tests := []struct{ name, domain string }{
		{"empty", ""},
		{"scheme", "https://acme.test"},
		{"scheme with port", "http://localhost:8080"},
		{"path", "acme.test/login"},
		{"query", "acme.test?x=1"},
		{"credentials", "user:pass@acme.test"},
		{"embedded space", "acme test"},
		{"empty label", "acme..test"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CreateOrg(context.Background(), CreateOrgCommand{
				InstanceID:    uuid.New(),
				Name:          "Acme",
				PrimaryDomain: tc.domain,
			})
			if err == nil {
				t.Fatalf("CreateOrg accepted %q", tc.domain)
			}
			if !domainerr.IsInvalidArgument(err) {
				t.Errorf("error is not InvalidArgument: %v", err)
			}
		})
	}
}

// The value CreateOrg would persist has to be the one tenant resolution derives
// from a request Host, or the organization is created and then unreachable.
// This pins the contract between the two without needing a database.
func TestCreateOrgWouldStoreAResolvableDomain(t *testing.T) {
	tests := []struct{ input, requestHost string }{
		{"localhost:8080", "localhost:8080"},
		{"ACME.test", "acme.test"},
		{"new-tenant.local:8080", "new-tenant.local"},
		{"[2001:db8::1]:8080", "[2001:db8::1]"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			stored, err := tenancy.CanonicalDomain(tc.input)
			if err != nil {
				t.Fatalf("CanonicalDomain(%q): %v", tc.input, err)
			}
			if resolved := tenancy.CanonicalHost(tc.requestHost); stored != resolved {
				t.Errorf("CreateOrg would store %q but requests to %q resolve as %q",
					stored, tc.requestHost, resolved)
			}
			if strings.ContainsAny(stored, ":/?#@") && !strings.Contains(stored, "::") {
				t.Errorf("stored domain %q still carries URL punctuation", stored)
			}
		})
	}
}
