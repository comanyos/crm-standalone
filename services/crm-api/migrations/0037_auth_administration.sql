-- Platform/workspace administration metadata. The platform plan is assigned
-- to an organization (not a person), while user access remains a membership.
-- Every mutation is recorded with its authenticated actor for accountability.
BEGIN;

ALTER TABLE auth_organizations
    ADD COLUMN IF NOT EXISTS plan_key text NOT NULL DEFAULT 'free';

ALTER TABLE auth_organizations
    DROP CONSTRAINT IF EXISTS auth_organizations_plan_known;
ALTER TABLE auth_organizations
    ADD CONSTRAINT auth_organizations_plan_known
    CHECK (plan_key IN ('free', 'pro', 'enterprise'));

CREATE TABLE IF NOT EXISTS auth_admin_audit (
    id               text        PRIMARY KEY,
    actor_user_id    text        NOT NULL REFERENCES auth_users(id),
    organization_id  text        REFERENCES auth_organizations(id),
    target_user_id   text        REFERENCES auth_users(id),
    action           text        NOT NULL,
    before_state     jsonb,
    after_state      jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_admin_audit_action_known CHECK (
        action IN ('organization.created', 'organization.updated',
                   'membership.created', 'membership.updated')
    )
);

CREATE INDEX IF NOT EXISTS auth_admin_audit_org_created_idx
    ON auth_admin_audit (organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS auth_admin_audit_actor_created_idx
    ON auth_admin_audit (actor_user_id, created_at DESC);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'crm_app') THEN
        GRANT SELECT, INSERT, UPDATE ON auth_users, auth_organizations, auth_memberships TO crm_app;
        GRANT SELECT, INSERT ON auth_admin_audit TO crm_app;
    END IF;
END $$;

COMMIT;
