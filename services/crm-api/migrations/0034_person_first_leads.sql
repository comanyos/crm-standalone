-- Person-first leads.
--
-- A lead may arrive with only a name and a phone number (Meta/WhatsApp is
-- the common case), so company and email cannot be the identity contract.
-- Empty strings retain backwards compatibility with existing rows and keep
-- the Go/Protobuf model free of nullable scalar ambiguity.

ALTER TABLE leads ADD COLUMN IF NOT EXISTS first_name text NOT NULL DEFAULT '';
ALTER TABLE leads ADD COLUMN IF NOT EXISTS last_name  text NOT NULL DEFAULT '';
ALTER TABLE leads ADD COLUMN IF NOT EXISTS phone      text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS leads_tenant_email_identity_idx
    ON leads (tenant_id, lower(contact_email))
    WHERE btrim(contact_email) <> '';

CREATE INDEX IF NOT EXISTS leads_tenant_phone_identity_idx
    ON leads (tenant_id, phone)
    WHERE btrim(phone) <> '';

CREATE INDEX IF NOT EXISTS contacts_tenant_phone_identity_idx
    ON contacts (tenant_id, (regexp_replace(phone, '[^0-9+]', '', 'g')))
    WHERE btrim(phone) <> '';
