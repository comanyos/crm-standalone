-- Native Facebook and Instagram Lead Ads connection, webhook routing and
-- immutable lead attribution. Credentials are ciphertext only; application
-- secrets and encryption keys remain deployment configuration.
BEGIN;

CREATE TABLE IF NOT EXISTS meta_oauth_states (
    tenant_id text NOT NULL,
    state text NOT NULL,
    actor_id text NOT NULL,
    return_path text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    PRIMARY KEY (tenant_id, state),
    CONSTRAINT meta_oauth_states_state_not_blank CHECK (length(btrim(state)) > 0),
    CONSTRAINT meta_oauth_states_actor_not_blank CHECK (length(btrim(actor_id)) > 0),
    CONSTRAINT meta_oauth_states_expiry CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS meta_oauth_states_global_state_key ON meta_oauth_states (state);
CREATE INDEX IF NOT EXISTS meta_oauth_states_expiry_idx ON meta_oauth_states (tenant_id, expires_at);

CREATE TABLE IF NOT EXISTS meta_connections (
    tenant_id text NOT NULL,
    id text NOT NULL,
    meta_user_id text NOT NULL DEFAULT '',
    meta_user_name text NOT NULL DEFAULT '',
    status text NOT NULL,
    granted_permissions text NOT NULL DEFAULT '',
    user_token_ciphertext bytea,
    page_token_ciphertext bytea,
    access_token_expires_at timestamptz,
    page_id text NOT NULL DEFAULT '',
    page_name text NOT NULL DEFAULT '',
    form_id text NOT NULL DEFAULT '',
    form_name text NOT NULL DEFAULT '',
    connected_by text NOT NULL DEFAULT '',
    connected_at timestamptz,
    last_webhook_at timestamptz,
    last_import_at timestamptz,
    disconnected_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT meta_connections_status_known CHECK
      (status IN ('selection_required','connected','reauthorization_required','disconnected')),
    CONSTRAINT meta_connections_live_tokens CHECK
      (status <> 'connected' OR (user_token_ciphertext IS NOT NULL AND page_token_ciphertext IS NOT NULL
        AND length(page_id) > 0 AND length(form_id) > 0)),
    CONSTRAINT meta_connections_pending_token CHECK
      (status <> 'selection_required' OR user_token_ciphertext IS NOT NULL),
    CONSTRAINT meta_connections_inactive_no_tokens CHECK
      (status NOT IN ('reauthorization_required','disconnected') OR
        (user_token_ciphertext IS NULL AND page_token_ciphertext IS NULL))
);
-- One durable authorization per CRM organization; it may be rebound to a
-- different Page/Form without accumulating orphaned credentials.
CREATE UNIQUE INDEX IF NOT EXISTS meta_connections_tenant_key ON meta_connections (tenant_id);
CREATE INDEX IF NOT EXISTS meta_connections_webhook_route_idx
    ON meta_connections (page_id, form_id) WHERE status = 'connected';

CREATE TABLE IF NOT EXISTS meta_lead_attribution (
    tenant_id text NOT NULL,
    lead_id text NOT NULL,
    connection_id text NOT NULL,
    provider_lead_id text NOT NULL,
    platform text NOT NULL DEFAULT '',
    page_id text NOT NULL DEFAULT '',
    form_id text NOT NULL DEFAULT '',
    ad_id text NOT NULL DEFAULT '',
    ad_name text NOT NULL DEFAULT '',
    adset_id text NOT NULL DEFAULT '',
    adset_name text NOT NULL DEFAULT '',
    campaign_id text NOT NULL DEFAULT '',
    campaign_name text NOT NULL DEFAULT '',
    provider_created_at timestamptz,
    captured_at timestamptz NOT NULL DEFAULT now(),
    raw_payload text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, lead_id),
    CONSTRAINT meta_lead_attribution_lead_fk FOREIGN KEY (tenant_id, lead_id)
      REFERENCES leads (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT meta_lead_attribution_connection_fk FOREIGN KEY (tenant_id, connection_id)
      REFERENCES meta_connections (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT meta_lead_attribution_provider_not_blank CHECK (length(btrim(provider_lead_id)) > 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS meta_lead_attribution_provider_key
    ON meta_lead_attribution (tenant_id, provider_lead_id);
CREATE INDEX IF NOT EXISTS meta_lead_attribution_campaign_idx
    ON meta_lead_attribution (tenant_id, campaign_id, captured_at DESC);

ALTER TABLE meta_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE meta_oauth_states FORCE ROW LEVEL SECURITY;
ALTER TABLE meta_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE meta_connections FORCE ROW LEVEL SECURITY;
ALTER TABLE meta_lead_attribution ENABLE ROW LEVEL SECURITY;
ALTER TABLE meta_lead_attribution FORCE ROW LEVEL SECURITY;

CREATE POLICY meta_oauth_states_tenant_policy ON meta_oauth_states FOR ALL
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
CREATE POLICY meta_connections_tenant_policy ON meta_connections FOR ALL
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));
CREATE POLICY meta_lead_attribution_tenant_policy ON meta_lead_attribution FOR ALL
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON meta_oauth_states, meta_connections, meta_lead_attribution TO crm_app;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_drain') THEN
    -- The unauthenticated OAuth callback resolves state; the signed webhook
    -- resolves Page/Form to tenant and receives only encrypted token bytes.
    GRANT SELECT ON meta_oauth_states, meta_connections TO crm_drain;
  END IF;
END $$;

COMMIT;
