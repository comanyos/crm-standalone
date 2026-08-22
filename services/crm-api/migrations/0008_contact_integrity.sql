-- 0008_contact_integrity.sql
-- B4: Contact referential integrity, role, and dedup flagging.
--
-- role is a plain validated field (internal/domain/contact_integrity.go):
-- one of billing, technical, decision_maker, other, or empty ("not yet
-- known"). No workflow attached to it.
--
-- duplicate_of_contact_id is a flag (set when email exactly matches another
-- tenant-scoped Contact's email at write time), never a block — mirrors
-- leads.duplicate_of_contact_id (0006) and accounts.duplicate_of_account_id
-- (0007).
--
-- contacts.account_id itself needs no schema change here — 0001 already
-- added the column; this migration only backs the referential-integrity
-- invariant Go now enforces on top of it (contact_integrity.go).
--
-- Migrations are append-only: add 0009_*.sql, never edit this file.

BEGIN;

ALTER TABLE contacts ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT '';
ALTER TABLE contacts ADD COLUMN IF NOT EXISTS duplicate_of_contact_id text NOT NULL DEFAULT '';

COMMIT;
