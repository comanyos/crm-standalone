-- 0025_saved_views.sql
-- Saved views (M4 — Data productivity, internal/domain/saved_views.go).
--
-- A saved view is private by construction: owner_id is part of every read
-- and write, the same way notifications.recipient_id is (0023). There is no
-- sharing in this version — see saved_views.go's package doc comment for
-- why — so unlike tag_definitions there is no tenant-wide uniqueness on
-- name, only per-owner: two people may each save a view named "My deals".
--
-- Migrations are append-only: add 0026_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS saved_views (
    tenant_id   text        NOT NULL,
    id          text        NOT NULL,
    owner_id    text        NOT NULL,
    object_type text        NOT NULL,
    name        text        NOT NULL,
    -- filters is a flat string map (see SavedView's doc comment for why),
    -- validated as an object by Go before it reaches here — the CHECK is a
    -- backstop against a hand-written row, not the primary defense.
    filters     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    sort        text        NOT NULL DEFAULT '',
    columns     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT saved_views_owner_not_blank CHECK (length(btrim(owner_id)) > 0),
    CONSTRAINT saved_views_object_type_known
        CHECK (object_type IN ('account', 'contact', 'lead', 'opportunity', 'task')),
    CONSTRAINT saved_views_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT saved_views_filters_is_object CHECK (jsonb_typeof(filters) = 'object'),
    CONSTRAINT saved_views_columns_is_array CHECK (jsonb_typeof(columns) = 'array')
);

-- One owner may not save two views of the same name on the same list.
CREATE UNIQUE INDEX IF NOT EXISTS saved_views_owner_name_key
    ON saved_views (tenant_id, owner_id, object_type, lower(name));

-- "This owner's views for this list" — the read the list page's saved-view
-- picker needs, ordered the way it is offered.
CREATE INDEX IF NOT EXISTS saved_views_owner_idx
    ON saved_views (tenant_id, owner_id, object_type, name);

ALTER TABLE saved_views ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_views FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS saved_views_tenant_policy ON saved_views;
CREATE POLICY saved_views_tenant_policy ON saved_views
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON saved_views TO crm_app;
    END IF;
END $$;

COMMIT;
