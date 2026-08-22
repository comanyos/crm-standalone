-- 0004_event_outbox.sql
--
-- Track A3: the producer seam for CRM's future domain events. Nothing in
-- the domain writes to this table yet — Track B decides which state
-- transitions publish, and calls domain.EnqueueEvent when it does. This
-- migration only builds the durable table, its RLS, and the crm_drain grant
-- so that decision does not have to reopen RLS wiring later.
--
-- Mirrors leave's migrations/0006_unpaid_leave_outbox.sql and hcm's
-- outbox migrations.
--
-- Migrations are append-only: add 0005_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS event_outbox (
    tenant_id       text        NOT NULL,
    id              text        NOT NULL,

    event_type      text        NOT NULL,   -- "<entity>.<verb>"
    entity_type     text        NOT NULL,
    entity_id       text        NOT NULL,
    source_version  bigint      NOT NULL,

    correlation_id  text        NOT NULL,
    causation_id    text        NOT NULL DEFAULT '',
    actor           text        NOT NULL,
    occurred_at     timestamptz NOT NULL,

    -- The envelope exactly as a consumer receives it. The columns above
    -- index and filter it; this is the payload of record.
    envelope        jsonb       NOT NULL,

    status          text        NOT NULL DEFAULT 'pending',
    attempts        int         NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    last_error      text        NOT NULL DEFAULT '',

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT event_outbox_status CHECK (
        status IN ('pending', 'sent', 'published', 'failed')
    ),
    CONSTRAINT event_outbox_versioned CHECK (source_version >= 1),
    CONSTRAINT event_outbox_typed CHECK (position('.' in event_type) > 0),
    CONSTRAINT event_outbox_attributed CHECK (btrim(actor) <> ''),
    CONSTRAINT event_outbox_terminal_not_scheduled CHECK (
        status IN ('pending', 'sent') OR next_attempt_at IS NULL
    )
);

-- One event per (entity, version) per tenant. A retried write produces the
-- same record at the same version, so this turns "the caller pressed save
-- twice" into one event rather than two identical ones racing to the bus.
CREATE UNIQUE INDEX IF NOT EXISTS event_outbox_source_key
    ON event_outbox (tenant_id, entity_type, entity_id, source_version);

-- The drain query: oldest first, only what is due.
CREATE INDEX IF NOT EXISTS event_outbox_due_idx
    ON event_outbox (tenant_id, next_attempt_at, created_at)
    WHERE status IN ('pending', 'sent');

CREATE INDEX IF NOT EXISTS event_outbox_entity_idx
    ON event_outbox (tenant_id, entity_id, source_version);

ALTER TABLE event_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_outbox FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS event_outbox_tenant_policy ON event_outbox;
CREATE POLICY event_outbox_tenant_policy ON event_outbox
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON event_outbox TO crm_app;

-- crm_drain (created by migrations/0003_rls_enforce.sql) is the only role
-- allowed to read across tenants, and only for DueOutboxTenants: its job is
-- to discover which tenants have work waiting, which has no tenant to bind.
-- It returns identifiers only; every read that follows uses a real Scope on
-- the application pool.
DO $$
BEGIN
    IF (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
            EXECUTE format('GRANT SELECT ON event_outbox TO crm_drain');
        END IF;
    END IF;
END $$;

COMMIT;
