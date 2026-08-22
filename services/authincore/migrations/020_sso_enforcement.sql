-- +goose Up

CREATE TABLE IF NOT EXISTS org_login_policies (
    org_id                UUID PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
    sso_enforced          BOOLEAN NOT NULL DEFAULT false,
    sso_provider_id       UUID,
    allow_password_fallback BOOLEAN NOT NULL DEFAULT false,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down

DROP TABLE IF EXISTS org_login_policies;
