-- +goose Up

-- OAuth2 consent requests (pending scope approval).
CREATE TABLE IF NOT EXISTS consent_requests (
    id           TEXT PRIMARY KEY,
    client_id    TEXT NOT NULL,
    client_name  TEXT NOT NULL DEFAULT '',
    identity_id  UUID NOT NULL,
    org_id       UUID NOT NULL,
    scopes       JSONB NOT NULL DEFAULT '[]',
    redirect_uri TEXT NOT NULL,
    state        TEXT NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ NOT NULL,
    granted      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_consent_requests_expiry ON consent_requests(expires_at);

-- Remembered consent grants (user ticked "remember this decision").
CREATE TABLE IF NOT EXISTS consent_grants (
    org_id      UUID NOT NULL,
    identity_id UUID NOT NULL,
    client_id   TEXT NOT NULL,
    scopes      JSONB NOT NULL DEFAULT '[]',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, identity_id, client_id)
);

-- SAML SP provider configurations.
CREATE TABLE IF NOT EXISTS saml_providers (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID NOT NULL,
    name           TEXT NOT NULL,
    metadata_url   TEXT NOT NULL DEFAULT '',
    entity_id      TEXT NOT NULL DEFAULT '',
    sso_url        TEXT NOT NULL DEFAULT '',
    certificate    TEXT NOT NULL DEFAULT '',
    name_id_format TEXT NOT NULL DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    attribute_map  JSONB NOT NULL DEFAULT '{}',
    active         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_saml_providers_org ON saml_providers(org_id);

-- Service accounts (machine identities).
CREATE TABLE IF NOT EXISTS service_accounts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL,
    name        TEXT NOT NULL,
    client_id   TEXT NOT NULL UNIQUE,
    secret_hash TEXT NOT NULL,
    scopes      TEXT[] NOT NULL DEFAULT '{}',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_service_accounts_org ON service_accounts(org_id);
CREATE INDEX IF NOT EXISTS idx_service_accounts_client_id ON service_accounts(client_id);

-- SMS OTPs for MFA second factor.
CREATE TABLE IF NOT EXISTS sms_otps (
    org_id      UUID NOT NULL,
    identity_id UUID NOT NULL,
    phone       TEXT NOT NULL,
    code        TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    attempts    INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, identity_id)
);
CREATE INDEX IF NOT EXISTS idx_sms_otps_expiry ON sms_otps(expires_at);

-- +goose Down
DROP TABLE IF EXISTS sms_otps;
DROP TABLE IF EXISTS service_accounts;
DROP TABLE IF EXISTS saml_providers;
DROP TABLE IF EXISTS consent_grants;
DROP TABLE IF EXISTS consent_requests;
