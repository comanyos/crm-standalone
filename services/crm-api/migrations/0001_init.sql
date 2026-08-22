-- crm — initial schema.
--
-- tenant_id leads every primary key, so a query that forgets the tenant cannot
-- accidentally use the index and every row is reachable only within its tenant.
-- Migrations are append-only: add 0002_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS accounts (
    tenant_id  text NOT NULL,
    id         text NOT NULL,
    name text,
    industry text,
    tier text,
    owner_id text,
    website text,
    annual_revenue double precision,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS accounts_tenant_created_idx
    ON accounts (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS contacts (
    tenant_id  text NOT NULL,
    id         text NOT NULL,
    email text,
    account_id text,
    first_name text,
    last_name text,
    phone text,
    title text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS contacts_tenant_created_idx
    ON contacts (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS leads (
    tenant_id  text NOT NULL,
    id         text NOT NULL,
    company text,
    source text,
    contact_email text,
    status text,
    score bigint,
    owner_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS leads_tenant_created_idx
    ON leads (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS opportunities (
    tenant_id  text NOT NULL,
    id         text NOT NULL,
    name text,
    account_id text,
    stage text,
    amount double precision,
    probability double precision,
    close_date timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS opportunities_tenant_created_idx
    ON opportunities (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS activities (
    tenant_id  text NOT NULL,
    id         text NOT NULL,
    kind text,
    subject_type text,
    subject_id text,
    notes text,
    actor_id text,
    occurred_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS activities_tenant_created_idx
    ON activities (tenant_id, created_at DESC);

COMMIT;
