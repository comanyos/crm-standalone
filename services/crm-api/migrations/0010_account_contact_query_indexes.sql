-- 0010_account_contact_query_indexes.sql
-- Phase 3.1: indexes backing ListAccounts/ListContacts' new filters and sort
-- orders (internal/domain/query.go), and a fix for FindAccountByDomain,
-- which previously scanned every account row in the tenant on every
-- Create/UpdateAccount (internal/domain/account_hierarchy.go).
--
-- website_domain mirrors websiteDomain(website) (internal/domain/
-- lead_lifecycle.go) at write time (pgstore.go's Create/UpdateAccount) so
-- FindAccountByDomain can do a single indexed lookup instead of reading and
-- re-normalizing every row. It is write-only from Go's point of view —
-- accountColumns deliberately does not read it back into an Account, since
-- Website is the single source of truth for what it should contain.
--
-- Migrations are append-only: add 0011_*.sql, never edit this file.

BEGIN;

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS website_domain text NOT NULL DEFAULT '';

-- Best-effort backfill approximating internal/domain/lead_lifecycle.go's
-- websiteDomain() for any account created before this column existed. Any
-- row this doesn't normalize exactly right corrects itself the next time
-- that account is written, since every Create/UpdateAccount recomputes it.
UPDATE accounts
   SET website_domain = lower(regexp_replace(regexp_replace(regexp_replace(trim(website),
         '^https?://', ''), '^www\.', ''), '[/?#].*$', ''))
 WHERE website <> '' AND website_domain = '';

CREATE INDEX IF NOT EXISTS accounts_tenant_website_domain_idx
    ON accounts (tenant_id, website_domain) WHERE website_domain <> '';

-- ListAccounts' default sort (query.go's SortAccountName).
CREATE INDEX IF NOT EXISTS accounts_tenant_name_idx ON accounts (tenant_id, name);

-- ListAccounts' tier/owner_id filters.
CREATE INDEX IF NOT EXISTS accounts_tenant_tier_idx ON accounts (tenant_id, tier) WHERE tier <> '';
CREATE INDEX IF NOT EXISTS accounts_tenant_owner_idx ON accounts (tenant_id, owner_id);

-- FindContactByEmail (dedup flagging, lead conversion) scanned every
-- contact row in the tenant for a case-insensitive match with no supporting
-- index; ListContacts' account_id filter (the "contacts of this account"
-- query an account detail page needs) had none either.
CREATE INDEX IF NOT EXISTS contacts_tenant_email_lower_idx ON contacts (tenant_id, lower(email));
CREATE INDEX IF NOT EXISTS contacts_tenant_account_idx ON contacts (tenant_id, account_id);

-- accounts_tenant_created_idx (0001) and contacts_tenant_created_idx (0001)
-- already back each entity's "-created_at" sort — no new index needed there.

COMMIT;
