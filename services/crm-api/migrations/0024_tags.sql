-- 0024_tags.sql
-- Tags (M4 — Data productivity, internal/domain/tags.go).
--
-- A tag is a tenant-authored label; an assignment attaches one to one
-- record. There is no soft-delete state for a tag the way pipelines and
-- custom fields have one: a tag carries no history anything depends on, so
-- deleting a tag removes its assignments too (ON DELETE CASCADE) rather than
-- leaving orphaned rows a filter would need to defend against.
--
-- object_type is an explicit allowlist (tag_assignments_object_type_known)
-- matching internal/domain/tags.go's TaggableObjectTypes, so a row naming a
-- type nothing can resolve is rejected at write time rather than accepted
-- and never found again.
--
-- Migrations are append-only: add 0025_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS tag_definitions (
    tenant_id  text        NOT NULL,
    id         text        NOT NULL,
    name       text        NOT NULL,
    color      text        NOT NULL DEFAULT '',
    created_by text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT tag_definitions_name_not_blank CHECK (length(btrim(name)) > 0)
);

-- A tenant may not define the same name twice, case-insensitively — matched
-- by the Go store's own pre-check (memstore_tags.go / pgstore_tags.go) so
-- both stores reject a collision the same way rather than one racing to a
-- database error the other cannot reproduce.
CREATE UNIQUE INDEX IF NOT EXISTS tag_definitions_name_key
    ON tag_definitions (tenant_id, lower(name));

CREATE TABLE IF NOT EXISTS tag_assignments (
    tenant_id   text        NOT NULL,
    id          text        NOT NULL,
    tag_id      text        NOT NULL,
    object_type text        NOT NULL,
    object_id   text        NOT NULL,
    created_by  text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT tag_assignments_object_type_known
        CHECK (object_type IN ('account', 'contact', 'lead', 'opportunity', 'task')),
    CONSTRAINT tag_assignments_object_id_not_blank CHECK (length(btrim(object_id)) > 0),
    FOREIGN KEY (tenant_id, tag_id) REFERENCES tag_definitions (tenant_id, id)
        ON DELETE CASCADE
);

-- One tag may not be assigned to the same record twice — the row AssignTag's
-- ON CONFLICT DO NOTHING targets to make re-assigning idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS tag_assignments_unique
    ON tag_assignments (tenant_id, tag_id, object_type, object_id);

-- "What tags does this record carry" — the read every detail page needs.
CREATE INDEX IF NOT EXISTS tag_assignments_object_idx
    ON tag_assignments (tenant_id, object_type, object_id);

-- "What does this tag point at" — ListTaggedRecords' filter/paging query.
CREATE INDEX IF NOT EXISTS tag_assignments_tag_idx
    ON tag_assignments (tenant_id, tag_id, created_at DESC, id);

ALTER TABLE tag_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE tag_definitions FORCE  ROW LEVEL SECURITY;
ALTER TABLE tag_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE tag_assignments FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tag_definitions_tenant_policy ON tag_definitions;
CREATE POLICY tag_definitions_tenant_policy ON tag_definitions
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS tag_assignments_tenant_policy ON tag_assignments;
CREATE POLICY tag_assignments_tenant_policy ON tag_assignments
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON tag_definitions, tag_assignments TO crm_app;
    END IF;
END $$;

COMMIT;
