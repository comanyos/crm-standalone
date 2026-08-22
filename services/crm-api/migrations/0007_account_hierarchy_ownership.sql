-- 0007_account_hierarchy_ownership.sql
-- B3: Account hierarchy acyclicity, ownership audit, and dedup flagging.
--
-- duplicate_of_account_id is a flag (set when website's domain exactly
-- matches another tenant-scoped Account's website at write time), never a
-- block — mirrors leads.duplicate_of_contact_id (0006).
--
-- account_ownership_history is an append-only log of owner_id changes,
-- written once per Update that actually changes Account.owner_id, so
-- territory reporting can answer "who owned this account, and when" —
-- mirrors opportunity_stage_history (0005)'s idiom exactly: ENABLE + FORCE
-- RLS with a tenant policy, since the table has never existed before.
--
-- Account.parent_id itself needs no schema change here — 0002 already added
-- the column; this migration only backs the acyclicity invariant Go now
-- enforces on top of it (internal/domain/account_hierarchy.go).
--
-- Migrations are append-only: add 0008_*.sql, never edit this file.

BEGIN;

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS duplicate_of_account_id text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS account_ownership_history (
    tenant_id     text        NOT NULL,
    id            text        NOT NULL,
    account_id    text        NOT NULL,
    from_owner_id text        NOT NULL DEFAULT '',
    to_owner_id   text        NOT NULL,
    changed_at    timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS account_ownership_history_account_idx
    ON account_ownership_history (tenant_id, account_id, changed_at);

ALTER TABLE account_ownership_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_ownership_history FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS account_ownership_history_tenant_policy ON account_ownership_history;
CREATE POLICY account_ownership_history_tenant_policy ON account_ownership_history
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- crm_app (created by migrations/0003_rls_enforce.sql) is what the service
-- connects as in a real deployment.
GRANT SELECT, INSERT, UPDATE, DELETE ON account_ownership_history TO crm_app;

COMMIT;
