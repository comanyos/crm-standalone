-- +goose Up

-- Identity schema definitions per org.
CREATE TABLE IF NOT EXISTS identity_schemas (
    org_id      UUID NOT NULL,
    id          TEXT NOT NULL,
    schema      JSONB NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, id)
);

-- Courier email templates per org and message type.
CREATE TABLE IF NOT EXISTS courier_templates (
    org_id      UUID NOT NULL,
    type        TEXT NOT NULL,
    subject     TEXT NOT NULL,
    body        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, type)
);

-- Projection state (checkpoint for event tail workers).
CREATE TABLE IF NOT EXISTS projection_state (
    name        TEXT PRIMARY KEY,
    position    BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS projection_state;
DROP TABLE IF EXISTS courier_templates;
DROP TABLE IF EXISTS identity_schemas;
