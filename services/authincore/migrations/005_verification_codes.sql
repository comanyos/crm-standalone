-- +goose Up
CREATE TABLE IF NOT EXISTS verification_codes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL,
    identity_id UUID        NOT NULL,
    email       TEXT        NOT NULL,
    code        TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT         NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_verification_codes_org_email ON verification_codes (org_id, email);

-- +goose Down
DROP TABLE IF EXISTS verification_codes;
