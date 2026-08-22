-- +goose Up
CREATE TABLE IF NOT EXISTS oidc_providers (
    id         TEXT        NOT NULL,
    org_id     UUID        NOT NULL,
    name       TEXT        NOT NULL,
    config     JSONB       NOT NULL DEFAULT '{}',
    active     BOOLEAN     NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, id)
);

CREATE TABLE IF NOT EXISTS social_links (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL,
    identity_id UUID        NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    provider_id TEXT        NOT NULL,
    subject     TEXT        NOT NULL,
    raw_claims  JSONB       NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, provider_id, subject)
);

CREATE INDEX IF NOT EXISTS idx_social_links_identity ON social_links (identity_id);

-- +goose Down
DROP TABLE IF EXISTS social_links;
DROP TABLE IF EXISTS oidc_providers;
