-- 0012_pipeline_definitions.sql
-- Foundation for tenant-configurable opportunity pipelines. The current
-- fixed-stage Opportunity flow remains live until the application migration
-- to pipeline_id/stage_id is complete; these tables are deliberately added
-- first so that transition without a destructive rewrite is possible.

BEGIN;

CREATE TABLE IF NOT EXISTS pipelines (
    tenant_id   text        NOT NULL,
    id          text        NOT NULL,
    name        text        NOT NULL,
    is_default  boolean     NOT NULL DEFAULT false,
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT pipelines_name_not_blank CHECK (length(btrim(name)) > 0)
);

-- Exactly one active default is allowed per tenant. Archived pipelines retain
-- their historical records but can never be selected for new opportunities.
CREATE UNIQUE INDEX IF NOT EXISTS pipelines_one_active_default_per_tenant_idx
    ON pipelines (tenant_id)
    WHERE is_default AND archived_at IS NULL;

CREATE TABLE IF NOT EXISTS pipeline_stages (
    tenant_id              text        NOT NULL,
    pipeline_id            text        NOT NULL,
    id                     text        NOT NULL,
    name                   text        NOT NULL,
    position               integer     NOT NULL,
    probability            numeric(5,2) NOT NULL DEFAULT 0,
    terminal_type          text        NOT NULL DEFAULT 'open',
    requires_close_reason  boolean     NOT NULL DEFAULT false,
    archived_at            timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, pipeline_id, id),
    CONSTRAINT pipeline_stages_position_positive CHECK (position >= 0),
    CONSTRAINT pipeline_stages_probability_range CHECK (probability >= 0 AND probability <= 100),
    CONSTRAINT pipeline_stages_terminal_type CHECK (terminal_type IN ('open', 'won', 'lost')),
    CONSTRAINT pipeline_stages_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT pipeline_stages_pipeline_fk
        FOREIGN KEY (tenant_id, pipeline_id) REFERENCES pipelines (tenant_id, id)
        ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS pipeline_stages_active_position_idx
    ON pipeline_stages (tenant_id, pipeline_id, position)
    WHERE archived_at IS NULL;

ALTER TABLE pipelines ENABLE ROW LEVEL SECURITY;
ALTER TABLE pipelines FORCE ROW LEVEL SECURITY;
ALTER TABLE pipeline_stages ENABLE ROW LEVEL SECURITY;
ALTER TABLE pipeline_stages FORCE ROW LEVEL SECURITY;

CREATE POLICY pipelines_tenant_policy ON pipelines
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

CREATE POLICY pipeline_stages_tenant_policy ON pipeline_stages
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON pipelines TO crm_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON pipeline_stages TO crm_app;

COMMIT;
