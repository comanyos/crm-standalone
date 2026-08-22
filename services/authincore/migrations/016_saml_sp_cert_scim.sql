-- +goose Up

-- Persist SP signing cert+key so the SP identity survives restarts.
ALTER TABLE saml_providers
    ADD COLUMN IF NOT EXISTS sp_cert_pem TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sp_key_pem  TEXT NOT NULL DEFAULT '';

-- SAML IdP: SP registrations that are allowed to receive assertions from us.
CREATE TABLE IF NOT EXISTS saml_service_providers (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID NOT NULL,
    name           TEXT NOT NULL,
    entity_id      TEXT NOT NULL,
    acs_url        TEXT NOT NULL,                 -- Assertion Consumer Service URL
    metadata_xml   TEXT NOT NULL DEFAULT '',
    name_id_format TEXT NOT NULL DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    attribute_map  JSONB NOT NULL DEFAULT '{}',   -- identity trait → SAML attribute name
    active         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_saml_sp_entity ON saml_service_providers(org_id, entity_id);

-- SCIM: provisioned users/groups from external identity providers (Okta, Azure AD).
CREATE TABLE IF NOT EXISTS scim_users (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID NOT NULL,
    external_id    TEXT NOT NULL,                 -- the SCIM externalId from the IdP
    identity_id    UUID REFERENCES identities(id) ON DELETE SET NULL,
    username       TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    emails         JSONB NOT NULL DEFAULT '[]',
    active         BOOLEAN NOT NULL DEFAULT TRUE,
    raw            JSONB NOT NULL DEFAULT '{}',   -- full SCIM resource as received
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_scim_users_ext ON scim_users(org_id, external_id);

CREATE TABLE IF NOT EXISTS scim_groups (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID NOT NULL,
    external_id  TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_scim_groups_ext ON scim_groups(org_id, external_id);

CREATE TABLE IF NOT EXISTS scim_group_members (
    group_id UUID NOT NULL REFERENCES scim_groups(id) ON DELETE CASCADE,
    user_id  UUID NOT NULL REFERENCES scim_users(id)  ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

-- IdP signing key-pair (one per org; persisted so cert is stable across restarts).
CREATE TABLE IF NOT EXISTS saml_idp_keys (
    org_id      UUID PRIMARY KEY,
    sp_cert_pem TEXT NOT NULL,
    sp_key_pem  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- SCIM bearer tokens (one per org, issued by an admin).
CREATE TABLE IF NOT EXISTS scim_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL UNIQUE,
    token_hash TEXT NOT NULL,   -- bcrypt
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS scim_tokens;
DROP TABLE IF EXISTS saml_idp_keys;
DROP TABLE IF EXISTS scim_group_members;
DROP TABLE IF EXISTS scim_groups;
DROP TABLE IF EXISTS scim_users;
DROP TABLE IF EXISTS saml_service_providers;
ALTER TABLE saml_providers DROP COLUMN IF EXISTS sp_cert_pem;
ALTER TABLE saml_providers DROP COLUMN IF EXISTS sp_key_pem;
