package authz

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// The scopes DBCore's interceptor actually checks. Kept here so a change to the
// role catalogue that drops one of them fails a test rather than a user.
const (
	connRead   = "dbcore:connection:read"
	connWrite  = "dbcore:connection:write"
	queryRead  = "dbcore:query:read"
	queryWrite = "dbcore:query:write"
	schemaRead = "dbcore:schema:read"
	dbcoreAll  = "dbcore:*"
)

// grant is one role_assignments row. project is nil for an organization-level
// assignment, mirroring the nullable project_id column.
type grant struct {
	org      uuid.UUID
	identity uuid.UUID
	roleKey  string
	project  *uuid.UUID
}

// fakeStores mirrors the Postgres stores' org filtering. Both real queries carry
// `WHERE org_id = $1`; a double that ignored the org would let a cross-tenant
// test pass while the production query was broken, so the filter is reproduced
// rather than assumed.
type fakeStores struct {
	grants []grant
	// perms maps org → role key → scopes, matching permissions joined to roles
	// on (org_id, key).
	perms map[uuid.UUID]map[string][]string

	roleErr error
	permErr error
}

func (f *fakeStores) RolesForIdentity(_ context.Context, orgID, identityID uuid.UUID, scope RoleScope) ([]Role, error) {
	if f.roleErr != nil {
		return nil, f.roleErr
	}
	var out []Role
	for _, g := range f.grants {
		if g.org != orgID || g.identity != identityID {
			continue
		}
		// Reproduce the SQL predicate exactly. Organization scope is
		// "project_id IS NULL", not "any project" — a double that ignored this
		// would keep passing while the leak it exists to catch came back.
		if scope.ProjectID == nil {
			if g.project != nil {
				continue
			}
		} else if g.project == nil || *g.project != *scope.ProjectID {
			continue
		}
		r := Role{Key: g.roleKey, OrgID: orgID}
		if g.project != nil {
			r.ProjectID = *g.project
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeStores) PermissionsForRoles(_ context.Context, orgID uuid.UUID, roles []Role) ([]Permission, error) {
	if f.permErr != nil {
		return nil, f.permErr
	}
	byRole := f.perms[orgID]
	var out []Permission
	for _, r := range roles {
		if scopes, ok := byRole[r.Key]; ok {
			out = append(out, Permission{ResourceType: "dbcore", Scopes: scopes})
		}
	}
	return out, nil
}

// dbcoreCatalogue is the mapping migration 025 seeds into every org.
func dbcoreCatalogue() map[string][]string {
	return map[string][]string{
		"owner":    {dbcoreAll},
		"admin":    {dbcoreAll},
		"operator": {connRead, connWrite, queryRead, queryWrite, schemaRead},
		"readonly": {connRead, queryRead, schemaRead},
	}
}

func TestResolveMapsRolesToScopes(t *testing.T) {
	org := uuid.New()

	tests := []struct {
		name       string
		roleKeys   []string
		wantRoles  []string
		wantScopes []string
	}{
		{
			name:       "admin holds the module wildcard",
			roleKeys:   []string{"admin"},
			wantRoles:  []string{"admin"},
			wantScopes: []string{dbcoreAll},
		},
		{
			name:      "operator reads and writes connections and queries, reads schema",
			roleKeys:  []string{"operator"},
			wantRoles: []string{"operator"},
			// Sorted, because Resolve sorts.
			wantScopes: []string{connRead, connWrite, queryRead, queryWrite, schemaRead},
		},
		{
			name:       "read-only never gets a write scope",
			roleKeys:   []string{"readonly"},
			wantRoles:  []string{"readonly"},
			wantScopes: []string{connRead, queryRead, schemaRead},
		},
		{
			name: "an unknown role grants nothing",
			// A role key with no permissions row is the only handling unknown
			// roles get. There is no default tier to fall back to.
			roleKeys:   []string{"marketing"},
			wantRoles:  []string{"marketing"},
			wantScopes: nil,
		},
		{
			name:       "an unknown role alongside a known one does not dilute it",
			roleKeys:   []string{"marketing", "readonly"},
			wantRoles:  []string{"marketing", "readonly"},
			wantScopes: []string{connRead, queryRead, schemaRead},
		},
		{
			name:      "overlapping roles yield each scope once",
			roleKeys:  []string{"operator", "readonly"},
			wantRoles: []string{"operator", "readonly"},
			// readonly's three scopes are all in operator's five.
			wantScopes: []string{connRead, connWrite, queryRead, queryWrite, schemaRead},
		},
		{
			name:       "no role assignment yields no authority at all",
			roleKeys:   nil,
			wantRoles:  nil,
			wantScopes: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			identity := uuid.New()
			f := &fakeStores{perms: map[uuid.UUID]map[string][]string{org: dbcoreCatalogue()}}
			for _, k := range tc.roleKeys {
				f.grants = append(f.grants, grant{org: org, identity: identity, roleKey: k})
			}

			roles, scopes, err := NewClaimsResolver(f, f).Resolve(context.Background(), org, identity)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !reflect.DeepEqual(roles, tc.wantRoles) {
				t.Errorf("roles = %v, want %v", roles, tc.wantRoles)
			}
			if !reflect.DeepEqual(scopes, tc.wantScopes) {
				t.Errorf("scopes = %v, want %v", scopes, tc.wantScopes)
			}
		})
	}
}

// Read-only must not reach a write scope through any path. Asserting the exact
// set above already covers this, but a token that leaked a write scope is the
// specific failure worth naming.
func TestReadOnlyHoldsNoWriteScope(t *testing.T) {
	org, identity := uuid.New(), uuid.New()
	f := &fakeStores{
		perms:  map[uuid.UUID]map[string][]string{org: dbcoreCatalogue()},
		grants: []grant{{org: org, identity: identity, roleKey: "readonly"}},
	}

	_, scopes, err := NewClaimsResolver(f, f).Resolve(context.Background(), org, identity)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, s := range scopes {
		if s == connWrite || s == queryWrite || s == dbcoreAll {
			t.Errorf("read-only token carries %q", s)
		}
	}
}

// An identity that is an admin in one organization must resolve to nothing in
// another. This is the isolation the whole tenancy model rests on: the same
// person can hold a session in both orgs, and the token minted for one must not
// carry authority granted in the other.
func TestCrossOrganizationAssignmentsAreIgnored(t *testing.T) {
	orgA, orgB := uuid.New(), uuid.New()
	identity := uuid.New()

	f := &fakeStores{
		perms: map[uuid.UUID]map[string][]string{
			orgA: dbcoreCatalogue(),
			orgB: dbcoreCatalogue(),
		},
		grants: []grant{{org: orgA, identity: identity, roleKey: "admin"}},
	}
	r := NewClaimsResolver(f, f)

	roles, scopes, err := r.Resolve(context.Background(), orgA, identity)
	if err != nil {
		t.Fatalf("Resolve orgA: %v", err)
	}
	if !reflect.DeepEqual(scopes, []string{dbcoreAll}) {
		t.Fatalf("orgA scopes = %v, want the admin wildcard", scopes)
	}
	if !reflect.DeepEqual(roles, []string{"admin"}) {
		t.Fatalf("orgA roles = %v", roles)
	}

	roles, scopes, err = r.Resolve(context.Background(), orgB, identity)
	if err != nil {
		t.Fatalf("Resolve orgB: %v", err)
	}
	if roles != nil || scopes != nil {
		t.Errorf("an admin in orgA resolved to roles=%v scopes=%v in orgB", roles, scopes)
	}
}

// Two identities in the same org must not see each other's grants.
func TestOtherIdentitiesGrantsAreIgnored(t *testing.T) {
	org := uuid.New()
	admin, stranger := uuid.New(), uuid.New()

	f := &fakeStores{
		perms:  map[uuid.UUID]map[string][]string{org: dbcoreCatalogue()},
		grants: []grant{{org: org, identity: admin, roleKey: "admin"}},
	}

	roles, scopes, err := NewClaimsResolver(f, f).Resolve(context.Background(), org, stranger)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if roles != nil || scopes != nil {
		t.Errorf("unrelated identity resolved to roles=%v scopes=%v", roles, scopes)
	}
}

// A store failure must not degrade into an empty-but-successful resolution: that
// would mint a token that silently authorises nothing and turn a database
// outage into a wave of inexplicable 403s.
func TestStoreErrorsPropagate(t *testing.T) {
	org, identity := uuid.New(), uuid.New()

	t.Run("role store", func(t *testing.T) {
		f := &fakeStores{roleErr: errors.New("connection refused")}
		if _, _, err := NewClaimsResolver(f, f).Resolve(context.Background(), org, identity); err == nil {
			t.Fatal("a role store failure resolved successfully")
		}
	})

	t.Run("permission store", func(t *testing.T) {
		f := &fakeStores{
			permErr: errors.New("connection refused"),
			grants:  []grant{{org: org, identity: identity, roleKey: "admin"}},
		}
		if _, _, err := NewClaimsResolver(f, f).Resolve(context.Background(), org, identity); err == nil {
			t.Fatal("a permission store failure resolved successfully")
		}
	})
}

// A nil resolver is what a deployment with no authz stores wired gets. It must
// mint an unauthorised token rather than panic in the login handler.
func TestNilResolverIsSafe(t *testing.T) {
	var r *ClaimsResolver
	roles, scopes, err := r.Resolve(context.Background(), uuid.New(), uuid.New())
	if err != nil || roles != nil || scopes != nil {
		t.Errorf("nil resolver returned roles=%v scopes=%v err=%v", roles, scopes, err)
	}
}

// A role granted inside one project must not become organization-wide
// authority, and must not leak into a sibling project. Before RoleScope existed
// the store selected every assignment in the org regardless of project_id, so
// making someone Admin of a scratch project silently made them Admin of DBCore
// everywhere.
func TestProjectRolesDoNotLeak(t *testing.T) {
	org := uuid.New()
	identity := uuid.New()
	projectA, projectB := uuid.New(), uuid.New()

	f := &fakeStores{
		perms:  map[uuid.UUID]map[string][]string{org: dbcoreCatalogue()},
		grants: []grant{{org: org, identity: identity, roleKey: "admin", project: &projectA}},
	}
	r := NewClaimsResolver(f, f)

	t.Run("organization level sees nothing", func(t *testing.T) {
		roles, scopes, err := r.Resolve(context.Background(), org, identity)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if roles != nil || scopes != nil {
			t.Errorf("a project-A admin resolved to org-level roles=%v scopes=%v", roles, scopes)
		}
	})

	t.Run("project A sees the grant", func(t *testing.T) {
		roles, scopes, err := r.ResolveForProject(context.Background(), org, identity, projectA)
		if err != nil {
			t.Fatalf("ResolveForProject: %v", err)
		}
		if !reflect.DeepEqual(roles, []string{"admin"}) || !reflect.DeepEqual(scopes, []string{dbcoreAll}) {
			t.Errorf("project A roles=%v scopes=%v, want the admin grant", roles, scopes)
		}
	})

	t.Run("project B sees nothing", func(t *testing.T) {
		roles, scopes, err := r.ResolveForProject(context.Background(), org, identity, projectB)
		if err != nil {
			t.Fatalf("ResolveForProject: %v", err)
		}
		if roles != nil || scopes != nil {
			t.Errorf("a project-A admin resolved to roles=%v scopes=%v in project B", roles, scopes)
		}
	})
}

// Organization-level grants are likewise not visible from inside a project:
// the two scopes are separate, and neither silently unions the other.
func TestOrgRolesAreNotUnionedIntoProjectScope(t *testing.T) {
	org, identity, project := uuid.New(), uuid.New(), uuid.New()
	f := &fakeStores{
		perms:  map[uuid.UUID]map[string][]string{org: dbcoreCatalogue()},
		grants: []grant{{org: org, identity: identity, roleKey: "readonly"}},
	}
	r := NewClaimsResolver(f, f)

	roles, scopes, err := r.Resolve(context.Background(), org, identity)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(roles, []string{"readonly"}) {
		t.Fatalf("org-level roles = %v", roles)
	}
	if len(scopes) != 3 {
		t.Fatalf("org-level scopes = %v", scopes)
	}

	roles, scopes, err = r.ResolveForProject(context.Background(), org, identity, project)
	if err != nil {
		t.Fatalf("ResolveForProject: %v", err)
	}
	if roles != nil || scopes != nil {
		t.Errorf("org-level grant leaked into project scope: roles=%v scopes=%v", roles, scopes)
	}
}

// Mixed grants: the org-level role is the only one a session token may carry,
// even when the same identity holds a broader role inside a project.
func TestMixedGrantsResolveToOrgLevelOnly(t *testing.T) {
	org, identity, project := uuid.New(), uuid.New(), uuid.New()
	f := &fakeStores{
		perms: map[uuid.UUID]map[string][]string{org: dbcoreCatalogue()},
		grants: []grant{
			{org: org, identity: identity, roleKey: "readonly"},
			{org: org, identity: identity, roleKey: "admin", project: &project},
		},
	}

	roles, scopes, err := NewClaimsResolver(f, f).Resolve(context.Background(), org, identity)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(roles, []string{"readonly"}) {
		t.Errorf("roles = %v, want only the org-level readonly", roles)
	}
	for _, s := range scopes {
		if s == dbcoreAll {
			t.Error("project-scoped admin leaked the dbcore wildcard into an org-level token")
		}
	}
}
