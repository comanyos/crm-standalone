// Package authz provides UMA 2.0-style fine-grained authorization evaluation.
// It resolves: identity → roles → permissions → resources → scopes.
package authz

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Role represents an identity's role within a project/application.
type Role struct {
	Key         string
	ProjectID   uuid.UUID
	OrgID       uuid.UUID
	DisplayName string
}

// RoleScope selects which of an identity's role assignments count.
//
// role_assignments carries a nullable project_id, and reading them without
// regard for it silently unions authority across unrelated projects: someone
// made Admin of one project became Admin everywhere, including at organization
// level. Making the scope an explicit parameter means no caller can leave that
// choice implicit again.
type RoleScope struct {
	// ProjectID nil means organization-level assignments only — the rows with
	// project_id IS NULL. It is not "any project".
	ProjectID *uuid.UUID
}

// OrgLevel selects only organization-wide assignments. This is the right scope
// for a session token, which is not tied to any one project.
func OrgLevel() RoleScope { return RoleScope{} }

// InProject selects only assignments belonging to one project. Organization-level
// assignments are excluded; callers that want both must ask for both and decide
// deliberately how to combine them.
func InProject(projectID uuid.UUID) RoleScope { return RoleScope{ProjectID: &projectID} }

// RoleStore reads role assignments.
type RoleStore interface {
	RolesForIdentity(ctx context.Context, orgID, identityID uuid.UUID, scope RoleScope) ([]Role, error)
}

// PermissionStore reads UMA resources/permissions.
type PermissionStore interface {
	PermissionsForRoles(ctx context.Context, orgID uuid.UUID, roles []Role) ([]Permission, error)
}

type Permission struct {
	ResourceID   uuid.UUID
	ResourceType string
	Scopes       []string
}

// Enforcer evaluates authorization decisions.
type Enforcer struct {
	roles       RoleStore
	permissions PermissionStore
}

func NewEnforcer(roles RoleStore, permissions PermissionStore) *Enforcer {
	return &Enforcer{roles: roles, permissions: permissions}
}

type CheckRequest struct {
	OrgID      uuid.UUID
	IdentityID uuid.UUID
	Resource   string // resource type or ID
	Scope      string // action (read, write, delete, ...)
}

type CheckResult struct {
	Allowed bool
	Reason  string
}

// Check evaluates whether an identity has the requested scope on a resource.
func (e *Enforcer) Check(ctx context.Context, req CheckRequest) (CheckResult, error) {
	// Organization scope: a role granted inside one project must not decide an
	// organization-level check. A caller that means to evaluate within a
	// project should resolve that project's roles explicitly.
	roles, err := e.roles.RolesForIdentity(ctx, req.OrgID, req.IdentityID, OrgLevel())
	if err != nil {
		return CheckResult{}, fmt.Errorf("authz roles: %w", err)
	}
	for _, role := range roles {
		if role.Key == "owner" || role.Key == "admin" {
			return CheckResult{Allowed: true, Reason: "administrative role"}, nil
		}
	}

	perms, err := e.permissions.PermissionsForRoles(ctx, req.OrgID, roles)
	if err != nil {
		return CheckResult{}, fmt.Errorf("authz permissions: %w", err)
	}

	for _, p := range perms {
		if p.ResourceType == req.Resource || p.ResourceID.String() == req.Resource {
			for _, s := range p.Scopes {
				if s == req.Scope || s == "*" {
					return CheckResult{Allowed: true, Reason: "permission matched"}, nil
				}
			}
		}
	}
	return CheckResult{Allowed: false, Reason: fmt.Sprintf("no permission for %s:%s", req.Resource, req.Scope)}, nil
}

// HasRole checks if an identity has a specific role in any project.
func (e *Enforcer) HasRole(ctx context.Context, orgID, identityID uuid.UUID, roleKey string) (bool, error) {
	roles, err := e.roles.RolesForIdentity(ctx, orgID, identityID, OrgLevel())
	if err != nil {
		return false, err
	}
	for _, r := range roles {
		if r.Key == roleKey {
			return true, nil
		}
	}
	return false, nil
}
