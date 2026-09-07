-- 0031_lead_capture.sql
-- Inbound lead capture (task_crm.md §1, docs/LEAD_CAPTURE.md,
-- internal/domain/lead_capture.go).
--
-- Two tables with two different jobs and two very different lifetimes, the
-- same split migrations/0019_google_connections.sql makes:
--
--   lead_capture_endpoints   — the long-lived configuration: which tenant a
--                              URL belongs to, how to check its signature,
--                              and how to read its payload.
--   lead_capture_deliveries  — one row per POST received, kept as the
--                              feature's own debugger and audit trail.
--
-- The token is what makes an unauthenticated request resolvable to a tenant,
-- so it carries a UNIQUE index below RLS — two tenants must not be able to
-- collide on one even though neither can see the other's row. That is exactly
-- how google_oauth_states.state works, and for the same reason: the browser
-- (here, a third-party server) never names a tenant.
--
-- The signing secret is sealed with the same AES-256-GCM helper as the Google
-- token set, bound to (tenant_id, endpoint id), so a row copied into another
-- tenant fails to open rather than yielding a usable secret. There is no
-- column that holds it in plaintext, which is why the UI can show it once and
-- never again.
--
-- Migrations are append-only: add 0032_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS lead_capture_endpoints (
    tenant_id       text        NOT NULL,
    id              text        NOT NULL,
    name            text        NOT NULL,
    -- token is 32 bytes of crypto/rand, hex encoded. It appears in URLs and
    -- in the logs of whatever calls it, so it is deliberately not treated as
    -- a secret: the signature is what authorizes a delivery. This only has to
    -- be unguessable enough that endpoints cannot be enumerated.
    token           text        NOT NULL,
    -- source_kind is an allowlist. v1 ships 'generic' only — see
    -- docs/LEAD_CAPTURE.md's named narrowings for why per-provider adapters
    -- wait for a payload that actually refuses to fit.
    source_kind     text        NOT NULL DEFAULT 'generic',
    secret_ciphertext bytea     NOT NULL,
    -- field_mapping maps the caller's field names onto Lead fields, using the
    -- same vocabulary as CSV import rather than a second one.
    field_mapping   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    default_owner_id text       NOT NULL DEFAULT '',
    status          text        NOT NULL DEFAULT 'active',
    created_by      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_delivery_at timestamptz,
    -- last_error is a bounded reason code (see domain.CaptureReason*), never
    -- a provider response or a fragment of a payload: those carry submitted
    -- personal data.
    last_error      text        NOT NULL DEFAULT '',

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT lead_capture_endpoints_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT lead_capture_endpoints_token_not_blank CHECK (length(token) >= 32),
    CONSTRAINT lead_capture_endpoints_status_known
        CHECK (status IN ('active', 'disabled')),
    CONSTRAINT lead_capture_endpoints_kind_known
        CHECK (source_kind IN ('generic'))
);

-- The token resolves an unauthenticated request to exactly one tenant. Unique
-- below RLS so a collision is impossible rather than merely unlikely, the
-- same property google_oauth_states_state_key holds.
CREATE UNIQUE INDEX IF NOT EXISTS lead_capture_endpoints_token_key
    ON lead_capture_endpoints (token);

CREATE TABLE IF NOT EXISTS lead_capture_deliveries (
    tenant_id   text        NOT NULL,
    id          text        NOT NULL,
    endpoint_id text        NOT NULL,
    -- dedup_key is the provider's own event id when it sends one, and
    -- otherwise a hash of the normalised body. Every webhook provider
    -- retries, so a repeat is the ordinary case rather than an attack.
    dedup_key   text        NOT NULL,
    outcome     text        NOT NULL,
    -- reason is a bounded code. The raw payload is next to it; this column is
    -- for grouping and metrics, so it must stay a small closed set.
    reason      text        NOT NULL DEFAULT '',
    -- raw_payload is what arrived, kept because "the lead came in but the
    -- phone number is missing" is unanswerable without it. It holds submitted
    -- personal data, which is why crm_drain is never granted on this table.
    raw_payload text        NOT NULL DEFAULT '',
    lead_id     text        NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT lead_capture_deliveries_outcome_known
        CHECK (outcome IN ('created', 'duplicate', 'rejected', 'failed'))
);

-- One lead per delivery, however many times the provider sends it. This index
-- is the idempotency: CreateLeadFromDelivery's INSERT targets it, so the
-- second arrival loses the race rather than creating a second lead.
CREATE UNIQUE INDEX IF NOT EXISTS lead_capture_deliveries_dedup_key
    ON lead_capture_deliveries (tenant_id, endpoint_id, dedup_key);

-- "What has this endpoint received" — the delivery log, which is this
-- feature's own debugger.
CREATE INDEX IF NOT EXISTS lead_capture_deliveries_endpoint_idx
    ON lead_capture_deliveries (tenant_id, endpoint_id, received_at DESC, id);

ALTER TABLE lead_capture_endpoints  ENABLE ROW LEVEL SECURITY;
ALTER TABLE lead_capture_endpoints  FORCE  ROW LEVEL SECURITY;
ALTER TABLE lead_capture_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE lead_capture_deliveries FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS lead_capture_endpoints_tenant_policy ON lead_capture_endpoints;
CREATE POLICY lead_capture_endpoints_tenant_policy ON lead_capture_endpoints
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS lead_capture_deliveries_tenant_policy ON lead_capture_deliveries;
CREATE POLICY lead_capture_deliveries_tenant_policy ON lead_capture_deliveries
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON lead_capture_endpoints, lead_capture_deliveries TO crm_app;
    END IF;
    -- crm_drain reads lead_capture_endpoints across tenants for exactly one
    -- query: resolving which tenant an unauthenticated POST belongs to, by
    -- its unguessable token. It returns the tenant, the endpoint id and the
    -- sealed secret needed to check the signature; every read and write that
    -- follows runs on the application pool with that tenant bound.
    --
    -- It is deliberately NOT granted on lead_capture_deliveries, which holds
    -- the personal data people submitted.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON lead_capture_endpoints TO crm_drain;
    END IF;
END $$;

COMMIT;
