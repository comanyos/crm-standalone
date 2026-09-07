-- 0032_outbound_messages.sql
-- Messages an automation has drafted (task_crm.md §6,
-- internal/domain/outbound_message.go).
--
-- These are *drafts*, not sends. Nothing in this codebase can deliver a
-- message: provider-sent messaging is task_crm.md §4 and waits on a provider
-- contract, a cost owner and a secret story. What a rule can do today is the
-- part that needs none of those — decide who to contact, when, and with what
-- wording — and leave a rep one tap away from sending it (task_crm.md §3).
--
-- Two consequences worth recording, because both look like omissions:
--
--   *Quiet hours are absent.* task_crm.md named D6 (per-tenant sending
--   windows) as blocking this section. It blocks §4, not this: a draft
--   waiting in a queue wakes nobody, and the human who sends it does so
--   during their own working hours by construction. The moment anything
--   drains this table automatically, D6 becomes required — hence the
--   `status` column already distinguishing 'pending' from 'sent', so a
--   sender can be added without reshaping anything.
--
--   *There is no retry or scheduling state.* A draft is either waiting, sent,
--   or discarded. Retries belong to whatever eventually sends.
--
-- The body is stored, which makes this the second table (after
-- lead_capture_deliveries) to hold text written for a specific person. It is
-- tenant-scoped and RLS-enforced like everything else, and crm_drain is
-- deliberately not granted on it.
--
-- Migrations are append-only: add 0033_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS outbound_messages (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    -- What this message is about, so the UI can show it on the record and a
    -- rep knows who they are writing to. Polymorphic like activities.
    subject_type text        NOT NULL,
    subject_id   text        NOT NULL,
    channel_kind text        NOT NULL,
    -- The normalised address, resolved and consent-checked when the draft was
    -- made. Re-checked before sending, because consent can be withdrawn in
    -- between — see domain.MayMessage.
    address      text        NOT NULL,
    subject      text        NOT NULL DEFAULT '',
    body         text        NOT NULL,
    status       text        NOT NULL DEFAULT 'pending',
    -- Who this is for. The record's owner at the time the rule ran, so the
    -- queue can be filtered to "mine" without a second lookup.
    assignee_id  text        NOT NULL DEFAULT '',
    -- Provenance: which rule version drafted it, and which execution. The
    -- execution is the idempotency key — the engine re-runs an action if it
    -- crashes between performing it and persisting actions_completed, and a
    -- re-run must not draft the same message twice.
    rule_version_id text     NOT NULL DEFAULT '',
    execution_id    text     NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz,
    sent_by      text        NOT NULL DEFAULT '',
    -- A bounded reason code when a draft was discarded, never free text.
    discard_reason text      NOT NULL DEFAULT '',

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT outbound_messages_body_not_blank CHECK (length(btrim(body)) > 0),
    CONSTRAINT outbound_messages_address_not_blank CHECK (length(btrim(address)) > 0),
    CONSTRAINT outbound_messages_kind_known
        CHECK (channel_kind IN ('email', 'sms', 'whatsapp')),
    CONSTRAINT outbound_messages_subject_known
        CHECK (subject_type IN ('lead', 'contact')),
    CONSTRAINT outbound_messages_status_known
        CHECK (status IN ('pending', 'sent', 'discarded')),
    -- A sent message records when and by whom, or the queue cannot be audited
    -- and "who contacted this customer" has no answer.
    CONSTRAINT outbound_messages_sent_is_attributed
        CHECK (status <> 'sent' OR (sent_at IS NOT NULL AND length(btrim(sent_by)) > 0))
);

-- One draft per execution. The engine re-runs an action after a crash between
-- performing it and recording that it did; without this the customer gets the
-- same message twice.
CREATE UNIQUE INDEX IF NOT EXISTS outbound_messages_execution_key
    ON outbound_messages (tenant_id, execution_id)
    WHERE execution_id <> '';

-- "What is waiting for me" — the queue read, and the only one that matters
-- for latency.
CREATE INDEX IF NOT EXISTS outbound_messages_pending_idx
    ON outbound_messages (tenant_id, status, assignee_id, created_at DESC);

-- "What has been drafted about this record" — the record-page read.
CREATE INDEX IF NOT EXISTS outbound_messages_subject_idx
    ON outbound_messages (tenant_id, subject_type, subject_id, created_at DESC);

ALTER TABLE outbound_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbound_messages FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS outbound_messages_tenant_policy ON outbound_messages;
CREATE POLICY outbound_messages_tenant_policy ON outbound_messages
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON outbound_messages TO crm_app;
    END IF;
END $$;

COMMIT;
