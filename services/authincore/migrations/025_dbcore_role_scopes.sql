-- DBCore role→scope catalogue.
--
-- The mapping lives in `resources`/`permissions` rather than in Go so that an
-- operator can retune what "operator" may do with an UPDATE, and so no login
-- handler ever has to know a product's scope names. The claims resolver
-- (internal/authz) reads these rows verbatim: whatever is in `permissions.scopes`
-- is what lands in the token.
--
-- Scopes are stored fully qualified ("dbcore:query:read") because that is the
-- string DBCore's interceptor compares against. Storing the bare action would
-- force the resolver to re-derive the prefix, which is exactly the
-- product-specific knowledge this table exists to hold.
--
-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION authincore_seed_dbcore_roles(target_org UUID)
RETURNS VOID AS $$
DECLARE
    resource_id_v UUID;
    role_id_v     UUID;
    spec          RECORD;
BEGIN
    -- `resources` carries no unique constraint on (org_id, type, name), so ON
    -- CONFLICT is unavailable and the lookup has to be explicit. Without this
    -- the function would insert a duplicate resource every time it ran.
    SELECT id INTO resource_id_v
    FROM resources
    WHERE org_id = target_org AND type = 'dbcore' AND name = 'dbcore'
    LIMIT 1;

    IF resource_id_v IS NULL THEN
        INSERT INTO resources (org_id, type, name)
        VALUES (target_org, 'dbcore', 'dbcore')
        RETURNING id INTO resource_id_v;
    END IF;

    FOR spec IN
        SELECT * FROM (VALUES
            -- `owner` is the role migration 023 grants to an org's first
            -- identity, and internal/authz.Check already treats owner and admin
            -- as one administrative tier. Granting both the same DBCore scopes
            -- keeps that equivalence true instead of letting the token say
            -- something the enforcer would disagree with.
            ('owner',    'Organization Owner', ARRAY['dbcore:*']),
            ('admin',    'DBCore Admin',       ARRAY['dbcore:*']),
            ('operator', 'DBCore Operator',    ARRAY[
                'dbcore:connection:read',
                'dbcore:connection:write',
                'dbcore:query:read',
                'dbcore:query:write',
                'dbcore:schema:read'
            ]),
            ('readonly', 'DBCore Read-only',   ARRAY[
                'dbcore:connection:read',
                'dbcore:query:read',
                'dbcore:schema:read'
            ])
        ) AS t(key, display_name, scopes)
    LOOP
        INSERT INTO roles (org_id, key, display_name)
        VALUES (target_org, spec.key, spec.display_name)
        ON CONFLICT (org_id, key) DO UPDATE SET display_name = roles.display_name
        RETURNING id INTO role_id_v;

        INSERT INTO permissions (org_id, role_id, resource_id, scopes)
        VALUES (target_org, role_id_v, resource_id_v, spec.scopes)
        ON CONFLICT (role_id, resource_id) DO UPDATE SET scopes = EXCLUDED.scopes;
    END LOOP;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION authincore_seed_dbcore_roles_on_org()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM authincore_seed_dbcore_roles(NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Without this, only organizations that existed when the migration ran would
-- have a DBCore mapping and every org created afterwards would mint empty
-- tokens — a failure that only shows up as a 403 long after the fact.
DROP TRIGGER IF EXISTS orgs_seed_dbcore_roles ON orgs;
CREATE TRIGGER orgs_seed_dbcore_roles
AFTER INSERT ON orgs
FOR EACH ROW EXECUTE FUNCTION authincore_seed_dbcore_roles_on_org();

-- Backfill every organization that already exists.
--
-- StatementBegin/End is required: goose splits plain SQL on semicolons, and a
-- DO block is full of them. Without the markers it ships only the fragment up to
-- the first `;` and Postgres rejects it as an unterminated dollar-quoted string.
-- +goose StatementBegin
DO $$
DECLARE
    o RECORD;
BEGIN
    FOR o IN SELECT id FROM orgs LOOP
        PERFORM authincore_seed_dbcore_roles(o.id);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER IF EXISTS orgs_seed_dbcore_roles ON orgs;
DROP FUNCTION IF EXISTS authincore_seed_dbcore_roles_on_org();

DELETE FROM permissions
WHERE resource_id IN (SELECT id FROM resources WHERE type = 'dbcore');
DELETE FROM resources WHERE type = 'dbcore';
DROP FUNCTION IF EXISTS authincore_seed_dbcore_roles(UUID);
