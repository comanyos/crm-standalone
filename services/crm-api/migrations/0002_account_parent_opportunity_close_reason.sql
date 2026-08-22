-- 0002_account_parent_opportunity_close_reason.sql
-- A0: adds the fields the domain rules will need in Track B. Schema only —
-- no constraint or state machine is enforced here.

BEGIN;

ALTER TABLE accounts      ADD COLUMN IF NOT EXISTS parent_id text;
ALTER TABLE opportunities ADD COLUMN IF NOT EXISTS close_reason text;

COMMIT;
