-- 0006_lead_lifecycle_conversion.sql
-- B1: adds the columns Lead's lifecycle state machine and conversion need.
--
-- duplicate_of_contact_id is a flag (set when contact_email exactly matches
-- an existing Contact at write time), never a block: the lead is still
-- stored either way.
--
-- converted_contact_id / converted_account_id / converted_opportunity_id are
-- populated exactly once, by ConvertLead, the instant status becomes
-- 'converted'. Empty until then.
--
-- Migrations are append-only: add 0007_*.sql, never edit this file.

BEGIN;

ALTER TABLE leads ADD COLUMN IF NOT EXISTS duplicate_of_contact_id  text NOT NULL DEFAULT '';
ALTER TABLE leads ADD COLUMN IF NOT EXISTS converted_contact_id     text NOT NULL DEFAULT '';
ALTER TABLE leads ADD COLUMN IF NOT EXISTS converted_account_id     text NOT NULL DEFAULT '';
ALTER TABLE leads ADD COLUMN IF NOT EXISTS converted_opportunity_id text NOT NULL DEFAULT '';

COMMIT;
