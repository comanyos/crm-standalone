-- 0041_calendar_writes_and_workflow_automation.sql
-- CRM-authored Google events plus the durable scheduler, approvals and
-- signed outbound-webhook configuration used by workflow automation.

BEGIN;

ALTER TABLE calendar_events
    ADD COLUMN IF NOT EXISTS provider_etag text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS calendar_events_managed_owner_idx
    ON calendar_events (tenant_id, owner_id, starts_at)
    WHERE created_by <> '' AND erased_at IS NULL;

ALTER TABLE workflow_executions DROP CONSTRAINT IF EXISTS workflow_executions_status_known;
ALTER TABLE workflow_executions ADD CONSTRAINT workflow_executions_status_known
    CHECK (status IN ('pending', 'running', 'waiting_approval', 'succeeded', 'failed', 'skipped'));
ALTER TABLE workflow_executions DROP CONSTRAINT IF EXISTS workflow_executions_terminal_not_scheduled;
ALTER TABLE workflow_executions ADD CONSTRAINT workflow_executions_terminal_not_scheduled
    CHECK (status IN ('pending', 'running') OR next_attempt_at IS NULL);

CREATE TABLE IF NOT EXISTS workflow_schedules (
    tenant_id text NOT NULL,
    id text NOT NULL,
    rule_id text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    time_zone text NOT NULL,
    recurrence text NOT NULL,
    interval_count int NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'active',
    next_run_at timestamptz NOT NULL,
    ends_at timestamptz,
    last_run_at timestamptz,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT workflow_schedules_subject_known CHECK (subject_type IN ('account','contact','lead','opportunity')),
    CONSTRAINT workflow_schedules_recurrence_known CHECK (recurrence IN ('once','daily','weekly','monthly')),
    CONSTRAINT workflow_schedules_status_known CHECK (status IN ('active','paused','completed')),
    CONSTRAINT workflow_schedules_interval_bounded CHECK (interval_count BETWEEN 1 AND 365)
);
CREATE INDEX IF NOT EXISTS workflow_schedules_due_idx
    ON workflow_schedules (tenant_id, next_run_at) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS workflow_approvals (
    tenant_id text NOT NULL,
    id text NOT NULL,
    execution_id text NOT NULL,
    rule_id text NOT NULL,
    action_index int NOT NULL,
    approver_id text NOT NULL,
    title text NOT NULL,
    body text NOT NULL DEFAULT '',
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    decision_note text NOT NULL DEFAULT '',
    decision_by text NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT workflow_approvals_status_known CHECK (status IN ('pending','approved','rejected')),
    CONSTRAINT workflow_approvals_action_nonnegative CHECK (action_index >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS workflow_approvals_action_key
    ON workflow_approvals (tenant_id, execution_id, action_index);
CREATE INDEX IF NOT EXISTS workflow_approvals_inbox_idx
    ON workflow_approvals (tenant_id, approver_id, status, requested_at DESC);

CREATE TABLE IF NOT EXISTS workflow_webhook_endpoints (
    tenant_id text NOT NULL,
    id text NOT NULL,
    name text NOT NULL,
    url text NOT NULL,
    signing_secret_ciphertext bytea NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS workflow_webhook_endpoints_name_key
    ON workflow_webhook_endpoints (tenant_id, lower(name));

CREATE TABLE IF NOT EXISTS workflow_webhook_deliveries (
    tenant_id text NOT NULL,
    id text NOT NULL,
    endpoint_id text NOT NULL,
    execution_id text NOT NULL,
    action_index int NOT NULL,
    status text NOT NULL,
    response_code int NOT NULL DEFAULT 0,
    attempted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS workflow_webhook_deliveries_execution_idx
    ON workflow_webhook_deliveries (tenant_id, execution_id, action_index, attempted_at DESC);

ALTER TABLE workflow_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_schedules FORCE ROW LEVEL SECURITY;
ALTER TABLE workflow_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_approvals FORCE ROW LEVEL SECURITY;
ALTER TABLE workflow_webhook_endpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_webhook_endpoints FORCE ROW LEVEL SECURITY;
ALTER TABLE workflow_webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_webhook_deliveries FORCE ROW LEVEL SECURITY;

CREATE POLICY workflow_schedules_tenant_policy ON workflow_schedules FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
CREATE POLICY workflow_approvals_tenant_policy ON workflow_approvals FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
CREATE POLICY workflow_webhook_endpoints_tenant_policy ON workflow_webhook_endpoints FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
CREATE POLICY workflow_webhook_deliveries_tenant_policy ON workflow_webhook_deliveries FOR ALL
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON workflow_schedules, workflow_approvals,
            workflow_webhook_endpoints, workflow_webhook_deliveries TO crm_app;
    END IF;
END $$;

COMMIT;
