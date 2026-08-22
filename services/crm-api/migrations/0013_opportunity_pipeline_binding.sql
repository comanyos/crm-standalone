-- 0013_opportunity_pipeline_binding.sql
-- Bind every opportunity to a tenant-owned pipeline and stage without
-- rewriting or discarding the legacy stage history. The system Sales pipeline
-- preserves the former stage sequence for records created before configurable
-- pipelines became available.

BEGIN;

ALTER TABLE opportunities
    ADD COLUMN IF NOT EXISTS pipeline_id text,
    ADD COLUMN IF NOT EXISTS stage_id text;

-- Every existing tenant that owns an opportunity receives the compatible
-- pipeline. It is only made the default when the tenant has not already chosen
-- an active custom default pipeline.
INSERT INTO pipelines (tenant_id, id, name, is_default, created_at, updated_at)
SELECT DISTINCT tenant_id, 'system-sales', 'Sales', false, now(), now()
FROM opportunities
ON CONFLICT (tenant_id, id) DO NOTHING;

UPDATE pipelines system_pipeline
SET is_default = true, updated_at = now()
WHERE system_pipeline.id = 'system-sales'
  AND system_pipeline.archived_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM pipelines existing_default
      WHERE existing_default.tenant_id = system_pipeline.tenant_id
        AND existing_default.id <> system_pipeline.id
        AND existing_default.is_default
        AND existing_default.archived_at IS NULL
  );

INSERT INTO pipeline_stages
    (tenant_id, pipeline_id, id, name, position, probability, terminal_type, requires_close_reason, created_at, updated_at)
SELECT pipeline.tenant_id, pipeline.id, stage.id, stage.name, stage.position,
       stage.probability, stage.terminal_type, stage.requires_close_reason, now(), now()
FROM pipelines pipeline
CROSS JOIN (VALUES
    ('prospecting',   'Prospecting',   0, 10::numeric, 'open', false),
    ('qualification', 'Qualification', 1, 25::numeric, 'open', false),
    ('proposal',      'Proposal',      2, 50::numeric, 'open', false),
    ('negotiation',   'Negotiation',   3, 75::numeric, 'open', false),
    ('closed_won',    'Closed won',    4, 100::numeric, 'won',  true),
    ('closed_lost',   'Closed lost',   5, 0::numeric, 'lost', true)
) AS stage(id, name, position, probability, terminal_type, requires_close_reason)
WHERE pipeline.id = 'system-sales'
ON CONFLICT (tenant_id, pipeline_id, id) DO NOTHING;

UPDATE opportunities
SET pipeline_id = 'system-sales', stage_id = stage
WHERE pipeline_id IS NULL OR stage_id IS NULL;

ALTER TABLE opportunities
    ALTER COLUMN pipeline_id SET NOT NULL,
    ALTER COLUMN stage_id SET NOT NULL,
    ADD CONSTRAINT opportunities_pipeline_fk
        FOREIGN KEY (tenant_id, pipeline_id) REFERENCES pipelines (tenant_id, id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT opportunities_stage_fk
        FOREIGN KEY (tenant_id, pipeline_id, stage_id)
        REFERENCES pipeline_stages (tenant_id, pipeline_id, id)
        ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS opportunities_pipeline_stage_idx
    ON opportunities (tenant_id, pipeline_id, stage_id);

COMMIT;
