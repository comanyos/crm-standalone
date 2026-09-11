-- 0033_simple_auth.sql
-- Minimal first-party identity for Savvy CRM.  Authentication data is global
-- metadata; tenant-owned CRM rows remain protected by their existing RLS
-- policies and are reached only after a membership has selected a tenant.

BEGIN;

CREATE TABLE IF NOT EXISTS auth_users (
    id               text        PRIMARY KEY,
    email_normalized text        NOT NULL UNIQUE,
    email            text        NOT NULL,
    first_name       text        NOT NULL DEFAULT '',
    last_name        text        NOT NULL DEFAULT '',
    email_verified_at timestamptz,
    active           boolean     NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_users_email_not_blank CHECK (length(btrim(email_normalized)) > 0)
);

CREATE TABLE IF NOT EXISTS auth_organizations (
    id         text        PRIMARY KEY,
    name       text        NOT NULL,
    active     boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_organizations_name_not_blank CHECK (length(btrim(name)) > 0)
);

CREATE TABLE IF NOT EXISTS auth_memberships (
    user_id         text        NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    organization_id text        NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    role_key        text        NOT NULL,
    job_role        text        NOT NULL DEFAULT '',
    sales_team_size text        NOT NULL DEFAULT '',
    active          boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, organization_id),
    CONSTRAINT auth_memberships_role_known CHECK (
        role_key IN ('crm_admin', 'crm_manager', 'crm_member', 'crm_viewer')
    )
);
CREATE INDEX IF NOT EXISTS auth_memberships_organization_idx
    ON auth_memberships (organization_id, user_id) WHERE active;

CREATE TABLE IF NOT EXISTS auth_email_challenges (
    id                 text        PRIMARY KEY,
    email_normalized   text        NOT NULL,
    flow               text        NOT NULL,
    code_digest        bytea       NOT NULL,
    attempts_remaining integer     NOT NULL DEFAULT 5,
    expires_at         timestamptz NOT NULL,
    consumed_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_email_challenges_flow_known CHECK (flow IN ('login', 'signup')),
    CONSTRAINT auth_email_challenges_attempts_valid CHECK (attempts_remaining >= 0)
);
CREATE INDEX IF NOT EXISTS auth_email_challenges_lookup_idx
    ON auth_email_challenges (id, email_normalized, expires_at);
CREATE INDEX IF NOT EXISTS auth_email_challenges_rate_idx
    ON auth_email_challenges (email_normalized, created_at DESC);
CREATE INDEX IF NOT EXISTS auth_email_challenges_created_idx
    ON auth_email_challenges (created_at);
CREATE INDEX IF NOT EXISTS auth_email_challenges_expiry_idx
    ON auth_email_challenges (expires_at);

CREATE TABLE IF NOT EXISTS auth_sessions (
    id                 text        PRIMARY KEY,
    token_digest       bytea       NOT NULL UNIQUE,
    user_id            text        NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    organization_id    text        NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    expires_at         timestamptz NOT NULL,
    revoked_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_sessions_membership_fk
        FOREIGN KEY (user_id, organization_id)
        REFERENCES auth_memberships(user_id, organization_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS auth_sessions_active_idx
    ON auth_sessions (token_digest, expires_at) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS auth_sessions_user_idx
    ON auth_sessions (user_id, organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry_idx
    ON auth_sessions (expires_at);
CREATE INDEX IF NOT EXISTS auth_sessions_revoked_idx
    ON auth_sessions (revoked_at) WHERE revoked_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS auth_external_identities (
    provider         text        NOT NULL,
    provider_subject text        NOT NULL,
    user_id          text        NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    email_at_link    text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, provider_subject),
    CONSTRAINT auth_external_identities_provider_known CHECK (provider IN ('google'))
);
CREATE UNIQUE INDEX IF NOT EXISTS auth_external_identities_user_provider_key
    ON auth_external_identities (user_id, provider);

CREATE TABLE IF NOT EXISTS auth_oauth_states (
    state_digest      bytea       PRIMARY KEY,
    binding_digest    bytea       NOT NULL,
    flow              text        NOT NULL,
    nonce              text        NOT NULL,
    pkce_verifier      text        NOT NULL,
    organization_name text        NOT NULL DEFAULT '',
    job_role           text        NOT NULL DEFAULT '',
    sales_team_size    text        NOT NULL DEFAULT '',
    expires_at         timestamptz NOT NULL,
    consumed_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_oauth_states_flow_known CHECK (flow IN ('login', 'signup'))
);
-- Keep this migration repeatable for development databases that applied an
-- earlier draft before transaction binding was introduced. Outstanding OAuth
-- attempts are ephemeral and cannot be safely upgraded without their secret
-- browser binding, so discard only those incomplete rows.
ALTER TABLE auth_oauth_states ADD COLUMN IF NOT EXISTS binding_digest bytea;
DELETE FROM auth_oauth_states WHERE binding_digest IS NULL;
ALTER TABLE auth_oauth_states ALTER COLUMN binding_digest SET NOT NULL;
CREATE INDEX IF NOT EXISTS auth_oauth_states_expiry_idx ON auth_oauth_states (expires_at);
CREATE INDEX IF NOT EXISTS auth_oauth_states_created_idx ON auth_oauth_states (created_at);

-- The restricted application role is created by migration 0003 on supported
-- deployments.  Conditional grants keep this migration usable in databases
-- where that role is provisioned externally.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON
            auth_users, auth_organizations, auth_memberships,
            auth_email_challenges, auth_sessions,
            auth_external_identities, auth_oauth_states
        TO crm_app;
    END IF;
END $$;

COMMIT;
