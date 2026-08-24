-- 0027_import_export.sql
-- CSV import and export, accounts only in v1
-- (M4 — Data productivity, docs/CSV_IMPORT_EXPORT.md, internal/domain/import_export.go).
--
-- content on import_jobs holds the raw uploaded CSV text — the "clearly
-- bounded local/dev implementation" docs/CSV_IMPORT_EXPORT.md's "No object
-- storage" section describes in place of a production blob store. The
-- 2 MiB bound here is the same maxImportFileBytes Go enforces before the
-- content ever reaches this table; the CHECK is a backstop against a
-- hand-written row, not the primary defense.
--
-- export_jobs never stores the exported file itself, only that the export
-- happened, by whom, and how many rows it covered — the file is generated
-- fresh for one response and never persisted.
--
-- Migrations are append-only: add 0028_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS import_jobs (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    object_type  text        NOT NULL,
    status       text        NOT NULL DEFAULT 'previewed',
    file_name    text        NOT NULL,
    content      text        NOT NULL DEFAULT '',
    mapping      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    row_count    int         NOT NULL DEFAULT 0,
    created_rows int         NOT NULL DEFAULT 0,
    skipped_rows int         NOT NULL DEFAULT 0,
    errors       jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_by   text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    -- Only accounts are importable in v1 — see docs/CSV_IMPORT_EXPORT.md's
    -- "Scope" section for why the column exists anyway.
    CONSTRAINT import_jobs_object_type_known CHECK (object_type = 'account'),
    CONSTRAINT import_jobs_status_known CHECK (status IN ('previewed', 'completed')),
    CONSTRAINT import_jobs_file_name_not_blank CHECK (length(btrim(file_name)) > 0),
    CONSTRAINT import_jobs_content_bounded CHECK (length(content) <= 2097152),
    CONSTRAINT import_jobs_counts_not_negative
        CHECK (row_count >= 0 AND created_rows >= 0 AND skipped_rows >= 0),
    CONSTRAINT import_jobs_mapping_is_object CHECK (jsonb_typeof(mapping) = 'object'),
    CONSTRAINT import_jobs_errors_is_array CHECK (jsonb_typeof(errors) = 'array')
);

-- "This tenant's import history for this list" — newest first, the shape
-- ListImportJobs reads in.
CREATE INDEX IF NOT EXISTS import_jobs_history_idx
    ON import_jobs (tenant_id, object_type, created_at DESC);

CREATE TABLE IF NOT EXISTS export_jobs (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    object_type  text        NOT NULL,
    row_count    int         NOT NULL DEFAULT 0,
    requested_by text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT export_jobs_object_type_known CHECK (object_type = 'account'),
    CONSTRAINT export_jobs_row_count_not_negative CHECK (row_count >= 0)
);

CREATE INDEX IF NOT EXISTS export_jobs_history_idx
    ON export_jobs (tenant_id, object_type, created_at DESC);

ALTER TABLE import_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_jobs FORCE  ROW LEVEL SECURITY;
ALTER TABLE export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE export_jobs FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS import_jobs_tenant_policy ON import_jobs;
CREATE POLICY import_jobs_tenant_policy ON import_jobs
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS export_jobs_tenant_policy ON export_jobs;
CREATE POLICY export_jobs_tenant_policy ON export_jobs
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON import_jobs, export_jobs TO crm_app;
    END IF;
END $$;

COMMIT;
