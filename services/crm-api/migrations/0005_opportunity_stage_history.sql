-- 0005_opportunity_stage_history.sql
-- B2: append-only stage history for the Opportunity pipeline.
--
-- The pipeline needs to answer "where was this deal on date X" and "how long
-- did it sit in each stage" — questions last-write-wins storage on
-- opportunities.stage cannot answer. This table is written once per stage
-- change (never updated, never deleted) by internal/domain's UpdateOpportunity
-- alongside the row it describes.
--
-- Follows migrations/0003_rls_enforce.sql's idiom exactly: ENABLE + FORCE +
-- a tenant policy together, since this table has never existed before.
--
-- Migrations are append-only: add 0006_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS opportunity_stage_history (
    tenant_id      text        NOT NULL,
    id             text        NOT NULL,
    opportunity_id text        NOT NULL,
    from_stage     text        NOT NULL DEFAULT '',
    to_stage       text        NOT NULL,
    changed_at     timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS opportunity_stage_history_opportunity_idx
    ON opportunity_stage_history (tenant_id, opportunity_id, changed_at);

ALTER TABLE opportunity_stage_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE opportunity_stage_history FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS opportunity_stage_history_tenant_policy ON opportunity_stage_history;
CREATE POLICY opportunity_stage_history_tenant_policy ON opportunity_stage_history
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- crm_app (created by migrations/0003_rls_enforce.sql) is what the service
-- connects as in a real deployment.
GRANT SELECT, INSERT, UPDATE, DELETE ON opportunity_stage_history TO crm_app;

COMMIT;
