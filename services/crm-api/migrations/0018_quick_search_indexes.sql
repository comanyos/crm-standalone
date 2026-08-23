-- 0018_quick_search_indexes.sql
-- Bounded cross-object quick search (SearchRecords) uses literal ILIKE
-- matching. pg_trgm GIN indexes keep prefix and substring matching from
-- degrading into a full-table scan as a tenant's CRM grows. No tenant data is
-- copied into an index outside its owning table, so the existing RLS policies
-- remain the authorization boundary.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS accounts_name_trgm_idx ON accounts USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS accounts_website_trgm_idx ON accounts USING gin (website gin_trgm_ops);
CREATE INDEX IF NOT EXISTS contacts_first_name_trgm_idx ON contacts USING gin (first_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS contacts_last_name_trgm_idx ON contacts USING gin (last_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS contacts_email_trgm_idx ON contacts USING gin (email gin_trgm_ops);
CREATE INDEX IF NOT EXISTS leads_company_trgm_idx ON leads USING gin (company gin_trgm_ops);
CREATE INDEX IF NOT EXISTS leads_contact_email_trgm_idx ON leads USING gin (contact_email gin_trgm_ops);
CREATE INDEX IF NOT EXISTS opportunities_name_trgm_idx ON opportunities USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS tasks_title_trgm_idx ON tasks USING gin (title gin_trgm_ops);

COMMIT;
