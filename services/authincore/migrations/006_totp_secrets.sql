-- +goose Up
CREATE TABLE IF NOT EXISTS totp_secrets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id UUID        NOT NULL UNIQUE,
    secret      TEXT        NOT NULL,
    active      BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS totp_backup_codes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id UUID        NOT NULL,
    code        TEXT        NOT NULL,
    used        BOOLEAN     NOT NULL DEFAULT false,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_totp_backup_codes_identity ON totp_backup_codes (identity_id) WHERE used = false;

-- +goose Down
DROP TABLE IF EXISTS totp_backup_codes;
DROP TABLE IF EXISTS totp_secrets;
