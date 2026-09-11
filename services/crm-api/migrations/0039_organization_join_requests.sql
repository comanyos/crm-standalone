-- Self-service organization access: a high-entropy, rotatable organization
-- code identifies the workspace, while an administrator approval remains the
-- authorization boundary. Codes are never stored in plaintext.
BEGIN;

ALTER TABLE auth_email_challenges
    DROP CONSTRAINT IF EXISTS auth_email_challenges_flow_known;
ALTER TABLE auth_email_challenges
    ADD CONSTRAINT auth_email_challenges_flow_known
    CHECK (flow IN ('login', 'signup', 'join'));

ALTER TABLE auth_oauth_states
    ADD COLUMN IF NOT EXISTS join_code_digest bytea;
ALTER TABLE auth_oauth_states
    DROP CONSTRAINT IF EXISTS auth_oauth_states_flow_known;
ALTER TABLE auth_oauth_states
    ADD CONSTRAINT auth_oauth_states_flow_known
    CHECK (flow IN ('login', 'signup', 'join'));
ALTER TABLE auth_oauth_states
    DROP CONSTRAINT IF EXISTS auth_oauth_states_join_code_shape;
ALTER TABLE auth_oauth_states
    ADD CONSTRAINT auth_oauth_states_join_code_shape CHECK (
        (flow = 'join' AND octet_length(join_code_digest) = 32)
        OR (flow <> 'join' AND join_code_digest IS NULL)
    );

CREATE TABLE IF NOT EXISTS auth_organization_join_codes (
    organization_id text        PRIMARY KEY REFERENCES auth_organizations(id) ON DELETE CASCADE,
    code_digest      bytea       NOT NULL UNIQUE,
    code_hint        text        NOT NULL,
    enabled          boolean     NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_organization_join_codes_digest_shape CHECK (octet_length(code_digest) = 32),
    CONSTRAINT auth_organization_join_codes_hint_shape CHECK (length(code_hint) BETWEEN 5 AND 24)
);

CREATE TABLE IF NOT EXISTS auth_join_requests (
    id               text        PRIMARY KEY,
    organization_id  text        NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
    user_id           text        NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    status            text        NOT NULL DEFAULT 'pending',
    assigned_role     text,
    requested_at      timestamptz NOT NULL DEFAULT now(),
    reviewed_at       timestamptz,
    reviewed_by       text        REFERENCES auth_users(id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_join_requests_status_known CHECK (status IN ('pending', 'approved', 'rejected')),
    CONSTRAINT auth_join_requests_role_known CHECK (
        assigned_role IS NULL OR assigned_role IN ('crm_admin', 'crm_manager', 'crm_member', 'crm_viewer')
    ),
    CONSTRAINT auth_join_requests_review_shape CHECK (
        (status = 'pending' AND reviewed_at IS NULL AND reviewed_by IS NULL AND assigned_role IS NULL)
        OR (status = 'approved' AND reviewed_at IS NOT NULL AND reviewed_by IS NOT NULL AND assigned_role IS NOT NULL)
        OR (status = 'rejected' AND reviewed_at IS NOT NULL AND reviewed_by IS NOT NULL AND assigned_role IS NULL)
    )
);

-- One applicant may have only one unresolved organization request. This keeps
-- the current single-workspace session model deterministic and makes retries
-- idempotent while retaining reviewed requests as audit history.
CREATE UNIQUE INDEX IF NOT EXISTS auth_join_requests_one_pending_per_user
    ON auth_join_requests (user_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS auth_join_requests_org_pending
    ON auth_join_requests (organization_id, requested_at, id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS auth_join_requests_user_history
    ON auth_join_requests (user_id, requested_at DESC);

ALTER TABLE auth_admin_audit
    DROP CONSTRAINT IF EXISTS auth_admin_audit_action_known;
ALTER TABLE auth_admin_audit
    ADD CONSTRAINT auth_admin_audit_action_known CHECK (
        action IN ('organization.created', 'organization.updated',
                   'membership.created', 'membership.updated',
                   'join_request.approved', 'join_request.rejected',
                   'join_code.rotated', 'join_code.updated')
    );

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE ON auth_organization_join_codes, auth_join_requests TO crm_app;
        GRANT SELECT, UPDATE ON auth_oauth_states, auth_email_challenges TO crm_app;
    END IF;
END $$;

COMMIT;
