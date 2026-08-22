-- 0009_activity_timeline.sql
-- B5: Activity polymorphic referential integrity, chronological timeline
-- query, and immutability.
--
-- activities.subject_type, subject_id and occurred_at need no schema change
-- here — 0001 already added all three columns; this migration only backs
-- the new timeline query (internal/domain/activity_timeline.go,
-- ListActivitiesForSubject) with an index shaped for it: filter by
-- (tenant_id, subject_type, subject_id), order by occurred_at DESC.
--
-- Referential integrity (subject_type + subject_id must resolve to an
-- existing, tenant-scoped Account/Contact/Lead/Opportunity) and
-- immutability (Update rejects any real content change) are enforced in Go,
-- not SQL — the same split every other B-track invariant in this schema
-- uses.
--
-- Migrations are append-only: add 0010_*.sql, never edit this file.

BEGIN;

CREATE INDEX IF NOT EXISTS activities_tenant_subject_occurred_idx
    ON activities (tenant_id, subject_type, subject_id, occurred_at DESC);

COMMIT;
