-- 0029_lead_distribution.sql
-- Round-robin owner assignment (task_crm.md §8, internal/domain/workflow.go's
-- assign_owner action).
--
-- There is no "assignment rules" table here, and that is the design decision
-- worth recording. Distributing new leads across a team is a rule that fires
-- on lead.created, evaluates conditions, and does something — which is
-- precisely what migrations/0023_workflow_automation.sql already models, with
-- versioning, an execution ledger, bounded retries and startup-checked loop
-- prevention. A second engine beside it would need all of that again, and
-- would be the one nobody remembered to give it to. So the rotation is an
-- action type, and the only state it needs of its own is: whose turn is it,
-- and what did this execution already decide.
--
-- Those are two questions with two different lifetimes, hence two tables.
--
--   workflow_assignment_cursors — one row per rule version: the position the
--       rotation has reached. Locked FOR UPDATE while an assignment is made,
--       which is what makes ten concurrent leads across three members come
--       out 4/3/3 rather than assigning several of them to whoever read the
--       cursor first.
--
--   workflow_owner_assignments — one row per execution: what that execution
--       assigned. This is the idempotency record, and it exists because of a
--       specific window in the engine: an action runs, and only then is
--       actions_completed persisted (internal/integration/workflow/engine.go).
--       A crash in between re-runs the action. For create_task that means a
--       duplicate task; for a rotation it would mean advancing the cursor
--       twice and silently skipping a member. Keyed on the execution, a
--       re-run finds its own earlier decision and repeats it instead.
--
-- Both are written in the same transaction as the owner change itself, so a
-- failure leaves the rotation exactly where it was rather than burning a turn
-- on an assignment that never happened.
--
-- Migrations are append-only: add 0030_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS workflow_assignment_cursors (
    tenant_id       text        NOT NULL,
    -- The rule *version*, not the rule. Editing a rule publishes a new
    -- version (0023), and a changed member list is a new rotation: carrying
    -- the old position across would start the new list at an arbitrary point.
    rule_version_id text        NOT NULL,
    next_position   bigint      NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, rule_version_id),
    CONSTRAINT workflow_assignment_cursors_position_not_negative
        CHECK (next_position >= 0)
);

CREATE TABLE IF NOT EXISTS workflow_owner_assignments (
    tenant_id       text        NOT NULL,
    execution_id    text        NOT NULL,
    rule_version_id text        NOT NULL,
    -- The position this execution consumed, kept so the ledger can be read as
    -- a rotation ("who was 4th?") rather than only as a list of outcomes.
    position        bigint      NOT NULL,
    member_id       text        NOT NULL,
    subject_type    text        NOT NULL,
    subject_id      text        NOT NULL,
    assigned_at     timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, execution_id),
    CONSTRAINT workflow_owner_assignments_member_not_blank
        CHECK (length(btrim(member_id)) > 0),
    -- Only the two record types that carry an owner of their own. An
    -- Opportunity's owner is its Account's, so assigning "the opportunity"
    -- would silently reassign the account and every other opportunity on it.
    CONSTRAINT workflow_owner_assignments_subject_known
        CHECK (subject_type IN ('lead', 'account'))
);

-- Two executions of one rule version must never consume the same turn. The
-- cursor's FOR UPDATE is what prevents it; this index is what proves it, by
-- failing the transaction rather than quietly double-booking a position if
-- the locking is ever weakened.
CREATE UNIQUE INDEX IF NOT EXISTS workflow_owner_assignments_position_key
    ON workflow_owner_assignments (tenant_id, rule_version_id, position);

-- "How has this rule distributed work" — the read a manager checking fairness
-- makes, and the one the tests assert the 4/3/3 split with.
CREATE INDEX IF NOT EXISTS workflow_owner_assignments_rule_idx
    ON workflow_owner_assignments (tenant_id, rule_version_id, assigned_at DESC);

ALTER TABLE workflow_assignment_cursors ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_assignment_cursors FORCE  ROW LEVEL SECURITY;
ALTER TABLE workflow_owner_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_owner_assignments FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS workflow_assignment_cursors_tenant_policy ON workflow_assignment_cursors;
CREATE POLICY workflow_assignment_cursors_tenant_policy ON workflow_assignment_cursors
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS workflow_owner_assignments_tenant_policy ON workflow_owner_assignments;
CREATE POLICY workflow_owner_assignments_tenant_policy ON workflow_owner_assignments
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON workflow_assignment_cursors, workflow_owner_assignments TO crm_app;
    END IF;
END $$;

COMMIT;
