-- 0030_message_templates.sql
-- Message templates (task_crm.md §5, internal/domain/message_template.go).
--
-- Tenant-shared rather than per-owner, unlike saved views
-- (migrations/0025_saved_views.sql). The distinction is what the thing
-- carries: a saved view is one person's working state, while a template is
-- wording the organization uses with its customers — closer to a tag, which
-- is also a shared vocabulary any member may add to. There is no is_shared
-- flag because one model is enough until somebody actually wants a private
-- draft, and a flag with one setting in practice is a filter every query has
-- to remember.
--
-- ponytail: no per-owner templates. Add an owner_id and an is_shared flag
-- when a rep genuinely needs a private draft; nothing above this changes
-- shape, only the list query gains a predicate.
--
-- Templates hold merge placeholders — {{contact.first_name}} and the rest of
-- the allowlist in message_template.go — but this schema never expands one.
-- Rendering happens in the browser, from the record already on screen, which
-- is why v1 needs none of the field-visibility model
-- docs/WORKFLOW_AUTOMATION.md names as the blocker for templated action text:
-- a user cannot leak to themselves a field they are already looking at. A
-- server-side render, for an automated send with nobody present, is a
-- different feature and inherits that blocker.
--
-- Migrations are append-only: add 0031_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS message_templates (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    name         text        NOT NULL,
    -- The channel this wording is for. A WhatsApp opener and an email are
    -- not interchangeable — one is a sentence, the other has a subject line
    -- and paragraphs — so the picker offers only what fits the channel.
    channel_kind text        NOT NULL,
    -- subject applies to email only; blank elsewhere.
    subject      text        NOT NULL DEFAULT '',
    body         text        NOT NULL,
    created_by   text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT message_templates_name_not_blank CHECK (length(btrim(name)) > 0),
    CONSTRAINT message_templates_body_not_blank CHECK (length(btrim(body)) > 0),
    CONSTRAINT message_templates_kind_known
        CHECK (channel_kind IN ('email', 'sms', 'whatsapp'))
);

-- One name per channel per tenant, case-insensitively. Scoped to the channel
-- rather than the tenant because "Intro" is a reasonable name for both a
-- WhatsApp opener and an email, and they are different templates.
CREATE UNIQUE INDEX IF NOT EXISTS message_templates_name_key
    ON message_templates (tenant_id, channel_kind, lower(name));

-- "What can I send on this channel" — the picker's only query.
CREATE INDEX IF NOT EXISTS message_templates_channel_idx
    ON message_templates (tenant_id, channel_kind, name);

ALTER TABLE message_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_templates FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS message_templates_tenant_policy ON message_templates;
CREATE POLICY message_templates_tenant_policy ON message_templates
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON message_templates TO crm_app;
    END IF;
END $$;

COMMIT;
