-- 0021_calendar_sync.sql
-- Google Calendar synchronisation (docs/GOOGLE_INTEGRATION.md).
--
-- The same privacy rule as Gmail applies and is encoded the same way: an
-- event is recorded only when one of its attendees is already a Contact in
-- this tenant, and what is kept is an envelope — title, times, organizer,
-- attendee count. There is no description column and no location column,
-- because a calendar holds medical appointments and job interviews as readily
-- as customer meetings.
--
-- What makes calendars different from mail, and what this schema is shaped
-- around: an event is MUTABLE. A meeting gets moved, and it gets cancelled.
-- Email never does either. So calendar_events is a mirror of Google's current
-- state — updated in place — while the CRM timeline stays append-only, which
-- is the property that makes an Activity worth having as evidence.
--
-- state_key is what connects the two. It fingerprints the part of an event a
-- person would want to be told about (its status and its start). A sync that
-- sees a new fingerprint appends a timeline entry; a sync that sees the same
-- one updates the mirror silently. Without it, either every description edit
-- would post to the timeline, or a rescheduled meeting never would.
--
-- Deliberately NOT a shared table with gmail_sync_state, though the columns
-- look alike. The two cursors fail differently — Gmail history expires into a
-- 404 needing a bounded backfill, a Calendar syncToken expires into a 410
-- needing a windowed re-list — and last_event_at here is a time in the
-- future, which it never is for mail. A shared table would need a
-- discriminator plus columns that are null for one side, which is the shape
-- that hides bugs.
--
-- Migrations are append-only: add 0022_*.sql, never edit this file.

BEGIN;

CREATE TABLE IF NOT EXISTS calendar_sync_state (
    tenant_id           text        NOT NULL,
    owner_id            text        NOT NULL,

    -- sync_token is Google's own incremental cursor. It expires into HTTP 410,
    -- at which point the only correct response is to re-list the window.
    sync_token          text        NOT NULL DEFAULT '',
    backfill_page_token text        NOT NULL DEFAULT '',
    backfill_complete   boolean     NOT NULL DEFAULT false,

    -- A lease, not a lock: it expires, so a replica that dies mid-pass cannot
    -- make a calendar permanently unsyncable.
    syncing_until       timestamptz,

    last_synced_at      timestamptz,
    -- last_event_at is the start of the latest event indexed, which for a
    -- calendar is usually in the future. It is progress information, not a
    -- freshness claim.
    last_event_at       timestamptz,
    events_indexed      bigint      NOT NULL DEFAULT 0,
    activities_created  bigint      NOT NULL DEFAULT 0,
    last_error          text        NOT NULL DEFAULT '',

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, owner_id),
    CONSTRAINT calendar_sync_state_owner_not_blank
        CHECK (length(btrim(owner_id)) > 0),
    CONSTRAINT calendar_sync_state_counters_not_negative
        CHECK (events_indexed >= 0 AND activities_created >= 0)
);

CREATE INDEX IF NOT EXISTS calendar_sync_state_due_idx
    ON calendar_sync_state (syncing_until NULLS FIRST, last_synced_at NULLS FIRST);

CREATE TABLE IF NOT EXISTS calendar_events (
    tenant_id          text        NOT NULL,
    owner_id           text        NOT NULL,
    provider_event_id  text        NOT NULL,
    subject_type       text        NOT NULL,
    subject_id         text        NOT NULL,

    calendar_id        text        NOT NULL DEFAULT 'primary',
    -- ical_uid is stable across calendars, so the same meeting on the
    -- organizer's and an attendee's calendar shares it. recurring_event_id
    -- names the series an instance belongs to. Neither is read yet; both are
    -- free at sync time and impossible to backfill without re-listing.
    ical_uid           text        NOT NULL DEFAULT '',
    recurring_event_id text        NOT NULL DEFAULT '',

    activity_id        text        NOT NULL,
    title              text        NOT NULL DEFAULT '',
    status             text        NOT NULL DEFAULT 'confirmed',

    starts_at          timestamptz NOT NULL,
    ends_at            timestamptz,
    -- all_day and time_zone travel together and exist for one reason: an
    -- all-day event on 1 August in Asia/Kolkata is 2026-07-31T18:30Z, and a
    -- reader who forgets that shows the customer the wrong day. Keeping the
    -- IANA zone means the date can always be rendered in the event's own
    -- terms rather than the viewer's.
    all_day            boolean     NOT NULL DEFAULT false,
    time_zone          text        NOT NULL DEFAULT '',

    organizer_address  text        NOT NULL DEFAULT '',
    attendee_count     int         NOT NULL DEFAULT 0,

    -- state_key fingerprints the material state last posted to the timeline.
    -- See the header: it is what stops a description edit from posting, and
    -- what makes a reschedule post.
    state_key          text        NOT NULL DEFAULT '',

    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, owner_id, provider_event_id, subject_type, subject_id),
    CONSTRAINT calendar_events_status_known
        CHECK (status IN ('confirmed', 'tentative', 'cancelled')),
    CONSTRAINT calendar_events_subject_type_known
        CHECK (subject_type IN ('account', 'contact', 'lead', 'opportunity')),
    CONSTRAINT calendar_events_ids_not_blank
        CHECK (length(btrim(owner_id)) > 0
           AND length(btrim(provider_event_id)) > 0
           AND length(btrim(subject_id)) > 0
           AND length(btrim(activity_id)) > 0),
    -- An all-day event without its zone cannot be rendered correctly, and a
    -- row that cannot be rendered correctly is worse than one that is absent.
    CONSTRAINT calendar_events_all_day_has_zone
        CHECK (NOT all_day OR length(btrim(time_zone)) > 0),
    CONSTRAINT calendar_events_ends_after_start
        CHECK (ends_at IS NULL OR ends_at >= starts_at)
);

-- "What meetings has this record been part of", soonest first for upcoming
-- and most recent first for past — one index serves both directions.
CREATE INDEX IF NOT EXISTS calendar_events_subject_idx
    ON calendar_events (tenant_id, subject_type, subject_id, starts_at DESC);

CREATE INDEX IF NOT EXISTS calendar_events_owner_idx
    ON calendar_events (tenant_id, owner_id);

ALTER TABLE calendar_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE calendar_sync_state FORCE  ROW LEVEL SECURITY;
ALTER TABLE calendar_events     ENABLE ROW LEVEL SECURITY;
ALTER TABLE calendar_events     FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS calendar_sync_state_tenant_policy ON calendar_sync_state;
CREATE POLICY calendar_sync_state_tenant_policy ON calendar_sync_state
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS calendar_events_tenant_policy ON calendar_events;
CREATE POLICY calendar_events_tenant_policy ON calendar_events
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON calendar_sync_state, calendar_events TO crm_app;
    END IF;
    -- crm_drain reads calendar_sync_state across tenants for one query: which
    -- calendars are due. Identifiers only, and deliberately not granted on
    -- calendar_events, which holds meeting metadata.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON calendar_sync_state TO crm_drain;
    END IF;
END $$;

COMMIT;
