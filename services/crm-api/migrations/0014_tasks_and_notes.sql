-- 0014_tasks_and_notes.sql
-- First-class Task and Note entities (task.md §5.1).
--
-- Deliberately not more columns on `activities`. An Activity is immutable
-- once logged, which is the property that makes it worth having for audit; a
-- Task is edited until it is done and a Note is corrected as understanding
-- improves. Sharing the table would force either making activities mutable
-- or storing tasks that can never complete.
--
-- Both tables follow this schema's tenancy rules: tenant_id first in the
-- primary key, RLS enabled and FORCEd, and a policy with both USING and
-- WITH CHECK so a connection scoped to one tenant can neither read nor
-- label rows as another. pgstore_tenant_isolation_test.go discovers these
-- tables from the catalog rather than a list, so they are covered the moment
-- they exist.
--
-- Migrations are append-only: add 0015_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS tasks (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    title        text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'open',
    priority     text        NOT NULL DEFAULT 'normal',
    assignee_id  text        NOT NULL DEFAULT '',
    due_at       timestamptz,
    subject_type text        NOT NULL DEFAULT '',
    subject_id   text        NOT NULL DEFAULT '',
    completed_at timestamptz,
    completed_by text        NOT NULL DEFAULT '',
    created_by   text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT tasks_title_not_blank CHECK (length(btrim(title)) > 0),
    CONSTRAINT tasks_status_known
        CHECK (status IN ('open', 'in_progress', 'completed', 'cancelled')),
    CONSTRAINT tasks_priority_known
        CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    -- Half a subject is a dangling reference no query can resolve. Enforced
    -- here as well as in Go because it is a property of the row, not of the
    -- request that wrote it.
    CONSTRAINT tasks_subject_paired
        CHECK ((subject_type = '') = (subject_id = '')),
    -- A completed task has a completion time, and a task that is not
    -- completed has none. Without this a reopened task could keep its
    -- completed_at and report as finished in every "completed this week"
    -- query while sitting in somebody's open list.
    CONSTRAINT tasks_completion_consistent
        CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);

-- The work-list query: one assignee's outstanding tasks, soonest first.
-- NULLS LAST matches the domain's ordering — an undated task is not urgent.
CREATE INDEX IF NOT EXISTS tasks_tenant_assignee_due_idx
    ON tasks (tenant_id, assignee_id, due_at NULLS LAST)
    WHERE status IN ('open', 'in_progress');

-- The record query: everything attached to one account/contact/lead/deal.
CREATE INDEX IF NOT EXISTS tasks_tenant_subject_idx
    ON tasks (tenant_id, subject_type, subject_id, due_at NULLS LAST)
    WHERE subject_type <> '';

-- The overdue sweep, across assignees.
CREATE INDEX IF NOT EXISTS tasks_tenant_due_idx
    ON tasks (tenant_id, due_at)
    WHERE status IN ('open', 'in_progress') AND due_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS notes (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    body         text        NOT NULL,
    subject_type text        NOT NULL,
    subject_id   text        NOT NULL,
    author_id    text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    edited_at    timestamptz,

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT notes_body_not_blank CHECK (length(btrim(body)) > 0),
    -- A note is always about something, unlike a task.
    CONSTRAINT notes_subject_required
        CHECK (length(btrim(subject_type)) > 0 AND length(btrim(subject_id)) > 0),
    CONSTRAINT notes_subject_type_known
        CHECK (subject_type IN ('account', 'contact', 'lead', 'opportunity'))
);

-- The record query: one record's notes, newest first.
CREATE INDEX IF NOT EXISTS notes_tenant_subject_created_idx
    ON notes (tenant_id, subject_type, subject_id, created_at DESC);

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE tasks FORCE  ROW LEVEL SECURITY;
ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE notes FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tasks_tenant_policy ON tasks;
CREATE POLICY tasks_tenant_policy ON tasks
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS notes_tenant_policy ON notes;
CREATE POLICY notes_tenant_policy ON notes
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- 0003 granted crm_app/crm_drain on ALL TABLES existing at that time, plus
-- default privileges for tables created later by the same role. A deployment
-- whose migration runner differs from the role that ran 0003 would not have
-- picked those defaults up, so grant explicitly rather than assume.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON tasks, notes TO crm_app;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON tasks, notes TO crm_drain;
    END IF;
END $$;

COMMIT;
