-- +goose Up

CREATE TABLE IF NOT EXISTS audit_export_configs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    type        TEXT NOT NULL CHECK (type IN ('splunk', 'datadog', 'generic_webhook')),
    endpoint    TEXT NOT NULL,
    secret      TEXT NOT NULL DEFAULT '',
    format      TEXT NOT NULL DEFAULT 'json' CHECK (format IN ('json', 'cef')),
    filters     JSONB NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, endpoint)
);

-- +goose Down

DROP TABLE IF EXISTS audit_export_configs;
