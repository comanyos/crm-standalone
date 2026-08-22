-- Migration 004: Account recovery codes.

-- +goose Up

CREATE TABLE IF NOT EXISTS recovery_codes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL,
    identity_id UUID NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    code        TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_recovery_org_email ON recovery_codes(org_id, email);
CREATE INDEX idx_recovery_expires   ON recovery_codes(expires_at);

-- +goose Down
DROP TABLE IF EXISTS recovery_codes;
