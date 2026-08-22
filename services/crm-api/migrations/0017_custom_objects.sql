-- 0017_custom_objects.sql
-- Tenant-defined objects use shared tables, never runtime DDL. This retains
-- crm_app's DML-only grant and gives every tenant the same RLS proof shape.
BEGIN;

CREATE TABLE IF NOT EXISTS custom_object_definitions (
    tenant_id text NOT NULL,
    id text NOT NULL,
    api_name text NOT NULL,
    label text NOT NULL,
    plural_label text NOT NULL,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, api_name),
    CONSTRAINT custom_object_definitions_api_name_shape CHECK (api_name ~ '^[a-z][a-z0-9_]{0,62}$'),
    CONSTRAINT custom_object_definitions_label_not_blank CHECK (length(btrim(label)) > 0),
    CONSTRAINT custom_object_definitions_plural_not_blank CHECK (length(btrim(plural_label)) > 0)
);

CREATE TABLE IF NOT EXISTS custom_records (
    tenant_id text NOT NULL,
    object_type text NOT NULL,
    id text NOT NULL,
    fields jsonb NOT NULL DEFAULT '{}',
    relations jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, object_type, id),
    CONSTRAINT custom_records_object_type_shape CHECK (object_type ~ '^[a-z][a-z0-9_]{0,62}$'),
    CONSTRAINT custom_records_fields_is_object CHECK (jsonb_typeof(fields) = 'object'),
    CONSTRAINT custom_records_relations_is_object CHECK (jsonb_typeof(relations) = 'object')
);
CREATE INDEX IF NOT EXISTS custom_records_fields_gin ON custom_records USING gin (fields jsonb_path_ops);
CREATE INDEX IF NOT EXISTS custom_records_relations_gin ON custom_records USING gin (relations jsonb_path_ops);

ALTER TABLE custom_object_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE custom_object_definitions FORCE ROW LEVEL SECURITY;
ALTER TABLE custom_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE custom_records FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS custom_object_definitions_tenant_policy ON custom_object_definitions;
CREATE POLICY custom_object_definitions_tenant_policy ON custom_object_definitions FOR ALL
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
DROP POLICY IF EXISTS custom_records_tenant_policy ON custom_records;
CREATE POLICY custom_records_tenant_policy ON custom_records FOR ALL
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON custom_object_definitions, custom_records TO crm_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
    GRANT SELECT ON custom_object_definitions, custom_records TO crm_drain;
  END IF;
END $$;
COMMIT;
