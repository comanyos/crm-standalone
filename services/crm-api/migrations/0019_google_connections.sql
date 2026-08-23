-- 0019_google_connections.sql
-- Durable, tenant-scoped storage for the Google communication integration
-- (docs/GOOGLE_INTEGRATION.md).
--
-- Two tables, because they answer two different questions and have two very
-- different lifetimes:
--
--   google_connections   — the long-lived link between one CRM user and one
--                          Google account, including the sealed token set.
--   google_oauth_states  — the short-lived, one-time record proving that a
--                          particular callback belongs to a particular
--                          authenticated tenant and actor.
--
-- Why the connection is keyed on (tenant_id, owner_id) rather than on the
-- tenant alone: Gmail and Calendar consent is granted by a person over their
-- own mailbox. A tenant-wide connection would mean one user's consent silently
-- exposing their mail to everyone else in the organization.
--
-- Token material is stored only as `token_ciphertext`, sealed with AES-256-GCM
-- by internal/integration/googleoauth. The associated data binds the
-- ciphertext to (tenant_id, connection id), so a row copied into another
-- tenant — or another connection — fails to open rather than decrypting into
-- a usable credential. No column here ever holds a plaintext access token,
-- refresh token, PKCE verifier, or the provider client secret.
--
-- Migrations are append-only: add 0020_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS google_connections (
    tenant_id               text        NOT NULL,
    id                      text        NOT NULL,
    -- owner_id is the CRM principal (JWT subject) whose Google account this
    -- is. It is not an email address and is never taken from a request body.
    owner_id                text        NOT NULL,
    google_account_email    text        NOT NULL DEFAULT '',
    status                  text        NOT NULL DEFAULT 'connected',
    -- granted_scopes is the space-separated scope string Google returned,
    -- retained verbatim so a downgrade of consent is visible rather than
    -- inferred from a failing API call.
    granted_scopes          text        NOT NULL DEFAULT '',
    token_ciphertext        bytea,
    access_token_expires_at timestamptz,
    connected_by            text        NOT NULL DEFAULT '',
    connected_at            timestamptz,
    last_refreshed_at       timestamptz,
    disconnected_at         timestamptz,
    -- last_error is an operator-facing reason code (see googleErrorReason in
    -- internal/domain/google_connection.go), never a provider response body:
    -- those can carry token and authorization material.
    last_error              text        NOT NULL DEFAULT '',
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT google_connections_owner_not_blank
        CHECK (length(btrim(owner_id)) > 0),
    CONSTRAINT google_connections_status_known
        CHECK (status IN ('connected', 'reauthorization_required', 'disconnected')),
    -- A disconnected connection is a tombstone kept for audit. Holding
    -- ciphertext after a disconnect is the exact failure this integration
    -- must not have: the user was told their access was revoked.
    CONSTRAINT google_connections_disconnected_holds_no_tokens
        CHECK (status <> 'disconnected' OR token_ciphertext IS NULL),
    -- A live connection must actually have something to authenticate with.
    CONSTRAINT google_connections_connected_holds_tokens
        CHECK (status <> 'connected' OR token_ciphertext IS NOT NULL)
);

-- One Google connection per CRM user per tenant. Reconnecting updates this
-- row rather than accumulating orphaned credentials nobody will revoke.
CREATE UNIQUE INDEX IF NOT EXISTS google_connections_owner_key
    ON google_connections (tenant_id, owner_id);

CREATE TABLE IF NOT EXISTS google_oauth_states (
    tenant_id           text        NOT NULL,
    state               text        NOT NULL,
    actor_id            text        NOT NULL,
    -- The PKCE verifier is sealed with the same AEAD as the token set, bound
    -- to (tenant_id, state). A database reader therefore cannot complete a
    -- half-finished authorization on someone else's behalf.
    verifier_ciphertext bytea       NOT NULL,
    -- return_path is a validated same-origin path in the web application, so
    -- the callback cannot be turned into an open redirect.
    return_path         text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    consumed_at         timestamptz,

    PRIMARY KEY (tenant_id, state),
    CONSTRAINT google_oauth_states_state_not_blank
        CHECK (length(btrim(state)) > 0),
    CONSTRAINT google_oauth_states_actor_not_blank
        CHECK (length(btrim(actor_id)) > 0),
    CONSTRAINT google_oauth_states_expires_after_creation
        CHECK (expires_at > created_at)
);

-- The state value is unguessable and globally unique, which is what lets the
-- unauthenticated callback resolve which tenant a redirect belongs to without
-- the browser ever naming a tenant. Enforced below RLS, so two tenants cannot
-- collide on one state even though neither can see the other's row.
CREATE UNIQUE INDEX IF NOT EXISTS google_oauth_states_state_key
    ON google_oauth_states (state);

-- The expiry sweep.
CREATE INDEX IF NOT EXISTS google_oauth_states_expiry_idx
    ON google_oauth_states (tenant_id, expires_at);

ALTER TABLE google_connections  ENABLE ROW LEVEL SECURITY;
ALTER TABLE google_connections  FORCE  ROW LEVEL SECURITY;
ALTER TABLE google_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE google_oauth_states FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS google_connections_tenant_policy ON google_connections;
CREATE POLICY google_connections_tenant_policy ON google_connections
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS google_oauth_states_tenant_policy ON google_oauth_states;
CREATE POLICY google_oauth_states_tenant_policy ON google_oauth_states
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- 0003 granted crm_app/crm_drain on the tables existing at that time, plus
-- default privileges for later ones created by the same role. A deployment
-- whose migration runner differs from the role that ran 0003 would not have
-- picked those defaults up, so grant explicitly rather than assume.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON google_connections, google_oauth_states TO crm_app;
    END IF;
    -- crm_drain reads google_oauth_states across tenants for exactly one
    -- query: resolving which tenant an unauthenticated OAuth callback belongs
    -- to, by its unguessable state value. It returns identifiers only; the
    -- consumption and every write that follows run on the application pool
    -- with that tenant bound. It is deliberately NOT granted on
    -- google_connections, which holds sealed credentials.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON google_oauth_states TO crm_drain;
    END IF;
END $$;

COMMIT;
