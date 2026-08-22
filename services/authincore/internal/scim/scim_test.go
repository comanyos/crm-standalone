package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockUserStore struct {
	users       map[string]*User // keyed by ID
	createFn    func(ctx context.Context, orgID uuid.UUID, u *User) (*User, error)
	byIDFn      func(ctx context.Context, orgID uuid.UUID, id string) (*User, error)
	replaceFn   func(ctx context.Context, orgID uuid.UUID, u *User) (*User, error)
	setActiveFn func(ctx context.Context, orgID uuid.UUID, id string, active bool) error
	deleteFn    func(ctx context.Context, orgID uuid.UUID, id string) error
	listFn      func(ctx context.Context, orgID uuid.UUID, filter *Filter, start, count int) ([]*User, int, error)
}

func newMockUserStore() *mockUserStore {
	return &mockUserStore{users: make(map[string]*User)}
}

func (m *mockUserStore) Create(ctx context.Context, orgID uuid.UUID, u *User) (*User, error) {
	if m.createFn != nil {
		return m.createFn(ctx, orgID, u)
	}
	out := *u
	out.ID = uuid.New().String()
	m.users[out.ID] = &out
	return &out, nil
}

func (m *mockUserStore) ByID(ctx context.Context, orgID uuid.UUID, id string) (*User, error) {
	if m.byIDFn != nil {
		return m.byIDFn(ctx, orgID, id)
	}
	u, ok := m.users[id]
	if !ok {
		return nil, errors.New("not found")
	}
	out := *u
	return &out, nil
}

func (m *mockUserStore) ByExternalID(_ context.Context, _ uuid.UUID, externalID string) (*User, error) {
	for _, u := range m.users {
		if u.ExternalID == externalID {
			out := *u
			return &out, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockUserStore) ByUserName(_ context.Context, _ uuid.UUID, userName string) (*User, error) {
	for _, u := range m.users {
		if u.UserName == userName {
			out := *u
			return &out, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockUserStore) Replace(ctx context.Context, orgID uuid.UUID, u *User) (*User, error) {
	if m.replaceFn != nil {
		return m.replaceFn(ctx, orgID, u)
	}
	existing, ok := m.users[u.ID]
	if !ok {
		return nil, errors.New("not found")
	}
	_ = existing
	out := *u
	m.users[u.ID] = &out
	return &out, nil
}

func (m *mockUserStore) SetActive(ctx context.Context, orgID uuid.UUID, id string, active bool) error {
	if m.setActiveFn != nil {
		return m.setActiveFn(ctx, orgID, id, active)
	}
	u, ok := m.users[id]
	if !ok {
		return errors.New("not found")
	}
	u.Active = active
	return nil
}

func (m *mockUserStore) Delete(ctx context.Context, orgID uuid.UUID, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, orgID, id)
	}
	if _, ok := m.users[id]; !ok {
		return errors.New("not found")
	}
	delete(m.users, id)
	return nil
}

func (m *mockUserStore) List(ctx context.Context, orgID uuid.UUID, filter *Filter, start, count int) ([]*User, int, error) {
	if m.listFn != nil {
		return m.listFn(ctx, orgID, filter, start, count)
	}
	var all []*User
	for _, u := range m.users {
		if filter == nil || filter.MatchesUser(u) {
			out := *u
			all = append(all, &out)
		}
	}
	return all, len(all), nil
}

// ---------------------------------------------------------------------------

type mockGroupStore struct {
	groups       map[string]*Group
	createFn     func(ctx context.Context, orgID uuid.UUID, g *Group) (*Group, error)
	byIDFn       func(ctx context.Context, orgID uuid.UUID, id string) (*Group, error)
	replaceFn    func(ctx context.Context, orgID uuid.UUID, g *Group) (*Group, error)
	deleteFn     func(ctx context.Context, orgID uuid.UUID, id string) error
	listFn       func(ctx context.Context, orgID uuid.UUID, filter *Filter, start, count int) ([]*Group, int, error)
	setMembersFn func(ctx context.Context, orgID uuid.UUID, groupID string, memberIDs []string) error
}

func newMockGroupStore() *mockGroupStore {
	return &mockGroupStore{groups: make(map[string]*Group)}
}

func (m *mockGroupStore) Create(ctx context.Context, orgID uuid.UUID, g *Group) (*Group, error) {
	if m.createFn != nil {
		return m.createFn(ctx, orgID, g)
	}
	out := *g
	out.ID = uuid.New().String()
	m.groups[out.ID] = &out
	return &out, nil
}

func (m *mockGroupStore) ByID(ctx context.Context, orgID uuid.UUID, id string) (*Group, error) {
	if m.byIDFn != nil {
		return m.byIDFn(ctx, orgID, id)
	}
	g, ok := m.groups[id]
	if !ok {
		return nil, errors.New("not found")
	}
	out := *g
	return &out, nil
}

func (m *mockGroupStore) Replace(ctx context.Context, orgID uuid.UUID, g *Group) (*Group, error) {
	if m.replaceFn != nil {
		return m.replaceFn(ctx, orgID, g)
	}
	if _, ok := m.groups[g.ID]; !ok {
		return nil, errors.New("not found")
	}
	out := *g
	m.groups[g.ID] = &out
	return &out, nil
}

func (m *mockGroupStore) Delete(ctx context.Context, orgID uuid.UUID, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, orgID, id)
	}
	if _, ok := m.groups[id]; !ok {
		return errors.New("not found")
	}
	delete(m.groups, id)
	return nil
}

func (m *mockGroupStore) List(ctx context.Context, orgID uuid.UUID, filter *Filter, start, count int) ([]*Group, int, error) {
	if m.listFn != nil {
		return m.listFn(ctx, orgID, filter, start, count)
	}
	var all []*Group
	for _, g := range m.groups {
		out := *g
		all = append(all, &out)
	}
	return all, len(all), nil
}

func (m *mockGroupStore) SetMembers(ctx context.Context, orgID uuid.UUID, groupID string, memberIDs []string) error {
	if m.setMembersFn != nil {
		return m.setMembersFn(ctx, orgID, groupID, memberIDs)
	}
	return nil
}

// ---------------------------------------------------------------------------

type mockTokenValidator struct {
	validateFn func(ctx context.Context, orgID uuid.UUID, token string) error
}

func alwaysValidToken() *mockTokenValidator {
	return &mockTokenValidator{
		validateFn: func(_ context.Context, _ uuid.UUID, _ string) error { return nil },
	}
}

func alwaysInvalidToken() *mockTokenValidator {
	return &mockTokenValidator{
		validateFn: func(_ context.Context, _ uuid.UUID, _ string) error {
			return errors.New("invalid token")
		},
	}
}

func (m *mockTokenValidator) Validate(ctx context.Context, orgID uuid.UUID, token string) error {
	if m.validateFn != nil {
		return m.validateFn(ctx, orgID, token)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test setup helpers
// ---------------------------------------------------------------------------

var testOrgID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func newTestHandler(users UserStore, groups GroupStore, tokens TokenValidator) (*Handler, *http.ServeMux) {
	h := NewHandler(users, groups, tokens, "https://auth.example.com/scim/v2")
	mux := http.NewServeMux()
	h.Routes(mux, "/scim/v2")
	return h, mux
}

func doRequest(t *testing.T, mux http.Handler, method, path string, body any, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Org-ID", testOrgID.String())
	req.Header.Set("Authorization", "Bearer valid-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, wantStatus, rr.Code, "unexpected HTTP status for %s %s: body=%s", method, path, rr.Body.String())
	return rr
}

// ---------------------------------------------------------------------------
// User CRUD
// ---------------------------------------------------------------------------

func TestSCIM_CreateUser_201(t *testing.T) {
	users := newMockUserStore()
	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())

	payload := User{
		UserName: "alice@example.com",
		Emails:   []Email{{Value: "alice@example.com", Primary: true}},
		Active:   true,
	}
	rr := doRequest(t, mux, http.MethodPost, "/scim/v2/Users", payload, http.StatusCreated)

	var created User
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&created))
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "alice@example.com", created.UserName)
	assert.NotEmpty(t, rr.Header().Get("Location"), "Location header must be set on 201")
}

func TestSCIM_CreateUser_MissingUserName_400(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysValidToken())
	doRequest(t, mux, http.MethodPost, "/scim/v2/Users", User{}, http.StatusBadRequest)
}

func TestSCIM_GetUser_200(t *testing.T) {
	users := newMockUserStore()
	created, err := users.Create(context.Background(), testOrgID, &User{
		UserName: "bob@example.com",
		Active:   true,
	})
	require.NoError(t, err)

	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())
	rr := doRequest(t, mux, http.MethodGet, "/scim/v2/Users/"+created.ID, nil, http.StatusOK)

	var got User
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	assert.Equal(t, created.ID, got.ID)
}

func TestSCIM_GetUser_NotFound_404(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysValidToken())
	doRequest(t, mux, http.MethodGet, "/scim/v2/Users/nonexistent-id", nil, http.StatusNotFound)
}

func TestSCIM_ReplaceUser_200(t *testing.T) {
	users := newMockUserStore()
	created, err := users.Create(context.Background(), testOrgID, &User{
		UserName: "charlie@example.com",
		Active:   true,
	})
	require.NoError(t, err)

	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())
	updated := User{
		UserName:    "charlie-updated@example.com",
		DisplayName: "Charlie Updated",
		Active:      false,
	}
	rr := doRequest(t, mux, http.MethodPut, "/scim/v2/Users/"+created.ID, updated, http.StatusOK)

	var got User
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&got))
	assert.Equal(t, "charlie-updated@example.com", got.UserName)
}

func TestSCIM_PatchUser_Deactivate_200(t *testing.T) {
	users := newMockUserStore()
	created, err := users.Create(context.Background(), testOrgID, &User{
		UserName: "dave@example.com",
		Active:   true,
	})
	require.NoError(t, err)

	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())
	patch := PatchOp{
		Schemas: []string{schemaPatch},
		Operations: []PatchOperation{
			{Op: "replace", Path: "active", Value: json.RawMessage(`false`)},
		},
	}
	doRequest(t, mux, http.MethodPatch, "/scim/v2/Users/"+created.ID, patch, http.StatusOK)

	// Confirm the store was updated.
	u, err := users.ByID(context.Background(), testOrgID, created.ID)
	require.NoError(t, err)
	assert.False(t, u.Active, "user should be deactivated after PATCH")
}

func TestSCIM_DeleteUser_204(t *testing.T) {
	users := newMockUserStore()
	created, err := users.Create(context.Background(), testOrgID, &User{
		UserName: "eve@example.com",
	})
	require.NoError(t, err)

	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())
	doRequest(t, mux, http.MethodDelete, "/scim/v2/Users/"+created.ID, nil, http.StatusNoContent)

	_, err = users.ByID(context.Background(), testOrgID, created.ID)
	assert.Error(t, err, "user should be gone after DELETE")
}

func TestSCIM_DeleteUser_NotFound_404(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysValidToken())
	doRequest(t, mux, http.MethodDelete, "/scim/v2/Users/does-not-exist", nil, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// List / pagination
// ---------------------------------------------------------------------------

func TestSCIM_ListUsers_200(t *testing.T) {
	users := newMockUserStore()
	for i := 0; i < 5; i++ {
		_, err := users.Create(context.Background(), testOrgID, &User{
			UserName: fmt.Sprintf("user%d@example.com", i),
			Active:   true,
		})
		require.NoError(t, err)
	}

	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())
	rr := doRequest(t, mux, http.MethodGet, "/scim/v2/Users?startIndex=1&count=10", nil, http.StatusOK)

	var resp listResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 5, resp.TotalResults)
	assert.Equal(t, 1, resp.StartIndex)
	assert.Len(t, resp.Resources, 5)
}

func TestSCIM_ListUsers_FilterByUserName(t *testing.T) {
	users := newMockUserStore()
	for _, name := range []string{"test@example.com", "other@example.com"} {
		_, err := users.Create(context.Background(), testOrgID, &User{
			UserName: name,
			Active:   true,
		})
		require.NoError(t, err)
	}

	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())
	rr := doRequest(t, mux, http.MethodGet,
		`/scim/v2/Users?filter=userName+eq+"test@example.com"`, nil, http.StatusOK)

	var resp listResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 1, resp.TotalResults)
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

func TestSCIM_ListGroups_200(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysValidToken())
	rr := doRequest(t, mux, http.MethodGet, "/scim/v2/Groups", nil, http.StatusOK)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestSCIM_CreateGroup_201(t *testing.T) {
	groups := newMockGroupStore()
	_, mux := newTestHandler(newMockUserStore(), groups, alwaysValidToken())

	payload := Group{DisplayName: "Engineering"}
	rr := doRequest(t, mux, http.MethodPost, "/scim/v2/Groups", payload, http.StatusCreated)

	var created Group
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&created))
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "Engineering", created.DisplayName)
	assert.NotEmpty(t, rr.Header().Get("Location"))
}

func TestSCIM_ReplaceGroup_WithMembers_200(t *testing.T) {
	groups := newMockGroupStore()
	g, err := groups.Create(context.Background(), testOrgID, &Group{DisplayName: "Ops"})
	require.NoError(t, err)

	membersSet := false
	groups.setMembersFn = func(_ context.Context, _ uuid.UUID, _ string, memberIDs []string) error {
		membersSet = true
		assert.Len(t, memberIDs, 2)
		return nil
	}

	_, mux := newTestHandler(newMockUserStore(), groups, alwaysValidToken())
	payload := Group{
		DisplayName: "Ops",
		Members: []GroupMember{
			{Value: "uid-1"},
			{Value: "uid-2"},
		},
	}
	doRequest(t, mux, http.MethodPut, "/scim/v2/Groups/"+g.ID, payload, http.StatusOK)
	assert.True(t, membersSet, "SetMembers must be called on PUT /Groups/{id}")
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

func TestSCIM_Unauthorized_NoToken_401(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysInvalidToken())

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("X-Org-ID", testOrgID.String())
	// No Authorization header.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSCIM_Unauthorized_InvalidToken_401(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysInvalidToken())

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("X-Org-ID", testOrgID.String())
	req.Header.Set("Authorization", "Bearer bad-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSCIM_Unauthorized_MissingOrgID_401(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysValidToken())

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	// No X-Org-ID header.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

func TestSCIM_ServiceProviderConfig_200(t *testing.T) {
	_, mux := newTestHandler(newMockUserStore(), newMockGroupStore(), alwaysValidToken())

	req := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	var doc map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&doc))
	schemas, _ := doc["schemas"].([]any)
	assert.NotEmpty(t, schemas)
}

func TestSCIM_ContentType_ApplicationSCIMJSON(t *testing.T) {
	users := newMockUserStore()
	_, mux := newTestHandler(users, newMockGroupStore(), alwaysValidToken())

	_, err := users.Create(context.Background(), testOrgID, &User{UserName: "ct@example.com"})
	require.NoError(t, err)

	rr := doRequest(t, mux, http.MethodGet, "/scim/v2/Users", nil, http.StatusOK)
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/scim+json")
}
