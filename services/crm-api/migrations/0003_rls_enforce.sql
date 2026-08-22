-- 0003_rls_enforce.sql
-- A1: Row-Level Security for crm's tables.
--
-- This module has never declared RLS before, so — like leave's 0005 — this
-- migration does ENABLE, FORCE, and CREATE POLICY together for every
-- tenant-scoped table, rather than splitting the declare/force fix into two
-- steps the way HCM had to when retrofitting an already-shipped policy.
--
-- Two reasons a policy silently enforces nothing if either is skipped:
--   1. The table owner is exempt from its own policies unless the table is
--      marked FORCE ROW LEVEL SECURITY.
--   2. A superuser, or any role with BYPASSRLS, is exempt regardless.
--
-- The suite's local stack connects as the table owner (`crm`, via
-- docker-compose's POSTGRES_USER), which is not itself a Postgres superuser,
-- but IS the owner — so FORCE is what makes these policies apply to it.

ALTER TABLE accounts      ENABLE ROW LEVEL SECURITY;
ALTER TABLE contacts      ENABLE ROW LEVEL SECURITY;
ALTER TABLE leads         ENABLE ROW LEVEL SECURITY;
ALTER TABLE opportunities ENABLE ROW LEVEL SECURITY;
ALTER TABLE activities    ENABLE ROW LEVEL SECURITY;

ALTER TABLE accounts      FORCE ROW LEVEL SECURITY;
ALTER TABLE contacts      FORCE ROW LEVEL SECURITY;
ALTER TABLE leads         FORCE ROW LEVEL SECURITY;
ALTER TABLE opportunities FORCE ROW LEVEL SECURITY;
ALTER TABLE activities    FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS accounts_tenant_policy ON accounts;
CREATE POLICY accounts_tenant_policy ON accounts
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS contacts_tenant_policy ON contacts;
CREATE POLICY contacts_tenant_policy ON contacts
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS leads_tenant_policy ON leads;
CREATE POLICY leads_tenant_policy ON leads
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS opportunities_tenant_policy ON opportunities;
CREATE POLICY opportunities_tenant_policy ON opportunities
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS activities_tenant_policy ON activities;
CREATE POLICY activities_tenant_policy ON activities
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- ---------------------------------------------------------------------------
-- crm_app — what the service connects as in a real deployment. No BYPASSRLS.
-- Every statement it runs is filtered by the policies above, so a missing
-- tenant predicate in Go returns nothing rather than another tenant's rows.
--
-- Creating this role does not require superuser privileges, only CREATEROLE
-- or being a superuser; it is a no-op ALTER on repeated application.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        CREATE ROLE crm_app LOGIN PASSWORD 'crm_app';
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO crm_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO crm_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO crm_app;

-- ---------------------------------------------------------------------------
-- crm_drain — the one role allowed to read across tenants, reserved for a
-- future outbox dispatcher (no chain in this module needs it yet, but the
-- role is created here so a later migration does not have to touch RLS
-- again). BYPASSRLS requires CREATEROLE + the ability to grant BYPASSRLS,
-- which in turn requires superuser — so, like HCM's drain role, this is only
-- created when the connecting role is itself a superuser. A non-superuser
-- deployment simply has no drain role yet, which is safe: nothing depends on
-- crm_drain existing today.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
            CREATE ROLE crm_drain LOGIN PASSWORD 'crm_drain' BYPASSRLS;
        END IF;
        GRANT USAGE ON SCHEMA public TO crm_drain;
        GRANT SELECT ON ALL TABLES IN SCHEMA public TO crm_drain;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            GRANT SELECT ON TABLES TO crm_drain;
    END IF;
END $$;
