package authz

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// ClaimsResolver turns an identity into the roles and scopes that go into a
// token: identity → effective roles → scopes.
//
// It exists so that no login path has to know anything about a product's
// permission model. Before this, every caller of MintAccessToken passed nil for
// roles and scopes, so a user who authenticated successfully still held a token
// that authorised nothing — every downstream module answered 403 to a session
// that had just logged in.
//
// Both underlying stores filter on org_id, so a resolver can only ever return
// roles and scopes granted inside the organization being resolved. An identity
// with an assignment in another org gets nothing here, which is what keeps a
// token from carrying authority across a tenant boundary.
type ClaimsResolver struct {
	roles       RoleStore
	permissions PermissionStore
}

// NewClaimsResolver builds a resolver over the same stores the Enforcer uses, so
// a token's scopes and a live Check() cannot drift apart.
func NewClaimsResolver(roles RoleStore, permissions PermissionStore) *ClaimsResolver {
	return &ClaimsResolver{roles: roles, permissions: permissions}
}

// Resolve returns the identity's role keys and the union of the scopes those
// roles grant, both deduplicated and sorted.
//
// A role with no permission rows contributes no scopes. That is the whole
// handling of unknown roles: an org can assign any role key it likes, and until
// someone grants that role a permission it authorises nothing. There is no
// fallback tier and no default grant.
//
// A nil resolver resolves to nothing rather than failing, so a deployment with
// no authz stores wired keeps minting tokens — unauthorised ones, which is the
// safe direction.
// A session token is not scoped to a project, so it carries organization-level
// authority only. An Admin role granted inside one project stays inside that
// project; it never becomes organization-wide DBCore authority.
func (r *ClaimsResolver) Resolve(ctx context.Context, orgID, identityID uuid.UUID) (roleKeys []string, scopes []string, err error) {
	return r.resolve(ctx, orgID, identityID, OrgLevel())
}

// ResolveForProject resolves the authority an identity holds inside one
// project — for tokens minted in an application/project context.
//
// It returns that project's assignments alone. Organization-level roles are
// deliberately not unioned in: mixing the two here is what turned a narrow
// grant into a broad one, and a caller that genuinely wants both should combine
// them where the intent is visible.
func (r *ClaimsResolver) ResolveForProject(ctx context.Context, orgID, identityID, projectID uuid.UUID) (roleKeys []string, scopes []string, err error) {
	return r.resolve(ctx, orgID, identityID, InProject(projectID))
}

func (r *ClaimsResolver) resolve(ctx context.Context, orgID, identityID uuid.UUID, scope RoleScope) (roleKeys []string, scopes []string, err error) {
	if r == nil || r.roles == nil {
		return nil, nil, nil
	}

	roles, err := r.roles.RolesForIdentity(ctx, orgID, identityID, scope)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve claims: roles for identity: %w", err)
	}
	if len(roles) == 0 {
		return nil, nil, nil
	}

	keys := make([]string, 0, len(roles))
	for _, role := range roles {
		keys = append(keys, role.Key)
	}

	if r.permissions == nil {
		return dedupe(keys), nil, nil
	}

	perms, err := r.permissions.PermissionsForRoles(ctx, orgID, roles)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve claims: permissions for roles: %w", err)
	}

	var granted []string
	for _, p := range perms {
		granted = append(granted, p.Scopes...)
	}

	return dedupe(keys), dedupe(granted), nil
}

// dedupe sorts and removes duplicates. Two roles commonly grant the same scope
// on the same resource; without this the token carries it twice, which bloats
// every request header and makes two equivalent tokens compare unequal.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}
