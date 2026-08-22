-- Migration 002: OAuth2 authorization codes and refresh tokens.

-- +goose Up

CREATE TABLE IF NOT EXISTS oauth2_auth_codes (
    code                   TEXT PRIMARY KEY,
    client_id              TEXT NOT NULL,
    identity_id            UUID NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    org_id                 UUID NOT NULL,
    session_id             UUID NOT NULL,
    redirect_uri           TEXT NOT NULL,
    scopes                 JSONB NOT NULL DEFAULT '[]',
    code_challenge         TEXT,
    code_challenge_method  TEXT,
    expires_at             TIMESTAMPTZ NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_auth_codes_expires ON oauth2_auth_codes(expires_at);

CREATE TABLE IF NOT EXISTS oauth2_refresh_tokens (
    token        TEXT PRIMARY KEY,
    identity_id  UUID NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
    session_id   UUID NOT NULL,
    org_id       UUID NOT NULL,
    client_id    TEXT NOT NULL,
    scopes       JSONB NOT NULL DEFAULT '[]',
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_rt_identity ON oauth2_refresh_tokens(identity_id);
CREATE INDEX idx_rt_session  ON oauth2_refresh_tokens(session_id);
CREATE INDEX idx_rt_expires  ON oauth2_refresh_tokens(expires_at);

-- +goose Down
DROP TABLE IF EXISTS oauth2_refresh_tokens;
DROP TABLE IF EXISTS oauth2_auth_codes;
