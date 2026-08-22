package tenancy

import (
	"errors"
	"testing"
)

// The invariant the whole tenancy model rests on: whatever CanonicalDomain
// stores must be exactly what CanonicalHost derives from a request, or the
// exact-equality lookup in ByDomain can never match.
func TestStoredDomainMatchesResolvedHost(t *testing.T) {
	tests := []struct {
		stored  string
		request string
	}{
		{"localhost:8080", "localhost:8080"},
		{"localhost", "localhost:8080"},
		{"ACME.test", "acme.test"},
		{"acme.test.", "acme.test:443"},
		{"[::1]", "[::1]:8080"},
		{"127.0.0.1:5000", "127.0.0.1"},
	}

	for _, tc := range tests {
		t.Run(tc.stored+" vs "+tc.request, func(t *testing.T) {
			stored, err := CanonicalDomain(tc.stored)
			if err != nil {
				t.Fatalf("CanonicalDomain(%q): %v", tc.stored, err)
			}
			resolved := CanonicalHost(tc.request)
			if stored != resolved {
				t.Errorf("stored %q but a request to %q resolves as %q — this tenant is unreachable",
					stored, tc.request, resolved)
			}
		})
	}
}

func TestCanonicalDomainAccepts(t *testing.T) {
	tests := []struct{ in, want string }{
		{"localhost", "localhost"},
		{"localhost:8080", "localhost"},
		{"  localhost:8080  ", "localhost"},
		{"AuthinCore:8080", "authincore"},
		{"acme.test", "acme.test"},
		{"acme.test.", "acme.test"},
		{"sub.domain.acme.test:443", "sub.domain.acme.test"},
		{"new-tenant.local:8080", "new-tenant.local"},
		{"127.0.0.1", "127.0.0.1"},
		{"127.0.0.1:5432", "127.0.0.1"},
		{"[::1]", "::1"},
		{"[::1]:8080", "::1"},
		// IPv6 goes through net.IP so equivalent spellings converge.
		{"[2001:0DB8::0001]", "2001:db8::1"},
		{"[2001:db8::1]:8080", "2001:db8::1"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := CanonicalDomain(tc.in)
			if err != nil {
				t.Fatalf("CanonicalDomain(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("CanonicalDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A URL stored as a domain becomes a tenant no request can resolve. Rejecting
// at write time turns a silent, permanent misconfiguration into an error the
// caller sees immediately.
func TestCanonicalDomainRejects(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"whitespace only", "   "},
		{"scheme", "https://acme.test"},
		{"scheme and port", "http://localhost:8080"},
		{"path", "acme.test/login"},
		{"query", "acme.test?tenant=1"},
		{"fragment", "acme.test#frag"},
		{"credentials", "user:pass@acme.test"},
		{"embedded space", "acme test"},
		{"empty label", "acme..test"},
		{"leading dash", "-acme.test"},
		{"trailing dash", "acme-.test"},
		{"underscore", "acme_test.local"},
		{"port only", ":8080"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanonicalDomain(tc.in)
			if err == nil {
				t.Fatalf("CanonicalDomain(%q) = %q, want an error", tc.in, got)
			}
			if !errors.Is(err, ErrInvalidDomain) {
				t.Errorf("error %v does not wrap ErrInvalidDomain", err)
			}
		})
	}
}

// Canonicalisation must be idempotent: re-running the migration, or updating an
// org with its own stored domain, must not change the value.
func TestCanonicalDomainIsIdempotent(t *testing.T) {
	for _, in := range []string{"localhost:8080", "ACME.test.", "[2001:0DB8::1]:443", "127.0.0.1:99"} {
		once, err := CanonicalDomain(in)
		if err != nil {
			t.Fatalf("CanonicalDomain(%q): %v", in, err)
		}
		twice, err := CanonicalDomain(once)
		if err != nil {
			t.Fatalf("CanonicalDomain(%q): %v", once, err)
		}
		if once != twice {
			t.Errorf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}

func TestCanonicalHostStripsPortAndCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"localhost:8080", "localhost"},
		{"LOCALHOST", "localhost"},
		{"acme.test.", "acme.test"},
		{"[::1]:8080", "::1"},
		{"[::1]", "::1"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := CanonicalHost(tc.in); got != tc.want {
			t.Errorf("CanonicalHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
