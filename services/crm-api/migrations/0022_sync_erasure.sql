-- 0022_sync_erasure.sql
-- Erasing synced communication content, and not collecting it again
-- (docs/GOOGLE_INTEGRATION.md).
--
-- Gmail and Calendar sync put a person's correspondence and meetings into
-- this CRM. Until now there was no way to take it back out, which meant a
-- data-subject erasure request could not be served at all. This migration is
-- what makes that possible, and it has to solve two problems rather than one.
--
-- 1. DELETING IS NOT ENOUGH BY ITSELF.
--
--    gmail_messages and calendar_events are the deduplication memory: they
--    are what stops a re-sync posting a second copy of everything. Deleting
--    those rows to erase somebody would make the next poll — a minute later —
--    re-create every Activity it had just removed. So an erased row is kept
--    as a REDACTED TOMBSTONE: the provider's opaque message or event id
--    survives, because that is what dedup needs, and every human-readable
--    field is blanked. erased_at records that this happened.
--
--    A provider message id is not personal data in any useful sense; a
--    subject line, a meeting title and a sender address are. Keeping the
--    first to suppress the second is the whole trick.
--
-- 2. ERASURE THAT DOES NOT STOP COLLECTION IS THEATRE.
--
--    Tombstones only cover what has already been seen. Without something
--    more, the next email that person sends is indexed as if nothing had been
--    asked. sync_suppressions is that something: an address sync must never
--    match again. It is checked in the matcher, before anything is written.
--
-- Migrations are append-only: add 0023_*.sql, never edit this file.

BEGIN;

-- erased_at marks a redacted tombstone. NULL is an ordinary live row.
ALTER TABLE gmail_messages   ADD COLUMN IF NOT EXISTS erased_at timestamptz;
ALTER TABLE calendar_events  ADD COLUMN IF NOT EXISTS erased_at timestamptz;

-- The retention sweep and the erasure audit both ask "what was erased, and
-- what is old enough to be", so both directions are worth indexing.
CREATE INDEX IF NOT EXISTS gmail_messages_retention_idx
    ON gmail_messages (tenant_id, owner_id, occurred_at)
    WHERE erased_at IS NULL;
CREATE INDEX IF NOT EXISTS calendar_events_retention_idx
    ON calendar_events (tenant_id, owner_id, starts_at)
    WHERE erased_at IS NULL;

-- Addresses this tenant's synchronisation must never match again.
--
-- Deliberately keyed on the address rather than on a Contact: the point of an
-- erasure request is that it survives the record being deleted and recreated,
-- and a person who asks not to be tracked has asked about themselves, not
-- about a row that happens to name them today.
CREATE TABLE IF NOT EXISTS sync_suppressions (
    tenant_id  text        NOT NULL,
    -- address is stored lowercased and trimmed; the matcher normalizes the
    -- same way. Two normalizations that disagreed by a capital letter would
    -- make suppression silently stop working, which is the worst way for this
    -- table to fail.
    address    text        NOT NULL,
    -- reason is an operator note, not free-form personal data. It answers
    -- "why is this address here" months later.
    reason     text        NOT NULL DEFAULT '',
    created_by text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, address),
    CONSTRAINT sync_suppressions_address_normalized
        CHECK (address = lower(btrim(address)) AND length(address) > 0),
    CONSTRAINT sync_suppressions_reason_bounded
        CHECK (length(reason) <= 200)
);

ALTER TABLE sync_suppressions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sync_suppressions FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS sync_suppressions_tenant_policy ON sync_suppressions;
CREATE POLICY sync_suppressions_tenant_policy ON sync_suppressions
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON sync_suppressions TO crm_app;
    END IF;
    -- crm_drain is granted nothing here. A suppression list names people who
    -- asked not to be tracked; nothing that reads across tenants needs it.
END $$;

COMMIT;
