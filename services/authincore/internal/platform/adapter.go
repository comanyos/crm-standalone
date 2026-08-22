// Package platform implements the shared platform.Module contract for AuthinCore,
// making the foundation module a first-class suite citizen — it renders in the shell
// sidebar/health-map and exposes Copilot actions.
//
// This is a thin adapter. AuthinCore already has all the underlying capabilities;
// this just maps them onto the single shared interface all 24 modules speak.
package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/command"
	"github.com/authincore/authincore/internal/invitation"
	"github.com/authincore/authincore/internal/query"
	"github.com/google/uuid"
	platform "github.com/santoshguru/platform-contract"
)

const (
	moduleID   = "authincore"
	moduleName = "Platform Core"
	version    = "1.0.0"
	band       = "foundation"
)

// AuthinCore is a platform.Module backed by AuthinCore's running server
// dependencies (DB pool, cache pinger, command/query layer).
type Adapter struct {
	issuer string // e.g. "http://localhost:8080"
	// Pinger for health checks
	dbPinger    Pinger
	cachePinger Pinger
	commands    *command.Commands
	queries     *query.Queries
	invitations *invitation.Service
}

func (a *Adapter) WithServices(commands *command.Commands, queries *query.Queries, invitations *invitation.Service) *Adapter {
	a.commands, a.queries, a.invitations = commands, queries, invitations
	return a
}

// Pinger abstracts a dependency with a Ping() check (pgxpool, redis client).
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewAdapter creates the platform.Module adapter for AuthinCore.
func NewAdapter(issuer string, db, cache Pinger) *Adapter {
	return &Adapter{issuer: issuer, dbPinger: db, cachePinger: cache}
}

// Manifest describes Platform Core for the shell sidebar.
func (a *Adapter) Manifest() platform.Manifest {
	return platform.Manifest{
		ID:      moduleID,
		Name:    moduleName,
		Version: version,
		Edition: platform.EditionOSS,
		Band:    band,
		Icon:    "shield-check",
		Nav: []platform.NavEntry{
			{Label: "Users", Route: "/admin/users"},
			{Label: "Organizations", Route: "/admin/orgs"},
			{Label: "Applications", Route: "/admin/apps"},
			{Label: "SSO", Route: "/admin/sso"},
			{Label: "RBAC", Route: "/admin/rbac"},
			{Label: "Audit", Route: "/admin/audit"},
		},
		Capabilities: []string{
			"oidc", "saml", "scim", "webauthn", "totp", "sms-mfa",
			"magic-link", "rbac", "abac", "fga", "webhooks", "blueprints",
		},
	}
}

// Health probes the database and cache, returning a rollup status for the Health Map.
func (a *Adapter) Health(ctx context.Context) platform.Health {
	now := time.Now().UTC()
	checks := []platform.Check{}

	dbStatus := platform.Green
	if err := a.dbPinger.Ping(ctx); err != nil {
		dbStatus = platform.Red
	}
	checks = append(checks, platform.Check{
		Name: "database", Status: dbStatus, Detail: "Postgres connection",
	})

	if a.cachePinger != nil {
		cacheStatus := platform.Green
		if err := a.cachePinger.Ping(ctx); err != nil {
			cacheStatus = platform.Red
		}
		checks = append(checks, platform.Check{
			Name: "cache", Status: cacheStatus, Detail: "Redis connection",
		})
	}

	rollup := platform.Green
	summary := "all subsystems healthy"
	for _, c := range checks {
		if c.Status == platform.Red {
			rollup = platform.Red
			summary = "one or more subsystems unhealthy"
			break
		}
		if c.Status == platform.Yellow {
			rollup = platform.Yellow
			summary = "one or more subsystems degraded"
		}
	}

	return platform.Health{
		Status:    rollup,
		Summary:   summary,
		Checks:    checks,
		CheckedAt: now,
	}
}

// Actions lists the Copilot actions Platform Core exposes.
func (a *Adapter) Actions() []platform.Action {
	return []platform.Action{
		{
			ID:            "authincore.lookup_identity",
			Description:   "Look up an identity (user) by email or ID within the current org",
			Mutating:      false,
			RequiredScope: "authincore:identity:read",
		},
		{
			ID:            "authincore.list_orgs",
			Description:   "List organizations the caller has access to",
			Mutating:      false,
			RequiredScope: "authincore:org:read",
		},
		{
			ID:            "authincore.create_invite",
			Description:   "Create an invitation to join an organization by email",
			Mutating:      true,
			RequiredScope: "authincore:invitation:write",
		},
		{
			ID:            "authincore.revoke_session",
			Description:   "Revoke an active session for a given identity",
			Mutating:      true,
			RequiredScope: "authincore:session:write",
		},
		{
			ID:            "authincore.health_status",
			Description:   "Return the current health status of Platform Core",
			Mutating:      false,
			RequiredScope: "authincore:status:read",
		},
	}
}

// Invoke dispatches a Copilot action by ID. Stub actions report their availability;
// real implementations can be wired here as the underlying services expose their
// request/response shapes through this package.
func (a *Adapter) Invoke(ctx context.Context, actionID string, params map[string]any) (any, error) {
	switch actionID {
	case "authincore.health_status":
		h := a.Health(ctx)
		return map[string]any{
			"status":  h.Status,
			"summary": h.Summary,
			"checks":  h.Checks,
		}, nil
	case "authincore.lookup_identity":
		orgID, err := trustedUUID(params, "_org_id")
		if err != nil {
			return nil, err
		}
		if a.queries == nil {
			return nil, fmt.Errorf("lookup identity unavailable")
		}
		if rawID, _ := params["id"].(string); rawID != "" {
			id, parseErr := uuid.Parse(rawID)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid identity id")
			}
			return a.queries.GetIdentity(ctx, id, orgID)
		}
		term, _ := params["query"].(string)
		items, total, searchErr := a.queries.SearchIdentities(ctx, query.IdentitySearchQuery{
			OrgID: orgID, Query: term, PageSize: 20,
		})
		return map[string]any{"items": items, "total": total}, searchErr
	case "authincore.list_orgs":
		instanceID, err := trustedUUID(params, "_instance_id")
		if err != nil {
			return nil, err
		}
		if a.queries == nil {
			return nil, fmt.Errorf("list orgs unavailable")
		}
		return a.queries.ListOrgs(ctx, instanceID)
	case "authincore.create_invite":
		orgID, orgErr := trustedUUID(params, "_org_id")
		actorID, actorErr := trustedUUID(params, "_actor_id")
		email, _ := params["email"].(string)
		role, _ := params["role"].(string)
		if orgErr != nil || actorErr != nil || email == "" || a.invitations == nil {
			return nil, fmt.Errorf("create invite unavailable or invalid parameters")
		}
		return a.invitations.Create(ctx, orgID, actorID, email, role)
	case "authincore.revoke_session":
		orgID, orgErr := trustedUUID(params, "_org_id")
		identityID, identityErr := paramUUID(params, "identity_id")
		if orgErr != nil || identityErr != nil || a.commands == nil {
			return nil, fmt.Errorf("revoke session unavailable or invalid parameters")
		}
		count, revokeErr := a.commands.RevokeAllSessions(ctx, identityID, orgID)
		return map[string]any{"revoked": count}, revokeErr
	default:
		return nil, errUnknownAction(actionID)
	}
}

func trustedUUID(params map[string]any, key string) (uuid.UUID, error) {
	return paramUUID(params, key)
}

func paramUUID(params map[string]any, key string) (uuid.UUID, error) {
	raw, _ := params[key].(string)
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	return id, nil
}

// errUnknownAction is a sentinel Action-not-found error.
func errUnknownAction(id string) error {
	return &actionError{msg: "unknown action: " + id}
}

type actionError struct{ msg string }

func (e *actionError) Error() string { return e.msg }
