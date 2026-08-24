-- 0026_account_merges.sql
-- Account deduplication and merge audit trail
-- (M4 — Data productivity, internal/domain/merge_accounts.go).
--
-- One row per completed merge, written once and never updated or deleted —
-- see merge_accounts.go's package doc comment for why a merge has no undo
-- and why this table exists anyway: it is the durable record a human needs
-- to reconstruct what happened.
--
-- Migrations are append-only: add 0027_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS account_merges (
    tenant_id              text        NOT NULL,
    id                     text        NOT NULL,
    survivor_account_id    text        NOT NULL,
    duplicate_account_id   text        NOT NULL,
    merged_by              text        NOT NULL DEFAULT '',
    -- field_resolutions and relationships_migrated are both validated as
    -- objects by Go before they reach here — see saved_views' identical
    -- CHECK for why this is a backstop, not the primary defense.
    field_resolutions      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    relationships_migrated jsonb       NOT NULL DEFAULT '{}'::jsonb,
    merged_at              timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT account_merges_survivor_not_blank CHECK (length(btrim(survivor_account_id)) > 0),
    CONSTRAINT account_merges_duplicate_not_blank CHECK (length(btrim(duplicate_account_id)) > 0),
    CONSTRAINT account_merges_distinct CHECK (survivor_account_id <> duplicate_account_id),
    CONSTRAINT account_merges_field_resolutions_is_object CHECK (jsonb_typeof(field_resolutions) = 'object'),
    CONSTRAINT account_merges_relationships_migrated_is_object CHECK (jsonb_typeof(relationships_migrated) = 'object')
);

-- "Every merge this account was ever part of" — as the kept id or the
-- retired one — newest first, the shape ListAccountMerges reads in.
CREATE INDEX IF NOT EXISTS account_merges_survivor_idx
    ON account_merges (tenant_id, survivor_account_id, merged_at DESC);
CREATE INDEX IF NOT EXISTS account_merges_duplicate_idx
    ON account_merges (tenant_id, duplicate_account_id, merged_at DESC);

ALTER TABLE account_merges ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_merges FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS account_merges_tenant_policy ON account_merges;
CREATE POLICY account_merges_tenant_policy ON account_merges
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON account_merges TO crm_app;
    END IF;
END $$;

COMMIT;
