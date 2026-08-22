-- +goose Up

CREATE TABLE IF NOT EXISTS org_branding (
    org_id          UUID PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    logo_url        TEXT NOT NULL DEFAULT '',
    primary_color   TEXT NOT NULL DEFAULT '#6366f1',
    theme           JSONB NOT NULL DEFAULT '{}',
    custom_css      TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down

DROP TABLE IF EXISTS org_branding;
