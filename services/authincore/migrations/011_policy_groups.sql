-- +goose Up

CREATE TABLE IF NOT EXISTS policies (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL,
    name        TEXT NOT NULL,
    rules       JSONB NOT NULL DEFAULT '[]',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_policies_org ON policies(org_id);

CREATE TABLE IF NOT EXISTS policy_bindings (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL,
    policy_id   UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    resource    TEXT NOT NULL,
    resource_id UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_policy_bindings_org ON policy_bindings(org_id);
CREATE INDEX IF NOT EXISTS idx_policy_bindings_resource ON policy_bindings(resource, resource_id);

CREATE TABLE IF NOT EXISTS reputation_scores (
    org_id      UUID NOT NULL,
    identity_id UUID NOT NULL,
    score       INT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, identity_id)
);

CREATE TABLE IF NOT EXISTS groups (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_groups_org ON groups(org_id);

CREATE TABLE IF NOT EXISTS group_members (
    group_id    UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    identity_id UUID NOT NULL,
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, identity_id)
);

-- +goose Down
DROP TABLE IF EXISTS group_members;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS reputation_scores;
DROP TABLE IF EXISTS policy_bindings;
DROP TABLE IF EXISTS policies;
