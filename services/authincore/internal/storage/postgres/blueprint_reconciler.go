package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/authincore/authincore/internal/blueprint"
	"github.com/authincore/authincore/internal/storage/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type blueprintReconciler struct {
	db    *pgxpool.Pool
	model string
}

// NewBlueprintReconcilers returns the supported, production-backed blueprint
// models. Identifiers are converted to deterministic UUIDs so repeated applies
// are idempotent and references remain stable.
func NewBlueprintReconcilers(db *pgxpool.Pool) []blueprint.Reconciler {
	models := []string{
		"authincore/org",
		"authincore/identity-schema",
		"authincore/project",
		"authincore/application",
		"authincore/webhook",
	}
	out := make([]blueprint.Reconciler, 0, len(models))
	for _, model := range models {
		out = append(out, &blueprintReconciler{db: db, model: model})
	}
	return out
}

func (r *blueprintReconciler) Model() string { return r.model }

func (r *blueprintReconciler) Reconcile(ctx context.Context, orgID uuid.UUID, entry blueprint.Entry, dryRun bool) (string, error) {
	if strings.TrimSpace(entry.Identifier) == "" {
		return "", fmt.Errorf("entry id is required")
	}
	state := entry.State
	if state == "" {
		state = "present"
	}
	if state != "present" && state != "absent" {
		return "", fmt.Errorf("state must be present or absent")
	}
	switch r.model {
	case "authincore/org":
		return r.org(ctx, orgID, entry, state, dryRun)
	case "authincore/identity-schema":
		return r.identitySchema(ctx, orgID, entry, state, dryRun)
	case "authincore/project":
		return r.project(ctx, orgID, entry, state, dryRun)
	case "authincore/application":
		return r.application(ctx, orgID, entry, state, dryRun)
	case "authincore/webhook":
		return r.webhook(ctx, orgID, entry, state, dryRun)
	default:
		return "", fmt.Errorf("unsupported model %q", r.model)
	}
}

func (r *blueprintReconciler) org(ctx context.Context, orgID uuid.UUID, entry blueprint.Entry, state string, dry bool) (string, error) {
	if state == "absent" {
		return "", fmt.Errorf("a tenant cannot delete itself through a blueprint")
	}
	name, err := requiredString(entry.Attrs, "name")
	if err != nil {
		return "", err
	}
	domain, err := requiredString(entry.Attrs, "primary_domain")
	if err != nil {
		return "", err
	}
	var oldName, oldDomain string
	db := database.ExecutorFor(ctx, r.db)
	if err := db.QueryRow(ctx, `SELECT name, primary_domain FROM orgs WHERE id=$1`, orgID).Scan(&oldName, &oldDomain); err != nil {
		return "", fmt.Errorf("load org: %w", err)
	}
	if oldName == name && oldDomain == domain {
		return "unchanged", nil
	}
	if !dry {
		if _, err := db.Exec(ctx, `UPDATE orgs SET name=$1, primary_domain=$2, updated_at=NOW() WHERE id=$3`, name, domain, orgID); err != nil {
			return "", fmt.Errorf("update org: %w", err)
		}
	}
	return "updated", nil
}

func (r *blueprintReconciler) identitySchema(ctx context.Context, orgID uuid.UUID, entry blueprint.Entry, state string, dry bool) (string, error) {
	id := entry.Identifier
	var existing []byte
	db := database.ExecutorFor(ctx, r.db)
	err := db.QueryRow(ctx, `SELECT schema FROM identity_schemas WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&existing)
	exists := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("load identity schema: %w", err)
	}
	if state == "absent" {
		return r.delete(ctx, dry, exists, `DELETE FROM identity_schemas WHERE org_id=$1 AND id=$2`, orgID, id)
	}
	raw, err := requiredString(entry.Attrs, "schema")
	if err != nil || !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("schema must be valid JSON")
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, []byte(raw))
	if exists && jsonEqual(existing, compact.Bytes()) {
		return "unchanged", nil
	}
	if !dry {
		_, err = db.Exec(ctx, `INSERT INTO identity_schemas (org_id,id,schema,version) VALUES ($1,$2,$3,1)
			ON CONFLICT (org_id,id) DO UPDATE SET schema=EXCLUDED.schema, version=identity_schemas.version+1`, orgID, id, compact.Bytes())
		if err != nil {
			return "", fmt.Errorf("upsert identity schema: %w", err)
		}
	}
	return createdOrUpdated(exists), nil
}

func (r *blueprintReconciler) project(ctx context.Context, orgID uuid.UUID, entry blueprint.Entry, state string, dry bool) (string, error) {
	id := stableID(orgID, "project", entry.Identifier)
	var oldName string
	db := database.ExecutorFor(ctx, r.db)
	err := db.QueryRow(ctx, `SELECT name FROM projects WHERE id=$1 AND org_id=$2`, id, orgID).Scan(&oldName)
	exists := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("load project: %w", err)
	}
	if state == "absent" {
		return r.delete(ctx, dry, exists, `DELETE FROM projects WHERE id=$1 AND org_id=$2`, id, orgID)
	}
	name, err := requiredString(entry.Attrs, "name")
	if err != nil {
		return "", err
	}
	if exists && oldName == name {
		return "unchanged", nil
	}
	if !dry {
		_, err = db.Exec(ctx, `INSERT INTO projects (id,org_id,name) VALUES ($1,$2,$3)
			ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, updated_at=NOW() WHERE projects.org_id=EXCLUDED.org_id`, id, orgID, name)
		if err != nil {
			return "", fmt.Errorf("upsert project: %w", err)
		}
	}
	return createdOrUpdated(exists), nil
}

func (r *blueprintReconciler) application(ctx context.Context, orgID uuid.UUID, entry blueprint.Entry, state string, dry bool) (string, error) {
	id := stableID(orgID, "application", entry.Identifier)
	var oldName string
	var oldType int
	var oldConfig []byte
	db := database.ExecutorFor(ctx, r.db)
	err := db.QueryRow(ctx, `SELECT name,type,config FROM applications WHERE id=$1 AND org_id=$2`, id, orgID).Scan(&oldName, &oldType, &oldConfig)
	exists := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("load application: %w", err)
	}
	if state == "absent" {
		return r.delete(ctx, dry, exists, `DELETE FROM applications WHERE id=$1 AND org_id=$2`, id, orgID)
	}
	name, err := requiredString(entry.Attrs, "name")
	if err != nil {
		return "", err
	}
	projectRef, err := requiredString(entry.Attrs, "project_id")
	if err != nil {
		return "", err
	}
	typeName, err := requiredString(entry.Attrs, "type")
	if err != nil {
		return "", err
	}
	types := map[string]int{"oidc": 1, "saml": 2, "api": 3}
	appType, ok := types[strings.ToLower(typeName)]
	if !ok {
		return "", fmt.Errorf("application type must be oidc, saml, or api")
	}
	config := entry.Attrs["config"]
	if config == nil {
		config = entry.Attrs[typeName+"_config"]
	}
	if config == nil {
		config = map[string]any{}
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("application config: %w", err)
	}
	if exists && oldName == name && oldType == appType && jsonEqual(oldConfig, configJSON) {
		return "unchanged", nil
	}
	projectID := stableID(orgID, "project", projectRef)
	if !dry {
		_, err = db.Exec(ctx, `INSERT INTO applications (id,project_id,org_id,name,type,config) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET project_id=EXCLUDED.project_id,name=EXCLUDED.name,type=EXCLUDED.type,config=EXCLUDED.config,updated_at=NOW()
			WHERE applications.org_id=EXCLUDED.org_id`, id, projectID, orgID, name, appType, configJSON)
		if err != nil {
			return "", fmt.Errorf("upsert application: %w", err)
		}
	}
	return createdOrUpdated(exists), nil
}

func (r *blueprintReconciler) webhook(ctx context.Context, orgID uuid.UUID, entry blueprint.Entry, state string, dry bool) (string, error) {
	id := stableID(orgID, "webhook", entry.Identifier)
	var oldURL string
	var oldEvents []byte
	var oldEnabled bool
	db := database.ExecutorFor(ctx, r.db)
	err := db.QueryRow(ctx, `SELECT url,events,enabled FROM webhooks WHERE id=$1 AND org_id=$2`, id, orgID).Scan(&oldURL, &oldEvents, &oldEnabled)
	exists := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("load webhook: %w", err)
	}
	if state == "absent" {
		return r.delete(ctx, dry, exists, `DELETE FROM webhooks WHERE id=$1 AND org_id=$2`, id, orgID)
	}
	endpoint, err := requiredString(entry.Attrs, "url")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", fmt.Errorf("webhook url must be an absolute HTTPS URL")
	}
	events, err := json.Marshal(entry.Attrs["events"])
	if err != nil || string(events) == "null" {
		return "", fmt.Errorf("webhook events are required")
	}
	enabled := true
	if value, ok := entry.Attrs["enabled"].(bool); ok {
		enabled = value
	}
	if exists && oldURL == endpoint && oldEnabled == enabled && jsonEqual(oldEvents, events) {
		return "unchanged", nil
	}
	if !dry {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return "", fmt.Errorf("generate webhook signing secret: %w", err)
		}
		_, err = db.Exec(ctx, `INSERT INTO webhooks (id,org_id,url,events,enabled,signing_secret) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET url=EXCLUDED.url,events=EXCLUDED.events,enabled=EXCLUDED.enabled,updated_at=NOW()
			WHERE webhooks.org_id=EXCLUDED.org_id`, id, orgID, endpoint, events, enabled, hex.EncodeToString(secretBytes))
		if err != nil {
			return "", fmt.Errorf("upsert webhook: %w", err)
		}
	}
	return createdOrUpdated(exists), nil
}

func (r *blueprintReconciler) delete(ctx context.Context, dry, exists bool, query string, args ...any) (string, error) {
	if !exists {
		return "unchanged", nil
	}
	if !dry {
		if _, err := database.ExecutorFor(ctx, r.db).Exec(ctx, query, args...); err != nil {
			return "", err
		}
	}
	return "deleted", nil
}

func requiredString(attrs map[string]any, key string) (string, error) {
	value, ok := attrs[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("attribute %s is required", key)
	}
	return strings.TrimSpace(value), nil
}

func stableID(orgID uuid.UUID, model, identifier string) uuid.UUID {
	return uuid.NewSHA1(orgID, []byte(model+":"+identifier))
}

func createdOrUpdated(exists bool) string {
	if exists {
		return "updated"
	}
	return "created"
}

func jsonEqual(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}
