-- Durable speed-to-lead alerts and email delivery.
--
-- Existing leads are marked as having received both alert classes so deploying
-- this migration never floods a team with historical "new lead" or overdue
-- mail. Historical untouched leads still surface in My Day; email escalation
-- begins with leads created after this release.
BEGIN;

CREATE TABLE IF NOT EXISTS lead_alert_states (
    tenant_id          text        NOT NULL,
    lead_id            text        NOT NULL,
    initial_notified_at timestamptz,
    sla_escalated_at    timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, lead_id),
    CONSTRAINT lead_alert_states_lead_fk
        FOREIGN KEY (tenant_id, lead_id) REFERENCES leads(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS lead_alert_states_initial_due_idx
    ON lead_alert_states (tenant_id, initial_notified_at) WHERE initial_notified_at IS NULL;
CREATE INDEX IF NOT EXISTS lead_alert_states_sla_due_idx
    ON lead_alert_states (tenant_id, sla_escalated_at) WHERE sla_escalated_at IS NULL;

INSERT INTO lead_alert_states
    (tenant_id, lead_id, initial_notified_at, sla_escalated_at)
SELECT tenant_id, id, now(), now() FROM leads
ON CONFLICT (tenant_id, lead_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS notification_deliveries (
    tenant_id       text        NOT NULL,
    notification_id text        NOT NULL,
    channel         text        NOT NULL DEFAULT 'email',
    status          text        NOT NULL DEFAULT 'pending',
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    delivered_at    timestamptz,
    error_category  text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, notification_id, channel),
    CONSTRAINT notification_deliveries_notification_fk
        FOREIGN KEY (tenant_id, notification_id)
        REFERENCES notifications(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT notification_deliveries_channel_known CHECK (channel IN ('email')),
    CONSTRAINT notification_deliveries_status_known
        CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    CONSTRAINT notification_deliveries_attempts_nonnegative CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS notification_deliveries_due_idx
    ON notification_deliveries (next_attempt_at NULLS FIRST, created_at)
    WHERE status IN ('pending', 'sending');

-- Every in-app notification also gets a durable email attempt. The trigger is
-- created after the historical lead-state backfill and does not backfill old
-- notifications, so deployment itself sends nothing.
CREATE OR REPLACE FUNCTION enqueue_notification_email_delivery()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO notification_deliveries
        (tenant_id, notification_id, channel, status, created_at, updated_at)
    VALUES (NEW.tenant_id, NEW.id, 'email', 'pending', NEW.created_at, NEW.created_at)
    ON CONFLICT (tenant_id, notification_id, channel) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS notifications_enqueue_email ON notifications;
CREATE TRIGGER notifications_enqueue_email
AFTER INSERT ON notifications
FOR EACH ROW EXECUTE FUNCTION enqueue_notification_email_delivery();

ALTER TABLE lead_alert_states       ENABLE ROW LEVEL SECURITY;
ALTER TABLE lead_alert_states       FORCE  ROW LEVEL SECURITY;
ALTER TABLE notification_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_deliveries FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS lead_alert_states_tenant_policy ON lead_alert_states;
CREATE POLICY lead_alert_states_tenant_policy ON lead_alert_states
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DROP POLICY IF EXISTS notification_deliveries_tenant_policy ON notification_deliveries;
CREATE POLICY notification_deliveries_tenant_policy ON notification_deliveries
    FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON lead_alert_states, notification_deliveries TO crm_app;
    END IF;
    -- Cross-tenant polling returns identifiers only; every mutation and the
    -- notification body read happen through the tenant-scoped application
    -- connection afterwards.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
        GRANT SELECT ON lead_alert_states, notification_deliveries TO crm_drain;
    END IF;
END $$;

COMMIT;
