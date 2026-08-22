-- 0015_custom_field_definitions.sql
-- The metadata layer's definition table (task_v2 Phase 1.1, step 1 of
-- docs/DESIGN_METADATA_LAYER.md §8).
--
-- Values are NOT stored here. Per the accepted design, a definition is
-- relational — low cardinality, queried constantly, and heavily constrained —
-- while the values it describes live in a `custom_fields` jsonb column on the
-- row they belong to (step 2, a later migration). That split is what keeps the
-- isolation story unchanged: a value sits on a row an existing policy already
-- covers, so no new isolation surface is created for values at all.
--
-- What is deliberately absent: any DDL issued at runtime. A tenant adding a
-- field INSERTs here; nothing ever runs ALTER TABLE. That keeps crm_app on DML
-- grants only — so a SQL injection in this service still cannot reach the
-- schema — and avoids the ACCESS EXCLUSIVE lock that altering a shared table
-- would take across every tenant at once.
--
-- Follows this schema's tenancy rules: tenant_id first in the primary key, RLS
-- enabled and FORCEd, and a policy with both USING and WITH CHECK so a
-- connection scoped to one tenant can neither read nor label rows as another.
-- pgstore_tenant_isolation_test.go discovers tables from the catalog, so this
-- one is covered the moment it exists.
--
-- Migrations are append-only: add 0016_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS custom_field_definitions (
    tenant_id   text        NOT NULL,
    id          text        NOT NULL,

    -- object_type names what the field extends: a shipped entity today
    -- ('account', 'contact', …), a custom object id once Phase 1.2 lands.
    object_type text        NOT NULL,

    -- field_key is the stable key this field occupies in the owning row's
    -- custom_fields document, and it appears verbatim in API payloads. It is
    -- shape-constrained HERE, not only in Go, because it is the one piece of
    -- tenant-authored text that becomes an identifier rather than a value.
    field_key   text        NOT NULL,

    label       text        NOT NULL,
    data_type   text        NOT NULL,

    -- config carries the per-type extras: picklist options, lookup target
    -- object, decimal precision, formula source. Its shape is validated in Go
    -- against data_type, because it varies by type in ways a CHECK cannot
    -- express usefully.
    config      jsonb       NOT NULL DEFAULT '{}',

    required    boolean     NOT NULL DEFAULT false,

    -- archived_at retires a field without deleting it. A deleted definition
    -- would orphan values already written into rows, and an orphaned value is
    -- how a "removed" field reappears later still carrying data — visible to
    -- nobody who could have expected it. Archived fields stop being offered
    -- for new writes and keep describing the values already stored.
    archived_at timestamptz,

    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),

    -- One key per object per tenant. This is what makes field_key safe to use
    -- as the document key: two definitions cannot claim the same slot.
    CONSTRAINT custom_field_definitions_key_unique
        UNIQUE (tenant_id, object_type, field_key),

    CONSTRAINT custom_field_definitions_object_type_shape
        CHECK (object_type ~ '^[a-z][a-z0-9_]{0,62}$'),

    -- Lowercase, alphanumeric, underscore, leading letter, 63 max — the same
    -- shape a SQL identifier would have to satisfy. The field never becomes an
    -- identifier under this design, but constraining it as though it might is
    -- what keeps that true if a later phase indexes on it.
    CONSTRAINT custom_field_definitions_field_key_shape
        CHECK (field_key ~ '^[a-z][a-z0-9_]{0,62}$'),

    CONSTRAINT custom_field_definitions_label_not_blank
        CHECK (length(btrim(label)) > 0),

    CONSTRAINT custom_field_definitions_data_type_known
        CHECK (data_type IN (
            'text', 'long_text', 'number', 'currency', 'percent', 'boolean',
            'date', 'datetime', 'picklist', 'multi_picklist', 'lookup',
            'formula', 'rollup')),

    CONSTRAINT custom_field_definitions_config_is_object
        CHECK (jsonb_typeof(config) = 'object')
);

-- The read every write path performs: "what fields does this object have in
-- this tenant?". Partial on the live rows because validating a write never
-- consults archived definitions.
CREATE INDEX IF NOT EXISTS custom_field_definitions_live_by_object
    ON custom_field_definitions (tenant_id, object_type, field_key)
    WHERE archived_at IS NULL;

ALTER TABLE custom_field_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE custom_field_definitions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS custom_field_definitions_tenant_policy ON custom_field_definitions;
CREATE POLICY custom_field_definitions_tenant_policy ON custom_field_definitions
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- 0003_rls_enforce.sql's ALTER DEFAULT PRIVILEGES only covers roles that
-- existed when it ran, and only for the role that ran it. Grant explicitly so
-- this table behaves like every other one regardless of migration order.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON custom_field_definitions TO crm_app;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON custom_field_definitions TO crm_drain;
    END IF;
END $$;

COMMIT;
