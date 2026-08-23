-- 0023_workflow_automation.sql
-- Workflow automation, v1 (docs/WORKFLOW_AUTOMATION.md).
--
-- The design contract this implements says why automation waited: enabling
-- rules without a durable execution ledger, bounded retries and an
-- idempotency model would make duplicate outbox delivery visible as duplicate
-- customer work. Somebody gets two follow-up calls, or the same task twice.
-- These tables are that ledger.
--
-- THE IDEMPOTENCY KEY. Outbox delivery is at-least-once, so the same event
-- reaches the engine more than once as a matter of course, not as a fault.
-- workflow_executions is therefore unique on
-- (tenant_id, rule_version_id, source_event_id): the second delivery of an
-- event loses the insert and does nothing. That constraint is the whole
-- safety property, which is why it is a unique index rather than a check in
-- Go — the database is the only place two racing runners both consult.
--
-- WHY VERSIONS ARE IMMUTABLE. An execution records which version it ran, and
-- editing an active rule creates a new version rather than mutating the old
-- one. Without that, the ledger could not answer "why did this fire" months
-- later: the rule would read as it does today, not as it did then.
--
-- LOOP PREVENTION BY CONSTRUCTION. v1 rules trigger on record events, and
-- actions produce tasks, activities and notifications. Those two sets are
-- disjoint, so no rule can trigger on its own output. It is asserted at
-- startup (assertTriggersCannotLoop) rather than trusted, because the
-- constraint is easy to break by adding one event type.
--
-- Migrations are append-only: add 0024_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS workflow_rules (
    tenant_id         text        NOT NULL,
    id                text        NOT NULL,
    name              text        NOT NULL,
    description       text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'draft',
    -- active_version_id is the version executions use. It is null for a rule
    -- that has never been published, and retained when a rule is paused so
    -- resuming does not need the author to re-pick a version.
    active_version_id text,
    created_by        text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT workflow_rules_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT workflow_rules_status_known
        CHECK (status IN ('draft', 'active', 'paused', 'archived')),
    -- An active rule with no version would match events and then have nothing
    -- to run, which reads to an author as "my rule is broken" with no clue.
    CONSTRAINT workflow_rules_active_has_a_version
        CHECK (status <> 'active' OR active_version_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS workflow_rules_active_idx
    ON workflow_rules (tenant_id, status)
    WHERE status = 'active';

-- Immutable once written. There is deliberately no updated_at: a row here is
-- a historical fact about what a rule said, and an execution points at one.
CREATE TABLE IF NOT EXISTS workflow_rule_versions (
    tenant_id           text        NOT NULL,
    id                  text        NOT NULL,
    rule_id             text        NOT NULL,
    version             int         NOT NULL,

    trigger_event_type  text        NOT NULL,
    trigger_object_type text        NOT NULL,
    -- conditions is an AND-only list of typed comparisons over allowlisted
    -- fields, and actions is an ordered list. Both are validated in Go
    -- against an allowlist a CHECK cannot express; the constraints here only
    -- guarantee the shape, so a hand-written row cannot be a bare object.
    conditions          jsonb       NOT NULL DEFAULT '[]'::jsonb,
    actions             jsonb       NOT NULL,
    created_by          text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT workflow_rule_versions_version_positive CHECK (version >= 1),
    CONSTRAINT workflow_rule_versions_conditions_is_array
        CHECK (jsonb_typeof(conditions) = 'array'),
    CONSTRAINT workflow_rule_versions_actions_is_array
        CHECK (jsonb_typeof(actions) = 'array'),
    -- A version with no actions would consume events and do nothing, which is
    -- indistinguishable from a broken trigger when an author debugs it.
    CONSTRAINT workflow_rule_versions_has_actions
        CHECK (jsonb_array_length(actions) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS workflow_rule_versions_number_key
    ON workflow_rule_versions (tenant_id, rule_id, version);

CREATE INDEX IF NOT EXISTS workflow_rule_versions_rule_idx
    ON workflow_rule_versions (tenant_id, rule_id, version DESC);

CREATE TABLE IF NOT EXISTS workflow_executions (
    tenant_id       text        NOT NULL,
    id              text        NOT NULL,
    rule_id         text        NOT NULL,
    rule_version_id text        NOT NULL,
    -- source_event_id is the outbox envelope id. Together with the version it
    -- is the idempotency key; see the header.
    source_event_id text        NOT NULL,
    source_event_type text      NOT NULL DEFAULT '',
    -- subject_type/subject_id name the record the event was about, so an
    -- author can ask "what did automation do to this deal".
    subject_type    text        NOT NULL DEFAULT '',
    subject_id      text        NOT NULL DEFAULT '',

    status          text        NOT NULL DEFAULT 'pending',
    attempts        int         NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    -- error_category is a bounded code, never a message. An execution row is
    -- read by rule authors, and a raw error would leak whatever the failing
    -- action was holding.
    error_category  text        NOT NULL DEFAULT '',
    -- actions_completed lets a retry resume rather than repeat: an execution
    -- that created the task and then failed to notify must not create a
    -- second task on its next attempt.
    actions_completed int       NOT NULL DEFAULT 0,

    started_at      timestamptz,
    finished_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT workflow_executions_status_known
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'skipped')),
    CONSTRAINT workflow_executions_attempts_not_negative
        CHECK (attempts >= 0 AND actions_completed >= 0),
    CONSTRAINT workflow_executions_terminal_not_scheduled
        CHECK (status IN ('pending', 'running') OR next_attempt_at IS NULL)
);

-- The safety property. One execution per event per rule version, enforced
-- where two racing runners both look.
CREATE UNIQUE INDEX IF NOT EXISTS workflow_executions_idempotency_key
    ON workflow_executions (tenant_id, rule_version_id, source_event_id);

-- The runner's claim query: what is due, oldest first.
CREATE INDEX IF NOT EXISTS workflow_executions_due_idx
    ON workflow_executions (tenant_id, next_attempt_at, created_at)
    WHERE status IN ('pending', 'running');

-- "What has automation done to this record", and "what has this rule done".
CREATE INDEX IF NOT EXISTS workflow_executions_subject_idx
    ON workflow_executions (tenant_id, subject_type, subject_id, created_at DESC);
CREATE INDEX IF NOT EXISTS workflow_executions_rule_idx
    ON workflow_executions (tenant_id, rule_id, created_at DESC);

CREATE TABLE IF NOT EXISTS notifications (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    -- recipient_id is a CRM principal. Notifications are the one entity here
    -- an ordinary user reads, and they read only their own.
    recipient_id text        NOT NULL,
    kind         text        NOT NULL DEFAULT 'workflow',
    title        text        NOT NULL,
    body         text        NOT NULL DEFAULT '',
    subject_type text        NOT NULL DEFAULT '',
    subject_id   text        NOT NULL DEFAULT '',
    -- rule_id records what produced this, so a user can ask why they were
    -- told and an administrator can find the rule that is too noisy.
    rule_id      text        NOT NULL DEFAULT '',
    read_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT notifications_recipient_not_blank
        CHECK (length(btrim(recipient_id)) > 0),
    CONSTRAINT notifications_title_not_blank
        CHECK (length(btrim(title)) > 0),
    CONSTRAINT notifications_subject_paired
        CHECK ((subject_type = '') = (subject_id = ''))
);

-- One user's unread list, newest first.
CREATE INDEX IF NOT EXISTS notifications_recipient_idx
    ON notifications (tenant_id, recipient_id, created_at DESC);
CREATE INDEX IF NOT EXISTS notifications_unread_idx
    ON notifications (tenant_id, recipient_id, created_at DESC)
    WHERE read_at IS NULL;

-- Where the engine has read up to in each tenant's outbox.
--
-- A fifth table the design contract does not name, because it is machinery
-- rather than a concept: the engine consumes the same durable outbox the bus
-- dispatcher does, and two consumers cannot share one position. Scanning
-- without a cursor would work — the idempotency key makes reprocessing
-- harmless — but the scan would grow without bound as a tenant's history did.
CREATE TABLE IF NOT EXISTS workflow_cursors (
    tenant_id             text        NOT NULL,
    -- The outbox is ordered by (created_at, id); both are kept so the
    -- position is exact rather than approximate at a timestamp boundary.
    last_event_created_at timestamptz,
    last_event_id         text        NOT NULL DEFAULT '',
    -- A lease, expiring rather than released, so a runner that dies mid-batch
    -- cannot wedge a tenant's automation.
    running_until         timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id)
);

CREATE INDEX IF NOT EXISTS workflow_cursors_due_idx
    ON workflow_cursors (running_until NULLS FIRST, updated_at NULLS FIRST);

ALTER TABLE workflow_rules          ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_rules          FORCE  ROW LEVEL SECURITY;
ALTER TABLE workflow_rule_versions  ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_rule_versions  FORCE  ROW LEVEL SECURITY;
ALTER TABLE workflow_executions     ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_executions     FORCE  ROW LEVEL SECURITY;
ALTER TABLE notifications           ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications           FORCE  ROW LEVEL SECURITY;
ALTER TABLE workflow_cursors        ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_cursors        FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS workflow_rules_tenant_policy ON workflow_rules;
CREATE POLICY workflow_rules_tenant_policy ON workflow_rules
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS workflow_rule_versions_tenant_policy ON workflow_rule_versions;
CREATE POLICY workflow_rule_versions_tenant_policy ON workflow_rule_versions
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS workflow_executions_tenant_policy ON workflow_executions;
CREATE POLICY workflow_executions_tenant_policy ON workflow_executions
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS notifications_tenant_policy ON notifications;
CREATE POLICY notifications_tenant_policy ON notifications
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS workflow_cursors_tenant_policy ON workflow_cursors;
CREATE POLICY workflow_cursors_tenant_policy ON workflow_cursors
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON workflow_rules, workflow_rule_versions,
            workflow_executions, notifications, workflow_cursors TO crm_app;
    END IF;
    -- crm_drain reads workflow_cursors across tenants for one query: which
    -- tenants have automation waiting. Identifiers only, and deliberately not
    -- granted on the rules, executions or notifications.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON workflow_cursors TO crm_drain;
    END IF;
END $$;

COMMIT;
