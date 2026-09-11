-- Finish the native Meta selection hierarchy. A Page is selected through the
-- Business that owns it, so operators can disambiguate identically named Pages
-- and the stored connection records the authorization boundary explicitly.
BEGIN;

ALTER TABLE meta_connections
  ADD COLUMN IF NOT EXISTS business_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS business_name text NOT NULL DEFAULT '';

ALTER TABLE meta_connections DROP CONSTRAINT IF EXISTS meta_connections_live_tokens;
ALTER TABLE meta_connections ADD CONSTRAINT meta_connections_live_tokens CHECK
  (status <> 'connected' OR (user_token_ciphertext IS NOT NULL AND page_token_ciphertext IS NOT NULL
    AND length(business_id) > 0 AND length(page_id) > 0 AND length(form_id) > 0));

-- One provider form must not silently feed two CRM workspaces. Rebinding is an
-- explicit disconnect/connect operation rather than an ambiguous fan-out.
DROP INDEX IF EXISTS meta_connections_webhook_route_idx;
CREATE UNIQUE INDEX IF NOT EXISTS meta_connections_webhook_route_key
  ON meta_connections (page_id, form_id) WHERE status = 'connected';

COMMIT;
