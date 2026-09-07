package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/auth"
)

// The role matrix, checked as behaviour rather than as a list (task.md §1.4).
//
// The scopes each built-in role grants live in packages/contracts/rbac/
// crm-roles.json, which AuthinCore's bootstrap provisions from and this
// service demands against. Those are two separate Go modules, so nothing but
// a shared file and a test on each side keeps them agreeing.
//
// What these tests assert is not "the JSON says what we think it says" —
// that would only restate the file. It is: given the scopes this role
// actually carries, which procedures can it reach through the real
// interceptor, and is that the intended set? A scope renamed in one place and
// not the other shows up here as a role that can suddenly reach nothing.

type roleMatrix struct {
	Version int      `json:"version"`
	Scopes  []string `json:"scopes"`
	Roles   []struct {
		Key         string   `json:"key"`
		DisplayName string   `json:"displayName"`
		Scopes      []string `json:"scopes"`
		Rationale   string   `json:"rationale"`
	} `json:"roles"`
}

// loadRoleMatrix reads the shared contract. It fails rather than skips: a
// missing matrix means the role definitions are unpinned, which is the
// condition this file exists to detect.
func loadRoleMatrix(t *testing.T) roleMatrix {
	t.Helper()
	var path string
	for _, candidate := range []string{
		"../../../../packages/contracts/rbac/crm-roles.json",
		"../../../packages/contracts/rbac/crm-roles.json",
		"packages/contracts/rbac/crm-roles.json",
	} {
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		t.Fatal("cannot find packages/contracts/rbac/crm-roles.json; the role matrix is unpinned")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read role matrix: %v", err)
	}
	var m roleMatrix
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse role matrix: %v", err)
	}
	if len(m.Roles) == 0 {
		t.Fatal("the role matrix defines no roles")
	}
	return m
}

// principalFor builds the principal AuthinCore would mint for a holder of one
// role in one organization.
func principalFor(t *testing.T, m roleMatrix, roleKey string) *auth.Principal {
	t.Helper()
	for _, role := range m.Roles {
		if role.Key == roleKey {
			return &auth.Principal{
				Subject: "user-" + roleKey, TenantID: "org-1", OrgID: "org-1",
				Roles: []string{roleKey}, Scopes: role.Scopes,
			}
		}
	}
	t.Fatalf("the role matrix has no role %q", roleKey)
	return nil
}

// reachable returns every served procedure the principal may call, decided by
// the same policy the running server enforces.
func reachable(p *auth.Principal) map[string]bool {
	out := map[string]bool{}
	for _, procedure := range servedProcedures() {
		scope, known := scopePolicy.Decide(procedure)
		if !known {
			continue
		}
		if scope == "" || p.HasScope(scope) {
			out[procedure] = true
		}
	}
	return out
}

// TestMatrixScopesAreOnesThisServiceActuallyDemands catches the drift that
// matters most: a scope granted to a role that no procedure requires is dead
// authority, and a scope this service demands that no role grants is a
// procedure nobody can call.
func TestMatrixScopesAreOnesThisServiceActuallyDemands(t *testing.T) {
	m := loadRoleMatrix(t)

	demanded := map[string]bool{}
	for _, scope := range crmScopes {
		demanded[scope] = true
	}
	declared := map[string]bool{}
	for _, scope := range m.Scopes {
		declared[scope] = true
	}

	for scope := range demanded {
		if !declared[scope] {
			t.Errorf("this service demands %q, which the role matrix does not declare; "+
				"no role grants it, so the procedures behind it are unreachable", scope)
		}
	}
	for scope := range declared {
		if !demanded[scope] {
			t.Errorf("the role matrix declares %q, which no procedure requires; "+
				"it is either a typo or authority granted for nothing", scope)
		}
	}

	// Every granted scope must be one of the declared ones, wildcards aside.
	for _, role := range m.Roles {
		for _, scope := range role.Scopes {
			if strings.HasSuffix(scope, ":*") || scope == "crm:*" {
				continue
			}
			if !declared[scope] {
				t.Errorf("role %s grants %q, which is not in the matrix's scope list", role.Key, scope)
			}
		}
	}
}

// TestEachRoleReachesItsIntendedProcedures is the behavioural assertion. The
// expectations are written as entity/verb intent, then compared against what
// the interceptor would really allow.
func TestEachRoleReachesItsIntendedProcedures(t *testing.T) {
	m := loadRoleMatrix(t)

	// intent[role][scope] — may this role hold this authority?
	intent := map[string]map[string]bool{
		"crm_admin": {
			"crm:account:read": true, "crm:account:write": true,
			"crm:contact:read": true, "crm:contact:write": true,
			"crm:lead:read": true, "crm:lead:write": true,
			"crm:opportunity:read": true, "crm:opportunity:write": true,
			"crm:pipeline:read": true, "crm:pipeline:write": true,
			"crm:activity:read": true, "crm:activity:write": true,
			"crm:task:read": true, "crm:task:write": true,
			"crm:note:read": true, "crm:note:write": true,
			"crm:custom_field:read": true, "crm:custom_field:write": true,
			"crm:custom_object:read": true, "crm:custom_object:write": true,
			"crm:summary:read": true,
			"crm:search:read":  true,
			// Only an administrator sees which colleagues have linked a
			// Google account; connecting one's own is separate authority.
			"crm:integration:read": true, "crm:integration:write": true,
			"crm:integration_admin:read": true, "crm:integration_admin:write": true,
			"crm:workflow:read": true, "crm:workflow:write": true,
			"crm:notification:read": true, "crm:notification:write": true,
			"crm:tag:read": true, "crm:tag:write": true,
			"crm:saved_view:read": true, "crm:saved_view:write": true,
			"crm:consent:read": true, "crm:consent:write": true,
			"crm:template:read": true, "crm:template:write": true,
			"crm:capture:read": true, "crm:capture:write": true,
		},
		"crm_manager": {
			"crm:account:read": true, "crm:account:write": true,
			"crm:contact:read": true, "crm:contact:write": true,
			"crm:lead:read": true, "crm:lead:write": true,
			"crm:opportunity:read": true, "crm:opportunity:write": true,
			"crm:pipeline:read": true, "crm:pipeline:write": true,
			"crm:activity:read": true, "crm:activity:write": true,
			"crm:task:read": true, "crm:task:write": true,
			"crm:note:read": true, "crm:note:write": true,
			"crm:custom_field:read": false, "crm:custom_field:write": false,
			"crm:custom_object:read": false, "crm:custom_object:write": false,
			"crm:summary:read":     true,
			"crm:search:read":      true,
			"crm:integration:read": true, "crm:integration:write": true,
			"crm:integration_admin:read": false, "crm:integration_admin:write": false,
			// Automation is manager authority: a rule acts on every record
			// its trigger matches, on behalf of whoever wrote it, until
			// somebody retires it.
			"crm:workflow:read": true, "crm:workflow:write": true,
			"crm:notification:read": true, "crm:notification:write": true,
			"crm:tag:read": true, "crm:tag:write": true,
			"crm:saved_view:read": true, "crm:saved_view:write": true,
			// A manager records and reviews whether the organization may
			// contact somebody — a claim it may later have to defend.
			"crm:consent:read": true, "crm:consent:write": true,
			"crm:template:read": true, "crm:template:write": true,
			"crm:capture:read": true, "crm:capture:write": true,
		},
		"crm_member": {
			"crm:account:read": true, "crm:account:write": false,
			"crm:contact:read": true, "crm:contact:write": true,
			"crm:lead:read": true, "crm:lead:write": true,
			"crm:opportunity:read": true, "crm:opportunity:write": true,
			// A member does not configure pipelines but must be able to read
			// them: the opportunity form and board cannot render a stage
			// without its name, position and close-reason rule.
			"crm:pipeline:read": true, "crm:pipeline:write": false,
			"crm:activity:read": true, "crm:activity:write": true,
			// Tasks and notes are the bulk of an individual contributor's
			// day, so both are read/write even though pipelines are not.
			"crm:task:read": true, "crm:task:write": true,
			"crm:note:read": true, "crm:note:write": true,
			"crm:custom_field:read": false, "crm:custom_field:write": false,
			"crm:custom_object:read": false, "crm:custom_object:write": false,
			"crm:summary:read": true,
			"crm:search:read":  true,
			// An individual contributor links their own mailbox; they do not
			// get to see whose else is linked.
			"crm:integration:read": true, "crm:integration:write": true,
			"crm:integration_admin:read": false, "crm:integration_admin:write": false,
			// A member reads the automation rules so they can see which rule
			// created the task in front of them, and authors none: a rule
			// acts on every record its trigger matches, for as long as it is
			// active, which is manager authority.
			"crm:workflow:read": true, "crm:workflow:write": false,
			"crm:notification:read": true, "crm:notification:write": true,
			"crm:tag:read": true, "crm:tag:write": true,
			"crm:saved_view:read": true, "crm:saved_view:write": true,
			// Write, unlike workflow authoring: the person who hears "stop
			// calling me" is the one on the call, and a withdrawal that has
			// to wait for a manager is a withdrawal that gets ignored.
			"crm:consent:read": true, "crm:consent:write": true,
			// Read/write for the same reason tags are: wording a rep uses on
			// their own calls is daily work, not process configuration.
			"crm:template:read": true, "crm:template:write": true,
			// Not a member's: issuing an endpoint mints a URL that lets
			// anybody holding it create leads without a session, which is a
			// different thing from being allowed to type a lead in. The
			// delivery log also holds the payloads people submitted.
			"crm:capture:read": false, "crm:capture:write": false,
		},
		"crm_viewer": {
			"crm:account:read": true, "crm:account:write": false,
			"crm:contact:read": true, "crm:contact:write": false,
			"crm:lead:read": true, "crm:lead:write": false,
			"crm:opportunity:read": true, "crm:opportunity:write": false,
			"crm:pipeline:read": true, "crm:pipeline:write": false,
			"crm:activity:read": true, "crm:activity:write": false,
			"crm:task:read": true, "crm:task:write": false,
			"crm:note:read": true, "crm:note:write": false,
			"crm:custom_field:read": false, "crm:custom_field:write": false,
			"crm:custom_object:read": false, "crm:custom_object:write": false,
			"crm:summary:read": true,
			"crm:search:read":  true,
			// A read-only role has no CRM records to sync a mailbox into, so
			// it holds no integration authority at all.
			"crm:integration:read": false, "crm:integration:write": false,
			"crm:integration_admin:read": false, "crm:integration_admin:write": false,
			"crm:workflow:read": false, "crm:workflow:write": false,
			// A viewer reads its notifications and cannot mark them read.
			// Acknowledging one is a write, and holding no write authority is
			// this role's defining property rather than an approximation of
			// it — see TestViewerHoldsNoWriteAuthority.
			"crm:notification:read": true, "crm:notification:write": false,
			"crm:tag:read": true, "crm:tag:write": false,
			"crm:saved_view:read": false, "crm:saved_view:write": false,
			// The one read this role gains beyond the records themselves.
			// Seeing that somebody opted out is what stops a wasted call;
			// asserting that a third party opted in is authority it must not
			// have, so the pair splits rather than being withheld entirely.
			"crm:consent:read": true, "crm:consent:write": false,
			// Sees what colleagues send, cannot author it.
			"crm:template:read": true, "crm:template:write": false,
			"crm:capture:read": false, "crm:capture:write": false,
		},
	}

	if len(intent) != len(m.Roles) {
		t.Fatalf("the matrix defines %d roles but this test states intent for %d; "+
			"a new role needs its intended authority written down here", len(m.Roles), len(intent))
	}

	for _, role := range m.Roles {
		want, stated := intent[role.Key]
		if !stated {
			t.Errorf("no intended authority stated for role %q", role.Key)
			continue
		}
		t.Run(role.Key, func(t *testing.T) {
			p := principalFor(t, m, role.Key)
			can := reachable(p)

			for procedure, scope := range crmScopes {
				allowed := can[procedure]
				intended, known := want[scope]
				if !known {
					t.Errorf("%s requires %q, which this role's intent does not mention", procedure, scope)
					continue
				}
				if allowed != intended {
					verb := "cannot"
					if allowed {
						verb = "can"
					}
					t.Errorf("%s %s call %s (scope %q); intended reachable=%v",
						role.Key, verb, procedure, scope, intended)
				}
			}
		})
	}
}

// TestViewerHoldsNoWriteAuthority states the viewer's defining property
// directly, rather than leaving it to be inferred from a list of scopes. A
// :write scope added to this role by accident would still satisfy a
// list-comparison test if the list were updated in the same edit.
func TestViewerHoldsNoWriteAuthority(t *testing.T) {
	m := loadRoleMatrix(t)
	p := principalFor(t, m, "crm_viewer")

	for _, scope := range p.Scopes {
		if strings.HasSuffix(scope, ":write") || strings.HasSuffix(scope, ":*") {
			t.Errorf("crm_viewer holds %q; a viewer must hold no write authority", scope)
		}
	}

	var mutationsAllowed []string
	for procedure := range reachable(p) {
		method := procedure[strings.LastIndex(procedure, "/")+1:]
		if isMutation(method) {
			mutationsAllowed = append(mutationsAllowed, procedure)
		}
	}
	sort.Strings(mutationsAllowed)
	if len(mutationsAllowed) > 0 {
		t.Errorf("crm_viewer can reach %d mutating procedures: %v", len(mutationsAllowed), mutationsAllowed)
	}
}

// TestOnlyTheAdminRoleUsesAWildcard keeps the wildcard from spreading. Every
// other role enumerates its scopes, so a scope added for a future entity has
// to be granted deliberately rather than inherited by anyone holding a "*".
func TestOnlyTheAdminRoleUsesAWildcard(t *testing.T) {
	m := loadRoleMatrix(t)
	for _, role := range m.Roles {
		for _, scope := range role.Scopes {
			if !strings.Contains(scope, "*") {
				continue
			}
			if role.Key != "crm_admin" {
				t.Errorf("role %s grants the wildcard %q; only crm_admin may", role.Key, scope)
			}
			if scope != "crm:*" {
				t.Errorf("role %s grants %q; the only permitted wildcard is crm:*", role.Key, scope)
			}
		}
	}
}

// TestEveryRoleExplainsItself makes the matrix reviewable. A role whose
// rationale is missing cannot be argued with, and a role nobody can argue
// with accretes scopes.
func TestEveryRoleExplainsItself(t *testing.T) {
	m := loadRoleMatrix(t)
	seen := map[string]bool{}
	for _, role := range m.Roles {
		if seen[role.Key] {
			t.Errorf("role %q is defined twice", role.Key)
		}
		seen[role.Key] = true
		if strings.TrimSpace(role.DisplayName) == "" {
			t.Errorf("role %q has no display name", role.Key)
		}
		if len(strings.TrimSpace(role.Rationale)) < 60 {
			t.Errorf("role %q has no real rationale (%q)", role.Key, role.Rationale)
		}
		if len(role.Scopes) == 0 {
			t.Errorf("role %q grants nothing", role.Key)
		}
	}
}

// TestUnscopedProceduresAreReachableByEveryRole confirms the waiver list does
// what it claims: the shell's manifest and health calls work for a viewer,
// which is the reason they are unscoped in the first place.
func TestUnscopedProceduresAreReachableByEveryRole(t *testing.T) {
	m := loadRoleMatrix(t)
	for _, role := range m.Roles {
		can := reachable(principalFor(t, m, role.Key))
		for procedure := range unscopedProcedures {
			if !can[procedure] {
				t.Errorf("%s cannot call %s, which is meant to need no scope", role.Key, procedure)
			}
		}
	}
}
