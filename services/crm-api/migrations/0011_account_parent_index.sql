-- 0011_account_parent_index.sql
-- Phase 3.2: backs ListAccounts' new parent_id filter (internal/domain/
-- query.go's AccountFilter.ParentID), which the web app's account detail
-- page uses for its "child accounts" hierarchy view.
--
-- Migrations are append-only: add 0012_*.sql, never edit this file.

BEGIN;

CREATE INDEX IF NOT EXISTS accounts_tenant_parent_idx
    ON accounts (tenant_id, parent_id) WHERE parent_id IS NOT NULL AND parent_id <> '';

COMMIT;
