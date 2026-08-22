-- +goose Up
CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_id   UUID        NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    credential_id BYTEA       NOT NULL UNIQUE,
    public_key    BYTEA       NOT NULL,
    raw           JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_webauthn_identity ON webauthn_credentials (identity_id);

-- WebAuthn session challenges (short-lived, evicted by TTL in Redis in prod;
-- stored here as fallback for single-instance deployments).
CREATE TABLE IF NOT EXISTS webauthn_sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flow_id    UUID        NOT NULL UNIQUE,
    session    JSONB       NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS webauthn_sessions;
DROP TABLE IF EXISTS webauthn_credentials;
