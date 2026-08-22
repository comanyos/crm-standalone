-- Ensure every organization has an owner. The first identity created in an
-- organization is promoted atomically, including concurrent registrations.
-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION authincore_bootstrap_org_owner()
RETURNS TRIGGER AS $$
DECLARE
    owner_role_id UUID;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.org_id::text, 0));

    IF (SELECT COUNT(*) FROM identities WHERE org_id = NEW.org_id) = 1 THEN
        INSERT INTO roles (id, org_id, key, display_name)
        VALUES (gen_random_uuid(), NEW.org_id, 'owner', 'Organization Owner')
        ON CONFLICT (org_id, key) DO UPDATE SET display_name = EXCLUDED.display_name
        RETURNING id INTO owner_role_id;

        INSERT INTO role_assignments (id, org_id, identity_id, role_id)
        VALUES (gen_random_uuid(), NEW.org_id, NEW.id, owner_role_id)
        ON CONFLICT (org_id, identity_id, role_id) DO NOTHING;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS identities_bootstrap_org_owner ON identities;
CREATE TRIGGER identities_bootstrap_org_owner
AFTER INSERT ON identities
FOR EACH ROW EXECUTE FUNCTION authincore_bootstrap_org_owner();

-- Backfill existing organizations that have identities but no owner.
WITH first_identity AS (
    SELECT DISTINCT ON (org_id) id AS identity_id, org_id
    FROM identities
    ORDER BY org_id, created_at, id
), owner_roles AS (
    INSERT INTO roles (id, org_id, key, display_name)
    SELECT gen_random_uuid(), fi.org_id, 'owner', 'Organization Owner'
    FROM first_identity fi
    ON CONFLICT (org_id, key) DO UPDATE SET display_name = EXCLUDED.display_name
    RETURNING id, org_id
)
INSERT INTO role_assignments (id, org_id, identity_id, role_id)
SELECT gen_random_uuid(), fi.org_id, fi.identity_id, owner_roles.id
FROM first_identity fi
JOIN owner_roles USING (org_id)
WHERE NOT EXISTS (
    SELECT 1 FROM role_assignments ra
    JOIN roles r ON r.id = ra.role_id
    WHERE ra.org_id = fi.org_id AND r.key = 'owner'
)
ON CONFLICT (org_id, identity_id, role_id) DO NOTHING;

-- +goose Down

DROP TRIGGER IF EXISTS identities_bootstrap_org_owner ON identities;
DROP FUNCTION IF EXISTS authincore_bootstrap_org_owner();
