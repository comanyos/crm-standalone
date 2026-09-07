-- 0028_messaging_consent.sql
-- The messaging consent ledger (task_crm.md §2, internal/domain/messaging.go).
--
-- One table, not two. task_crm.md §2 sketched a `contact_channels` table
-- beside this one, holding each address a contact can be reached at. It is
-- not here, because a Contact already carries exactly one Email and one
-- Phone: the channels are *derived* from those fields
-- (domain.ContactChannels) rather than stored a second time. A channel table
-- would need a migration of existing contacts, a management UI, and a rule
-- for which of several addresses is "the" one — none of which anybody has
-- asked for, and all of which can be added later without changing the shape
-- of this table or the signature of the gate that reads it.
--
-- ponytail: channels derived from Contact.Email/Contact.Phone, so a contact
-- has at most one address per kind. Promote to a contact_channels table when
-- a second address per kind is actually needed; MayMessage takes a
-- MessagingChannel already, so nothing above it changes.
--
-- Why the ledger is append-only rather than a `consent` column on the
-- contact: "when did they opt out, and what were we told" must be answerable
-- after the fact — during a complaint, an audit, or a regulator's question.
-- A mutable flag answers none of those. Current state is the newest row for
-- an address; there is no UPDATE path in internal/domain at all, which is
-- what makes "append-only" a property rather than a convention.
--
-- Consent is keyed on (channel kind, address) rather than on a contact id on
-- purpose. A person who withdraws consent has withdrawn it for that address,
-- and must stay withdrawn if the same address is later added to a second
-- contact record — which is exactly what happens when a duplicate is created
-- or an import re-adds someone. Keying on the contact would let a re-import
-- silently resurrect permission to message somebody who said no.
--
-- Migrations are append-only: add 0029_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS messaging_consent (
    tenant_id    text        NOT NULL,
    id           text        NOT NULL,
    -- The channel this decision covers. An address can be permitted on one
    -- channel and refused on another: "email me but do not text me" is an
    -- ordinary thing for a person to want.
    channel_kind text        NOT NULL,
    -- Normalised by domain.NormalizeChannelAddress before it reaches here:
    -- lower-cased for email, punctuation stripped for phone numbers, so
    -- "+1 (555) 010-0000" and "+15550100000" are one address rather than two
    -- with different permissions.
    address      text        NOT NULL,
    state        text        NOT NULL,
    -- Where the decision came from (import, manual, capture endpoint, an
    -- inbound STOP keyword). A bounded vocabulary, not free text — see
    -- domain.ConsentSources.
    source       text        NOT NULL DEFAULT '',
    -- What we were shown. A reference — a form submission id, a message id —
    -- never the message content itself.
    evidence     text        NOT NULL DEFAULT '',
    recorded_by  text        NOT NULL DEFAULT '',
    recorded_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    CONSTRAINT messaging_consent_kind_known
        CHECK (channel_kind IN ('email', 'sms', 'whatsapp', 'voice')),
    CONSTRAINT messaging_consent_state_known
        CHECK (state IN ('granted', 'withdrawn')),
    CONSTRAINT messaging_consent_address_not_blank
        CHECK (length(btrim(address)) > 0)
);

-- The only read this table has: "what is the current state for this address
-- on this channel". Ordered so the newest row for a key is the first one the
-- index yields, because that single row is the answer.
--
-- CurrentConsent's ORDER BY carries one more term than this index does —
-- (state = 'withdrawn') DESC, between recorded_at and id. Stamps are
-- truncated to the microsecond timestamptz keeps, so two decisions recorded
-- in the same instant tie, and ids are random: a tie broken on id alone picks
-- a winner at random, which for consent means a grant can beat a same-instant
-- withdrawal and somebody who opted out gets messaged. The extra term makes
-- the more restrictive decision win a tie. It costs nothing measurable: the
-- index still resolves the address and the ordering, and the set that
-- actually ties is one or two rows.
CREATE INDEX IF NOT EXISTS messaging_consent_current_idx
    ON messaging_consent (tenant_id, channel_kind, address, recorded_at DESC, id DESC);

ALTER TABLE messaging_consent ENABLE ROW LEVEL SECURITY;
ALTER TABLE messaging_consent FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS messaging_consent_tenant_policy ON messaging_consent;
CREATE POLICY messaging_consent_tenant_policy ON messaging_consent
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    -- DELETE is granted for the erasure path only (a data subject asking to
    -- be forgotten entirely). Nothing in internal/domain exposes an update or
    -- a delete of a consent row as an ordinary operation; withdrawing is a
    -- new row saying so, which is the point of the table.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, DELETE ON messaging_consent TO crm_app;
    END IF;
END $$;

COMMIT;
