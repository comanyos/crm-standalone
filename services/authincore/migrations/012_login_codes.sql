-- +goose Up

CREATE TABLE IF NOT EXISTS login_codes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL,
    identity_id UUID NOT NULL,
    email       TEXT NOT NULL,
    code        TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, email)
);
CREATE INDEX IF NOT EXISTS idx_login_codes_expiry ON login_codes(expires_at);

-- +goose Down
DROP TABLE IF EXISTS login_codes;
