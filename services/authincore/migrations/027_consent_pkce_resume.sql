-- Persist the complete authorization request across a user-consent roundtrip.
-- Without these values a public OIDC client cannot resume Authorization Code
-- + PKCE safely after consent.
-- +goose Up
ALTER TABLE consent_requests
    ADD COLUMN IF NOT EXISTS code_challenge TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS code_challenge_method TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE consent_requests
    DROP COLUMN IF EXISTS code_challenge_method,
    DROP COLUMN IF EXISTS code_challenge;
