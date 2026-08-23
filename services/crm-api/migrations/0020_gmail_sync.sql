-- 0020_gmail_sync.sql
-- Gmail synchronisation: the cursor that makes sync incremental, and the
-- index that makes it idempotent (docs/GOOGLE_INTEGRATION.md).
--
-- The load-bearing decision this schema encodes is what is NOT here.
--
-- A user's mailbox holds their entire correspondence, most of which has
-- nothing to do with this CRM and much of which is private. Anything written
-- into these tables becomes readable by every colleague holding
-- crm:activity:read. So a message is recorded only when at least one of its
-- participants matches a Contact in the same tenant, and even then only its
-- envelope is kept: participants, subject line, direction and timestamps.
--
-- There is deliberately no body column, and no snippet column. The sync
-- fetches Gmail messages with format=metadata, so the body is never even
-- requested — a column added here later would have nothing to put in it until
-- someone also widens the fetch, which is the point. Retention policy for
-- message content is still an open product decision (docs/ROADMAP.md M5), and
-- an absent column is the only version of "we have not decided yet" that
-- cannot leak.
--
-- Migrations are append-only: add 0021_*.sql, never edit this file.

BEGIN;

-- One row per connected mailbox. Separate from google_connections because
-- the two answer different questions and fail independently: a connection is
-- about authorization, this is about how far data movement has got. A
-- credential can be healthy while sync is stuck, and vice versa.
CREATE TABLE IF NOT EXISTS gmail_sync_state (
    tenant_id          text        NOT NULL,
    owner_id           text        NOT NULL,

    -- last_history_id is Gmail's own incremental cursor. Google expires
    -- history older than roughly a week; when it does, users.history.list
    -- answers 404 and the only correct response is to start a fresh backfill,
    -- which is why backfill_complete can go back to false.
    last_history_id    text        NOT NULL DEFAULT '',
    -- backfill_page_token resumes an interrupted first pass. Without it a
    -- mailbox large enough to exceed one sync window would restart from the
    -- beginning every time and never finish.
    backfill_page_token text       NOT NULL DEFAULT '',
    backfill_complete  boolean     NOT NULL DEFAULT false,

    -- syncing_until is a lease, not a lock: a replica that dies mid-pass must
    -- not leave a mailbox unsyncable forever, so the claim expires rather
    -- than needing release.
    syncing_until      timestamptz,

    last_synced_at     timestamptz,
    -- last_message_at is the occurred_at of the newest message indexed. It is
    -- what the UI shows as "up to date as of", which is a more honest claim
    -- than last_synced_at: a sync that ran and found nothing is not the same
    -- as a sync that is current.
    last_message_at    timestamptz,
    messages_indexed   bigint      NOT NULL DEFAULT 0,
    activities_created bigint      NOT NULL DEFAULT 0,
    -- last_error is a bounded reason code, never a provider response body.
    last_error         text        NOT NULL DEFAULT '',

    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, owner_id),
    CONSTRAINT gmail_sync_state_owner_not_blank
        CHECK (length(btrim(owner_id)) > 0),
    CONSTRAINT gmail_sync_state_counters_not_negative
        CHECK (messages_indexed >= 0 AND activities_created >= 0)
);

-- The sweep the background poller runs: which mailboxes are unclaimed and
-- least recently synced. Both columns are in the index rather than a partial
-- predicate on now(), which PostgreSQL rejects as non-IMMUTABLE — and rightly:
-- an index whose membership changed with the clock could not be maintained.
-- NULLS FIRST on both puts the never-claimed and never-synced rows first,
-- which is the order the poller wants anyway.
CREATE INDEX IF NOT EXISTS gmail_sync_state_due_idx
    ON gmail_sync_state (syncing_until NULLS FIRST, last_synced_at NULLS FIRST);

-- One row per timeline entry, not per message.
--
-- An email to three Contacts belongs on three timelines, so it produces three
-- Activities and three rows here. Keying on the subject as well as the
-- message id is what makes each of those entries individually idempotent:
-- re-syncing the same message cannot double-post to a timeline, and a
-- contact matched for the first time on a later pass still gets its entry.
CREATE TABLE IF NOT EXISTS gmail_messages (
    tenant_id           text        NOT NULL,
    owner_id            text        NOT NULL,
    provider_message_id text        NOT NULL,
    subject_type        text        NOT NULL,
    subject_id          text        NOT NULL,

    -- thread_id groups a conversation. Nothing reads it yet; it is stored
    -- because it is free at sync time and impossible to backfill later
    -- without re-fetching every message.
    thread_id           text        NOT NULL DEFAULT '',
    activity_id         text        NOT NULL,
    -- message_subject is the Subject header. It is the only part of a message
    -- kept verbatim, because a timeline entry that does not say what the
    -- email was about is not worth having.
    message_subject     text        NOT NULL DEFAULT '',
    from_address        text        NOT NULL DEFAULT '',
    direction           text        NOT NULL DEFAULT 'inbound',
    occurred_at         timestamptz NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, owner_id, provider_message_id, subject_type, subject_id),
    CONSTRAINT gmail_messages_direction_known
        CHECK (direction IN ('inbound', 'outbound')),
    CONSTRAINT gmail_messages_subject_type_known
        CHECK (subject_type IN ('account', 'contact', 'lead', 'opportunity')),
    CONSTRAINT gmail_messages_ids_not_blank
        CHECK (length(btrim(owner_id)) > 0
           AND length(btrim(provider_message_id)) > 0
           AND length(btrim(subject_id)) > 0
           AND length(btrim(activity_id)) > 0)
);

-- "What has this record been emailed about", newest first.
CREATE INDEX IF NOT EXISTS gmail_messages_subject_idx
    ON gmail_messages (tenant_id, subject_type, subject_id, occurred_at DESC);

-- The purge path: everything one mailbox ever indexed.
CREATE INDEX IF NOT EXISTS gmail_messages_owner_idx
    ON gmail_messages (tenant_id, owner_id);

ALTER TABLE gmail_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE gmail_sync_state FORCE  ROW LEVEL SECURITY;
ALTER TABLE gmail_messages   ENABLE ROW LEVEL SECURITY;
ALTER TABLE gmail_messages   FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS gmail_sync_state_tenant_policy ON gmail_sync_state;
CREATE POLICY gmail_sync_state_tenant_policy ON gmail_sync_state
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS gmail_messages_tenant_policy ON gmail_messages;
CREATE POLICY gmail_messages_tenant_policy ON gmail_messages
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON gmail_sync_state, gmail_messages TO crm_app;
    END IF;
    -- crm_drain reads gmail_sync_state across tenants for one query: which
    -- mailboxes are due for a sync pass, which has no tenant to bind. It
    -- returns identifiers only, and every read and write that follows uses a
    -- real Scope on the application pool. It is deliberately NOT granted on
    -- gmail_messages, which holds correspondence metadata.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON gmail_sync_state TO crm_drain;
    END IF;
END $$;

COMMIT;
