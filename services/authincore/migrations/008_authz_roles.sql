-- +goose Up
CREATE TABLE IF NOT EXISTS roles (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID        NOT NULL,
    key          TEXT        NOT NULL,
    display_name TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, key)
);

CREATE TABLE IF NOT EXISTS role_assignments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL,
    project_id  UUID,
    identity_id UUID        NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    role_id     UUID        NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, identity_id, role_id)
);

CREATE TABLE IF NOT EXISTS resources (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID        NOT NULL,
    type       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL,
    role_id     UUID        NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    resource_id UUID        NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    scopes      TEXT[]      NOT NULL DEFAULT '{}',
    UNIQUE (role_id, resource_id)
);

CREATE INDEX IF NOT EXISTS idx_role_assignments_identity ON role_assignments (org_id, identity_id);
CREATE INDEX IF NOT EXISTS idx_permissions_role ON permissions (role_id);

-- +goose Down
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS resources;
DROP TABLE IF EXISTS role_assignments;
DROP TABLE IF EXISTS roles;
