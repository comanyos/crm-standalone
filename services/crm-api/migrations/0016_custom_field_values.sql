-- 0016_custom_field_values.sql
-- Where custom field values live (task_v2 Phase 1.1, step 2 of
-- docs/DESIGN_METADATA_LAYER.md §8).
--
-- One jsonb document per row, on the row the values belong to, keyed by the
-- field_key of a definition in 0015. This is the accepted design's
-- load-bearing decision, and the reason for it is isolation: a value written
-- here sits on a row that accounts_tenant_policy (and its siblings) already
-- cover, so the metadata layer adds no new isolation surface for values at
-- all. There is no new table, no new join, and no new policy to get right.
--
-- These seven tables are exactly the object types internal/domain's
-- customObjectTypes allows a definition to extend. Tenant-defined objects
-- (Phase 1.2) get their own shared table rather than more columns here.
--
-- This is intended to be the LAST DDL the metadata layer needs. From here a
-- tenant adds a field by INSERTing a definition, never by altering a table —
-- which is what keeps crm_app on DML grants only, and what keeps one tenant's
-- schema change from taking a lock across every other tenant.
--
-- Cost of this migration itself: on PostgreSQL 11+ ADD COLUMN with a
-- non-volatile DEFAULT is metadata-only, so no table is rewritten. The brief
-- ACCESS EXCLUSIVE lock each statement takes is a one-time cost paid here so
-- that it is never paid again at runtime.
--
-- No RLS changes: these tables already have policies, and a column inherits
-- the row's protection. pgstore_tenant_isolation_test.go keeps covering them
-- unchanged.
--
-- Migrations are append-only: add 0017_*.sql, never edit this file.

BEGIN;

ALTER TABLE accounts      ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';
ALTER TABLE contacts      ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';
ALTER TABLE leads         ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';
ALTER TABLE opportunities ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';
ALTER TABLE tasks         ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';
ALTER TABLE notes         ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';
ALTER TABLE activities    ADD COLUMN IF NOT EXISTS custom_fields jsonb NOT NULL DEFAULT '{}';

-- The document must be an object. Without this a writer could store a bare
-- array or scalar, and every reader that expects to look a field_key up in it
-- would have to defend against a shape the column promised not to hold.
-- Enforced here as well as in Go for the same reason 0015 constrains
-- field_key in both places: the constraint makes it true of the data, the Go
-- check makes it explainable to the caller.
--
-- Added through a DO block because PostgreSQL has no ADD CONSTRAINT IF NOT
-- EXISTS, and this file should survive a re-run the way the rest of this
-- tree's migrations do. The table names are literals in the array below, not
-- caller input, so the dynamic SQL takes no identifier it did not choose.
--
-- NOT VALID skips the full-table scan that would otherwise verify every
-- existing row while holding a lock; VALIDATE then performs it under SHARE
-- UPDATE EXCLUSIVE, which blocks neither reads nor writes. Every existing row
-- holds the '{}' default and cannot violate the check, so the scan finds
-- nothing — but doing it in this order means that stays true on a table with
-- real data in it.
DO $$
DECLARE
    target text;
    constraint_name text;
BEGIN
    FOREACH target IN ARRAY ARRAY[
        'accounts', 'contacts', 'leads', 'opportunities', 'tasks', 'notes', 'activities'
    ] LOOP
        constraint_name := target || '_custom_fields_is_object';
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint
             WHERE conname = constraint_name
               AND conrelid = target::regclass
        ) THEN
            EXECUTE format(
                'ALTER TABLE %I ADD CONSTRAINT %I CHECK (jsonb_typeof(custom_fields) = ''object'') NOT VALID',
                target, constraint_name);
            EXECUTE format('ALTER TABLE %I VALIDATE CONSTRAINT %I', target, constraint_name);
        END IF;
    END LOOP;
END $$;

-- jsonb_path_ops rather than the default jsonb_ops: it indexes only the
-- containment operator @>, which is precisely the filter the design specifies
-- (docs/DESIGN_METADATA_LAYER.md §7), and in exchange the index is
-- substantially smaller and faster than one that also supports key-existence
-- operators nothing here uses.
--
-- Ordering by a custom field is deliberately NOT supported by these indexes.
-- Doing it well needs either a per-definition expression index — runtime DDL,
-- which this design spends real effort avoiding — or Phase 4's aggregation
-- layer. Shipping a sort that silently degrades to a full scan on a large
-- tenant would be worse than not shipping it.
CREATE INDEX IF NOT EXISTS accounts_custom_fields_gin
    ON accounts USING gin (custom_fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS contacts_custom_fields_gin
    ON contacts USING gin (custom_fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS leads_custom_fields_gin
    ON leads USING gin (custom_fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS opportunities_custom_fields_gin
    ON opportunities USING gin (custom_fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS tasks_custom_fields_gin
    ON tasks USING gin (custom_fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS notes_custom_fields_gin
    ON notes USING gin (custom_fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS activities_custom_fields_gin
    ON activities USING gin (custom_fields jsonb_path_ops);

COMMIT;
