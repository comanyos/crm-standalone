-- Migration 001: Core schema — identities, sessions, flows, events, orgs, projects, apps.
-- System of record: relational tables.
-- Audit trail: events table (append-only, same transaction).

-- +goose Up

-- Instances (deployment singleton)
CREATE TABLE IF NOT EXISTS instances (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    domain      TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Organizations (realm / tenant isolation boundary)
CREATE TABLE IF NOT EXISTS orgs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id    UUID NOT NULL REFERENCES instances(id),
    name           TEXT NOT NULL,
    primary_domain TEXT NOT NULL,
    state          SMALLINT NOT NULL DEFAULT 1,    -- 1=active 2=inactive 3=removed
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (instance_id, primary_domain)
);
CREATE INDEX idx_orgs_instance ON orgs(instance_id);

-- Projects
CREATE TABLE IF NOT EXISTS projects (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_projects_org ON projects(org_id);

-- Applications (OIDC/SAML clients)
CREATE TABLE IF NOT EXISTS applications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id      UUID NOT NULL,
    name        TEXT NOT NULL,
    type        SMALLINT NOT NULL DEFAULT 1,   -- 1=oidc 2=saml 3=api
    config      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_apps_project ON applications(project_id);
CREATE INDEX idx_apps_org ON applications(org_id);

-- Identity Schemas
CREATE TABLE IF NOT EXISTS identity_schemas (
    id         TEXT NOT NULL,
    org_id     UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    version    INT NOT NULL DEFAULT 1,
    schema     JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, id)
);

-- Identities (users)
CREATE TABLE IF NOT EXISTS identities (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    external_id      TEXT,
    schema_id        TEXT NOT NULL,
    traits           JSONB NOT NULL DEFAULT '{}',
    state            SMALLINT NOT NULL DEFAULT 1,   -- 1=active 2=inactive
    available_aal    SMALLINT NOT NULL DEFAULT 0,   -- 0=aal0 1=aal1 2=aal2
    metadata_public  JSONB NOT NULL DEFAULT '{}',
    metadata_admin   JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, external_id)
);
CREATE INDEX idx_identities_org ON identities(org_id);
CREATE INDEX idx_identities_traits ON identities USING GIN (traits);
CREATE INDEX idx_identities_state ON identities(org_id, state);

-- Identity Credentials
CREATE TABLE IF NOT EXISTS identity_credentials (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id UUID NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,                       -- password|oidc|totp|webauthn|lookup_secret|code
    identifiers JSONB NOT NULL DEFAULT '[]',         -- e.g. ["user@example.com"]
    config      JSONB NOT NULL DEFAULT '{}',         -- hashed password, OIDC subject, etc.
    version     TEXT NOT NULL DEFAULT 'v1',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (identity_id, type)
);
CREATE INDEX idx_creds_identity ON identity_credentials(identity_id);
CREATE INDEX idx_creds_identifiers ON identity_credentials USING GIN (identifiers);

-- Sessions
CREATE TABLE IF NOT EXISTS sessions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id          UUID NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    org_id               UUID NOT NULL,
    state                SMALLINT NOT NULL DEFAULT 1,   -- 1=active 2=inactive
    aal                  SMALLINT NOT NULL DEFAULT 0,
    authentication_methods JSONB NOT NULL DEFAULT '[]',
    devices              JSONB NOT NULL DEFAULT '[]',
    authenticated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    issued_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at           TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_sessions_identity ON sessions(identity_id);
CREATE INDEX idx_sessions_org_state ON sessions(org_id, state);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- Flows (in-flight self-service interactions)
CREATE TABLE IF NOT EXISTS flows (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type             SMALLINT NOT NULL,
    state            SMALLINT NOT NULL DEFAULT 1,
    org_id           UUID NOT NULL,
    identity_id      UUID REFERENCES identities(id) ON DELETE SET NULL,
    return_to        TEXT,
    ui               JSONB NOT NULL DEFAULT '{}',
    internal_context JSONB,
    issued_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_flows_org ON flows(org_id);
CREATE INDEX idx_flows_expires ON flows(expires_at);

-- Events (append-only audit log — written atomically with SoR writes)
CREATE TABLE IF NOT EXISTS events (
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    aggregate_type TEXT NOT NULL,
    aggregate_id   UUID NOT NULL,
    org_id         UUID NOT NULL,
    event_type     TEXT NOT NULL,
    payload        JSONB,
    editor_actor   TEXT NOT NULL DEFAULT 'system',
    sequence       BIGINT NOT NULL,
    position       BIGSERIAL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (aggregate_type, aggregate_id, sequence)
);
CREATE INDEX idx_events_position ON events(position);
CREATE INDEX idx_events_org ON events(org_id);
CREATE INDEX idx_events_type ON events(event_type);

-- Projection state (tracks last processed event position per projection)
CREATE TABLE IF NOT EXISTS projection_state (
    name          TEXT PRIMARY KEY,
    last_position BIGINT NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Actions (custom JS hooks)
CREATE TABLE IF NOT EXISTS actions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID NOT NULL,
    name           TEXT NOT NULL,
    script         TEXT NOT NULL,
    timeout_ms     BIGINT NOT NULL DEFAULT 5000,
    allowed_to_fail BOOLEAN NOT NULL DEFAULT false,
    state          SMALLINT NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_actions_org ON actions(org_id);

-- Execution targets (action-trigger bindings)
CREATE TABLE IF NOT EXISTS execution_targets (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id    UUID NOT NULL,
    action_id UUID NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
    trigger   TEXT NOT NULL,
    ord       INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_exec_targets_trigger ON execution_targets(org_id, trigger);

-- Webhooks
CREATE TABLE IF NOT EXISTS webhooks (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID NOT NULL,
    url                TEXT NOT NULL,
    description        TEXT,
    events             JSONB NOT NULL DEFAULT '[]',
    signing_secret     TEXT,
    enabled            BOOLEAN NOT NULL DEFAULT true,
    connect_timeout_ms INT NOT NULL DEFAULT 5000,
    read_timeout_ms    INT NOT NULL DEFAULT 10000,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_webhooks_org ON webhooks(org_id);

-- Webhook delivery events
CREATE TABLE IF NOT EXISTS webhook_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id   UUID NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    state        SMALLINT NOT NULL DEFAULT 1,
    attempts     INT NOT NULL DEFAULT 0,
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ
);
CREATE INDEX idx_wh_events_webhook ON webhook_events(webhook_id);

-- Courier messages
CREATE TABLE IF NOT EXISTS courier_messages (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL,
    type       TEXT NOT NULL,
    recipient  TEXT NOT NULL,
    subject    TEXT,
    body       TEXT,
    data       JSONB,
    status     SMALLINT NOT NULL DEFAULT 1,
    send_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at    TIMESTAMPTZ
);
CREATE INDEX idx_courier_status ON courier_messages(status, created_at);

-- Identity Schemas (moved above identities — FK reference)
-- Blueprints
CREATE TABLE IF NOT EXISTS blueprints (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID NOT NULL,
    name         TEXT NOT NULL,
    version      TEXT,
    state        SMALLINT NOT NULL DEFAULT 0,
    yaml_content TEXT NOT NULL,
    last_error   TEXT,
    last_applied TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX idx_blueprints_org_name ON blueprints(org_id, name);

-- Default instance bootstrap
INSERT INTO instances (id, name, domain)
VALUES ('00000000-0000-0000-0000-000000000001', 'Default', 'localhost')
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS blueprints;
DROP TABLE IF EXISTS courier_messages;
DROP TABLE IF EXISTS webhook_events;
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS execution_targets;
DROP TABLE IF EXISTS actions;
DROP TABLE IF EXISTS projection_state;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS flows;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS identity_credentials;
DROP TABLE IF EXISTS identities;
DROP TABLE IF EXISTS identity_schemas;
DROP TABLE IF EXISTS applications;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS orgs;
DROP TABLE IF EXISTS instances;
