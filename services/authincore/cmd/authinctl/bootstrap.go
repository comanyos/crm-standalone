package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	passwordstrategy "github.com/authincore/authincore/internal/flow/strategy/password"
	"github.com/authincore/authincore/internal/tenancy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultInstanceID = "00000000-0000-0000-0000-000000000001"

type bootstrapConfig struct {
	OrgName       string
	OrgDomain     string
	AdminName     string
	AdminEmail    string
	AdminPassword string
	ClientID      string
	RedirectURI   string
}

type bootstrapResult struct {
	OrganizationID       string `json:"organization_id"`
	AdministratorID      string `json:"administrator_id"`
	CRMWebClientID       string `json:"crm_web_client_id"`
	CreatedOrganization  bool   `json:"created_organization"`
	CreatedAdministrator bool   `json:"created_administrator"`
}

func cmdBootstrap(ctx context.Context) error {
	cfg, err := bootstrapConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL environment variable is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	result, err := bootstrap(ctx, pool, cfg)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func bootstrapConfigFromEnv(getenv func(string) string) (bootstrapConfig, error) {
	cfg := bootstrapConfig{
		OrgName:       strings.TrimSpace(getenv("BOOTSTRAP_ORG_NAME")),
		OrgDomain:     strings.TrimSpace(getenv("BOOTSTRAP_ORG_DOMAIN")),
		AdminName:     strings.TrimSpace(getenv("BOOTSTRAP_ADMIN_NAME")),
		AdminEmail:    strings.TrimSpace(strings.ToLower(getenv("BOOTSTRAP_ADMIN_EMAIL"))),
		AdminPassword: getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		ClientID:      strings.TrimSpace(getenv("CRM_WEB_CLIENT_ID")),
		RedirectURI:   strings.TrimSpace(getenv("CRM_WEB_REDIRECT_URI")),
	}
	if cfg.OrgName == "" || cfg.OrgDomain == "" || cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return bootstrapConfig{}, errors.New("BOOTSTRAP_ORG_NAME, BOOTSTRAP_ORG_DOMAIN, BOOTSTRAP_ADMIN_EMAIL, and BOOTSTRAP_ADMIN_PASSWORD are required")
	}
	if len(cfg.AdminPassword) < 12 {
		return bootstrapConfig{}, errors.New("BOOTSTRAP_ADMIN_PASSWORD must be at least 12 characters")
	}
	if !strings.Contains(cfg.AdminEmail, "@") {
		return bootstrapConfig{}, errors.New("BOOTSTRAP_ADMIN_EMAIL must be an email address")
	}
	var err error
	cfg.OrgDomain, err = tenancy.CanonicalDomain(cfg.OrgDomain)
	if err != nil {
		return bootstrapConfig{}, fmt.Errorf("BOOTSTRAP_ORG_DOMAIN: %w", err)
	}
	if cfg.AdminName == "" {
		cfg.AdminName = cfg.AdminEmail
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "crm-web"
	}
	if cfg.RedirectURI == "" {
		cfg.RedirectURI = "http://localhost:3000/auth/callback"
	}
	if !strings.HasPrefix(cfg.RedirectURI, "http://") && !strings.HasPrefix(cfg.RedirectURI, "https://") {
		return bootstrapConfig{}, errors.New("CRM_WEB_REDIRECT_URI must be an absolute http(s) URL")
	}
	return cfg, nil
}

func bootstrap(ctx context.Context, pool *pgxpool.Pool, cfg bootstrapConfig) (bootstrapResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return bootstrapResult{}, fmt.Errorf("begin bootstrap transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	instanceID := uuid.MustParse(defaultInstanceID)
	orgID, createdOrg, err := ensureOrganization(ctx, tx, instanceID, cfg)
	if err != nil {
		return bootstrapResult{}, err
	}
	adminID, createdAdmin, err := ensureAdministrator(ctx, tx, orgID, cfg)
	if err != nil {
		return bootstrapResult{}, err
	}
	roleIDs, err := ensureCRMRoleModel(ctx, tx, orgID)
	if err != nil {
		return bootstrapResult{}, err
	}
	if err := assignBootstrapRoles(ctx, tx, orgID, adminID, roleIDs); err != nil {
		return bootstrapResult{}, err
	}
	if err := ensureCRMWebClient(ctx, tx, orgID, cfg); err != nil {
		return bootstrapResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return bootstrapResult{}, fmt.Errorf("commit bootstrap transaction: %w", err)
	}
	return bootstrapResult{
		OrganizationID: orgID.String(), AdministratorID: adminID.String(), CRMWebClientID: cfg.ClientID,
		CreatedOrganization: createdOrg, CreatedAdministrator: createdAdmin,
	}, nil
}

func ensureOrganization(ctx context.Context, tx pgx.Tx, instanceID uuid.UUID, cfg bootstrapConfig) (uuid.UUID, bool, error) {
	var orgID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM orgs WHERE instance_id=$1 AND primary_domain=$2`, instanceID, cfg.OrgDomain).Scan(&orgID)
	if err == nil {
		return orgID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("find organization: %w", err)
	}
	orgID = uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO orgs (id, instance_id, name, primary_domain, state) VALUES ($1,$2,$3,$4,1)`, orgID, instanceID, cfg.OrgName, cfg.OrgDomain); err != nil {
		return uuid.Nil, false, fmt.Errorf("create organization: %w", err)
	}
	if err := insertBootstrapEvent(ctx, tx, "org", orgID, orgID, "org.created", map[string]string{"name": cfg.OrgName, "primary_domain": cfg.OrgDomain}); err != nil {
		return uuid.Nil, false, err
	}
	return orgID, true, nil
}

func ensureAdministrator(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, cfg bootstrapConfig) (uuid.UUID, bool, error) {
	var identityID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT i.id FROM identities i JOIN identity_credentials c ON c.identity_id=i.id WHERE i.org_id=$1 AND c.type='password' AND c.identifiers @> jsonb_build_array($2::text) LIMIT 1`, orgID, cfg.AdminEmail).Scan(&identityID)
	if err == nil {
		return identityID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("find bootstrap administrator: %w", err)
	}
	hash, err := passwordstrategy.NewArgon2idHasher().Hash(cfg.AdminPassword)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("hash bootstrap administrator password: %w", err)
	}
	identityID = uuid.New()
	traits, _ := json.Marshal(map[string]string{"email": cfg.AdminEmail, "name": cfg.AdminName})
	if _, err := tx.Exec(ctx, `INSERT INTO identities (id, org_id, schema_id, traits, state, available_aal) VALUES ($1,$2,'default',$3,1,1)`, identityID, orgID, traits); err != nil {
		return uuid.Nil, false, fmt.Errorf("create bootstrap administrator: %w", err)
	}
	credentialConfig, _ := json.Marshal(map[string]string{"hashed_password": hash})
	identifiers, _ := json.Marshal([]string{cfg.AdminEmail})
	if _, err := tx.Exec(ctx, `INSERT INTO identity_credentials (id, identity_id, type, identifiers, config, version) VALUES ($1,$2,'password',$3,$4,'v1')`, uuid.New(), identityID, identifiers, credentialConfig); err != nil {
		return uuid.Nil, false, fmt.Errorf("create bootstrap administrator credential: %w", err)
	}
	if err := insertBootstrapEvent(ctx, tx, "identity", identityID, orgID, "identity.created", map[string]any{"schema_id": "default", "traits": json.RawMessage(traits)}); err != nil {
		return uuid.Nil, false, err
	}
	return identityID, true, nil
}

func insertBootstrapEvent(ctx context.Context, tx pgx.Tx, aggregateType string, aggregateID, orgID uuid.UUID, eventType string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal bootstrap event: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO events (id, aggregate_type, aggregate_id, org_id, event_type, payload, editor_actor, sequence) VALUES ($1,$2,$3,$4,$5,$6,'bootstrap',1)`, uuid.New(), aggregateType, aggregateID, orgID, eventType, b)
	if err != nil {
		return fmt.Errorf("write bootstrap event: %w", err)
	}
	return nil
}

type roleDefinition struct {
	Key          string
	DisplayName  string
	ResourceType string
	ResourceName string
	Scopes       []string
}

// crmRoleDefinitions provisions the built-in roles into an organization.
//
// The CRM entries here must match packages/contracts/rbac/crm-roles.json,
// which services/crm-api demands its scopes against. The two live in separate
// Go modules and cannot share a package, so bootstrap_roles_test.go compares
// this slice against that file — drift between what identity grants and what
// the resource server requires is otherwise silent until a role hits a
// permission error in production.
//
// The "owner" entry is deliberately absent from the shared file: it is an
// AuthinCore administration role, not a CRM one, and crm-api has no opinion
// about it.
var crmRoleDefinitions = []roleDefinition{
	{Key: "owner", DisplayName: "Organization Owner", ResourceType: "authincore", ResourceName: "administration", Scopes: []string{"*"}},
	{Key: "crm_admin", DisplayName: "CRM Administrator", ResourceType: "crm", ResourceName: "crm", Scopes: []string{"crm:*"}},
	{Key: "crm_manager", DisplayName: "CRM Manager", ResourceType: "crm", ResourceName: "crm", Scopes: []string{"crm:account:read", "crm:account:write", "crm:contact:read", "crm:contact:write", "crm:lead:read", "crm:lead:write", "crm:opportunity:read", "crm:opportunity:write", "crm:pipeline:read", "crm:pipeline:write", "crm:activity:read", "crm:activity:write", "crm:task:read", "crm:task:write", "crm:note:read", "crm:note:write", "crm:summary:read", "crm:search:read", "crm:integration:read", "crm:integration:write", "crm:workflow:read", "crm:workflow:write", "crm:notification:read", "crm:notification:write"}},
	{Key: "crm_member", DisplayName: "CRM Member", ResourceType: "crm", ResourceName: "crm", Scopes: []string{"crm:account:read", "crm:contact:read", "crm:contact:write", "crm:lead:read", "crm:lead:write", "crm:opportunity:read", "crm:opportunity:write", "crm:pipeline:read", "crm:activity:read", "crm:activity:write", "crm:task:read", "crm:task:write", "crm:note:read", "crm:note:write", "crm:summary:read", "crm:search:read", "crm:integration:read", "crm:integration:write", "crm:workflow:read", "crm:notification:read", "crm:notification:write"}},
	{Key: "crm_viewer", DisplayName: "CRM Viewer", ResourceType: "crm", ResourceName: "crm", Scopes: []string{"crm:account:read", "crm:contact:read", "crm:lead:read", "crm:opportunity:read", "crm:pipeline:read", "crm:activity:read", "crm:task:read", "crm:note:read", "crm:summary:read", "crm:search:read", "crm:notification:read"}},
}

func ensureCRMRoleModel(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (map[string]uuid.UUID, error) {
	roleIDs := make(map[string]uuid.UUID, len(crmRoleDefinitions))
	resourceIDs := make(map[string]uuid.UUID)
	for _, definition := range crmRoleDefinitions {
		resourceKey := definition.ResourceType + ":" + definition.ResourceName
		resourceID, ok := resourceIDs[resourceKey]
		if !ok {
			var err error
			resourceID, err = ensureResource(ctx, tx, orgID, definition.ResourceType, definition.ResourceName)
			if err != nil {
				return nil, err
			}
			resourceIDs[resourceKey] = resourceID
		}
		var roleID uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO roles (id, org_id, key, display_name) VALUES ($1,$2,$3,$4) ON CONFLICT (org_id,key) DO UPDATE SET display_name=EXCLUDED.display_name RETURNING id`, uuid.New(), orgID, definition.Key, definition.DisplayName).Scan(&roleID); err != nil {
			return nil, fmt.Errorf("ensure role %s: %w", definition.Key, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO permissions (id, org_id, role_id, resource_id, scopes) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (role_id,resource_id) DO UPDATE SET scopes=EXCLUDED.scopes`, uuid.New(), orgID, roleID, resourceID, definition.Scopes); err != nil {
			return nil, fmt.Errorf("ensure permissions for role %s: %w", definition.Key, err)
		}
		roleIDs[definition.Key] = roleID
	}
	return roleIDs, nil
}

func ensureResource(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, resourceType, name string) (uuid.UUID, error) {
	var resourceID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM resources WHERE org_id=$1 AND type=$2 AND name=$3 ORDER BY created_at, id LIMIT 1`, orgID, resourceType, name).Scan(&resourceID)
	if err == nil {
		return resourceID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find resource %s/%s: %w", resourceType, name, err)
	}
	resourceID = uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO resources (id, org_id, type, name) VALUES ($1,$2,$3,$4)`, resourceID, orgID, resourceType, name); err != nil {
		return uuid.Nil, fmt.Errorf("create resource %s/%s: %w", resourceType, name, err)
	}
	return resourceID, nil
}

func assignBootstrapRoles(ctx context.Context, tx pgx.Tx, orgID, identityID uuid.UUID, roleIDs map[string]uuid.UUID) error {
	for _, key := range []string{"owner", "crm_admin"} {
		if _, err := tx.Exec(ctx, `INSERT INTO role_assignments (id, org_id, identity_id, role_id) VALUES ($1,$2,$3,$4) ON CONFLICT (org_id,identity_id,role_id) DO NOTHING`, uuid.New(), orgID, identityID, roleIDs[key]); err != nil {
			return fmt.Errorf("assign %s to bootstrap administrator: %w", key, err)
		}
	}
	return nil
}

func ensureCRMWebClient(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, cfg bootstrapConfig) error {
	var projectID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE org_id=$1 AND name='CRM Applications' ORDER BY created_at, id LIMIT 1`, orgID).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		projectID = uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO projects (id, org_id, name) VALUES ($1,$2,'CRM Applications')`, projectID, orgID); err != nil {
			return fmt.Errorf("create CRM applications project: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("find CRM applications project: %w", err)
	}
	clientConfig, _ := json.Marshal(map[string]any{
		"client_id": cfg.ClientID, "client_secret": "", "redirect_uris": []string{cfg.RedirectURI},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"application_type": "spa", "auth_method_type": "none",
	})
	var appID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM applications WHERE org_id=$1 AND type=1 AND config->>'client_id'=$2`, orgID, cfg.ClientID).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `INSERT INTO applications (id, project_id, org_id, name, type, config) VALUES ($1,$2,$3,'CRM Web',1,$4)`, uuid.New(), projectID, orgID, clientConfig); err != nil {
			return fmt.Errorf("create CRM web client: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("find CRM web client: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE applications SET project_id=$1, name='CRM Web', config=$2, updated_at=NOW() WHERE id=$3 AND org_id=$4`, projectID, clientConfig, appID, orgID); err != nil {
		return fmt.Errorf("reconcile CRM web client: %w", err)
	}
	return nil
}
