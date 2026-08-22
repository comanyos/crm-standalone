-- +goose Up

-- Align the persisted policy schema with the runtime policy engine. The
-- original migration stored a generic rules document, while the engine uses
-- typed expression/group/reputation columns.
ALTER TABLE policies ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'expression';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS expression_code TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS group_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE policies ADD COLUMN IF NOT EXISTS score_threshold INT NOT NULL DEFAULT 0;

ALTER TABLE policy_bindings ADD COLUMN IF NOT EXISTS binding_point TEXT NOT NULL DEFAULT '';
ALTER TABLE policy_bindings ADD COLUMN IF NOT EXISTS ord INT NOT NULL DEFAULT 0;
UPDATE policy_bindings SET binding_point = resource WHERE binding_point = '';
CREATE INDEX IF NOT EXISTS idx_policy_bindings_point ON policy_bindings(org_id, binding_point, ord);

-- IP reputation is intentionally separate from identity reputation so each
-- table has an unambiguous key and callers cannot mix identity UUIDs with IPs.
CREATE TABLE IF NOT EXISTS ip_reputation_scores (
    org_id     UUID NOT NULL,
    ip         TEXT NOT NULL,
    score      INT NOT NULL DEFAULT 100 CHECK (score BETWEEN 0 AND 100),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, ip)
);

-- +goose Down

DROP TABLE IF EXISTS ip_reputation_scores;
DROP INDEX IF EXISTS idx_policy_bindings_point;
ALTER TABLE policy_bindings DROP COLUMN IF EXISTS ord;
ALTER TABLE policy_bindings DROP COLUMN IF EXISTS binding_point;
ALTER TABLE policies DROP COLUMN IF EXISTS score_threshold;
ALTER TABLE policies DROP COLUMN IF EXISTS group_ids;
ALTER TABLE policies DROP COLUMN IF EXISTS expression_code;
ALTER TABLE policies DROP COLUMN IF EXISTS type;
