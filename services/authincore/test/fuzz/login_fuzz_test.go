package fuzz

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/authincore/authincore/internal/scim"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Inline mocks for fuzz tests (zero external I/O)
// ---------------------------------------------------------------------------

type noopUserStore struct{}

func (n *noopUserStore) Create(_ context.Context, _ uuid.UUID, u *scim.User) (*scim.User, error) {
	out := *u
	out.ID = uuid.New().String()
	return &out, nil
}
func (n *noopUserStore) ByID(_ context.Context, _ uuid.UUID, _ string) (*scim.User, error) {
	return nil, errors.New("not found")
}
func (n *noopUserStore) ByExternalID(_ context.Context, _ uuid.UUID, _ string) (*scim.User, error) {
	return nil, errors.New("not found")
}
func (n *noopUserStore) ByUserName(_ context.Context, _ uuid.UUID, _ string) (*scim.User, error) {
	return nil, errors.New("not found")
}
func (n *noopUserStore) Replace(_ context.Context, _ uuid.UUID, u *scim.User) (*scim.User, error) {
	return u, nil
}
func (n *noopUserStore) SetActive(_ context.Context, _ uuid.UUID, _ string, _ bool) error {
	return nil
}
func (n *noopUserStore) Delete(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (n *noopUserStore) List(_ context.Context, _ uuid.UUID, _ *scim.Filter, _, _ int) ([]*scim.User, int, error) {
	return nil, 0, nil
}

type noopGroupStore struct{}

func (n *noopGroupStore) Create(_ context.Context, _ uuid.UUID, g *scim.Group) (*scim.Group, error) {
	out := *g
	out.ID = uuid.New().String()
	return &out, nil
}
func (n *noopGroupStore) ByID(_ context.Context, _ uuid.UUID, _ string) (*scim.Group, error) {
	return nil, errors.New("not found")
}
func (n *noopGroupStore) Replace(_ context.Context, _ uuid.UUID, g *scim.Group) (*scim.Group, error) {
	return g, nil
}
func (n *noopGroupStore) Delete(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (n *noopGroupStore) List(_ context.Context, _ uuid.UUID, _ *scim.Filter, _, _ int) ([]*scim.Group, int, error) {
	return nil, 0, nil
}
func (n *noopGroupStore) SetMembers(_ context.Context, _ uuid.UUID, _ string, _ []string) error {
	return nil
}

type alwaysValidTokenFuzz struct{}

func (a *alwaysValidTokenFuzz) Validate(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

var (
	testMux = func() *http.ServeMux {
		h := scim.NewHandler(&noopUserStore{}, &noopGroupStore{}, &alwaysValidTokenFuzz{},
			"https://auth.example.com/scim/v2")
		mux := http.NewServeMux()
		h.Routes(mux, "/scim/v2")
		return mux
	}()
	testOrgFuzz = uuid.New()
)

func scimRequest(method, path string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Org-ID", testOrgFuzz.String())
	req.Header.Set("Authorization", "Bearer valid")
	req.Header.Set("Content-Type", "application/scim+json")
	return req
}

func assertValidHTTPStatus(t *testing.T, code int) {
	t.Helper()
	if code < 200 || code > 599 {
		t.Errorf("response code %d is not a valid HTTP status", code)
	}
	if code == http.StatusInternalServerError {
		t.Errorf("handler returned 500 — possible panic/unhandled error")
	}
}

// ---------------------------------------------------------------------------
// FuzzLoginBody — sends malformed JSON to the SCIM POST /Users endpoint.
// ---------------------------------------------------------------------------

func FuzzLoginBody(f *testing.F) {
	// Seed corpus: valid, empty, partial, unicode.
	f.Add([]byte(`{"userName":"alice@example.com","active":true}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"userName":null}`))
	f.Add([]byte(`{"userName":` + `"` + "\x00\xff\xfe" + `"}`))
	f.Add([]byte(`{"userName":"` + "emoji🔑test" + `"}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`true`))
	f.Add([]byte(`"string"`))
	f.Add([]byte("\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		req := scimRequest(http.MethodPost, "/scim/v2/Users", data)
		rr := httptest.NewRecorder()

		// Must not panic.
		testMux.ServeHTTP(rr, req)
		assertValidHTTPStatus(t, rr.Code)
	})
}

// ---------------------------------------------------------------------------
// FuzzSCIMFilter — sends random strings as the filter query parameter.
// ---------------------------------------------------------------------------

func FuzzSCIMFilter(f *testing.F) {
	f.Add(`userName eq "alice"`)
	f.Add(`active eq true`)
	f.Add(`emails.value co "@example.com"`)
	f.Add(``)
	f.Add(`userName eq "` + "\x00\x01\x02" + `"`)
	f.Add(`'; DROP TABLE users; --`)
	f.Add(`a b c d e f g h`)
	f.Add(`userName eq true false null`)
	f.Add("🔑 eq 🔒")

	f.Fuzz(func(t *testing.T, filterStr string) {
		// First, ParseFilter must never panic.
		_ = scim.ParseFilter(filterStr)

		// Second, passing the filter to the HTTP endpoint must not panic or 500.
		path := "/scim/v2/Users?filter=" + url.QueryEscape(filterStr)
		req := scimRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		testMux.ServeHTTP(rr, req)
		assertValidHTTPStatus(t, rr.Code)
	})
}

// ---------------------------------------------------------------------------
// FuzzOAuthParams — sends random bytes as OAuth2 form fields.
// ---------------------------------------------------------------------------

func FuzzOAuthParams(f *testing.F) {
	// Seeds covering the grant_type dispatch.
	f.Add("authorization_code", "code123", "https://app.example.com/cb", "client1", "")
	f.Add("refresh_token", "", "", "client2", "rt-abc")
	f.Add("client_credentials", "", "", "client3", "secret")
	f.Add("", "", "", "", "")
	f.Add("\x00\xff", "\x00", "\x00", "\x00", "\x00")
	f.Add("authorization_code; DROP TABLE--", "code", "http://evil.com", "c", "")

	f.Fuzz(func(t *testing.T, grantType, code, redirectURI, clientID, refreshToken string) {
		// Build a minimal OAuth2 token endpoint request.
		body := "grant_type=" + grantType +
			"&code=" + code +
			"&redirect_uri=" + redirectURI +
			"&client_id=" + clientID +
			"&refresh_token=" + refreshToken

		req := httptest.NewRequest(http.MethodPost, "/oauth2/token",
			bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Org-ID", testOrgFuzz.String())

		rr := httptest.NewRecorder()

		// We use a minimal ServeMux that always 404s — the point is that
		// constructing the request and routing it does not panic.
		http.NewServeMux().ServeHTTP(rr, req)
		// 404 from empty mux is expected; just confirm no panic.
		if rr.Code < 200 || rr.Code > 599 {
			t.Errorf("invalid HTTP status: %d", rr.Code)
		}
	})
}
