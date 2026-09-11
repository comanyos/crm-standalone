-- The Savvy support identity is the recovery-level platform administrator.
-- It remains a normal passwordless account, but neither application code nor
-- accidental direct SQL may delete it, disable it, move it out of its home
-- organization, or downgrade its administrator membership.
BEGIN;

CREATE OR REPLACE FUNCTION protect_savvy_super_admin_user()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.email_normalized = 'support@savvyadvi.com' THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'the Savvy super administrator cannot be deleted'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.email_normalized <> OLD.email_normalized OR NOT NEW.active THEN
            RAISE EXCEPTION 'the Savvy super administrator cannot be renamed or disabled'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS auth_users_protect_super_admin ON auth_users;
CREATE TRIGGER auth_users_protect_super_admin
BEFORE UPDATE OR DELETE ON auth_users
FOR EACH ROW EXECUTE FUNCTION protect_savvy_super_admin_user();

CREATE OR REPLACE FUNCTION protect_savvy_super_admin_membership()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    protected boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT EXISTS (
            SELECT 1
              FROM auth_users
             WHERE id = NEW.user_id
               AND email_normalized = 'support@savvyadvi.com'
        ) INTO protected;

        IF protected THEN
            IF NOT NEW.active OR NEW.role_key <> 'crm_admin' THEN
                RAISE EXCEPTION 'the Savvy super administrator must have an active administrator anchor membership'
                    USING ERRCODE = '23514';
            END IF;
            IF EXISTS (
                SELECT 1
                  FROM auth_memberships
                 WHERE user_id = NEW.user_id
            ) THEN
                RAISE EXCEPTION 'the Savvy super administrator cannot be added to another organization'
                    USING ERRCODE = '23514';
            END IF;
        END IF;
        RETURN NEW;
    END IF;

    SELECT EXISTS (
        SELECT 1
          FROM auth_users
         WHERE id = OLD.user_id
           AND email_normalized = 'support@savvyadvi.com'
    ) INTO protected;

    IF protected THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'the Savvy super administrator membership cannot be deleted'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.user_id <> OLD.user_id
           OR NEW.organization_id <> OLD.organization_id
           OR NOT NEW.active
           OR NEW.role_key <> 'crm_admin' THEN
            RAISE EXCEPTION 'the Savvy super administrator membership cannot be moved, disabled, or downgraded'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS auth_memberships_protect_super_admin ON auth_memberships;
CREATE TRIGGER auth_memberships_protect_super_admin
BEFORE INSERT OR UPDATE OR DELETE ON auth_memberships
FOR EACH ROW EXECUTE FUNCTION protect_savvy_super_admin_membership();

CREATE OR REPLACE FUNCTION protect_savvy_super_admin_organization()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    contains_super_admin boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
          FROM auth_memberships m
          JOIN auth_users u ON u.id = m.user_id
         WHERE m.organization_id = OLD.id
           AND u.email_normalized = 'support@savvyadvi.com'
    ) INTO contains_super_admin;

    IF contains_super_admin THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'the Savvy super administrator organization cannot be deleted'
                USING ERRCODE = '23514';
        END IF;
        IF NOT NEW.active THEN
            RAISE EXCEPTION 'the Savvy super administrator organization cannot be disabled'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS auth_organizations_protect_super_admin ON auth_organizations;
CREATE TRIGGER auth_organizations_protect_super_admin
BEFORE UPDATE OR DELETE ON auth_organizations
FOR EACH ROW EXECUTE FUNCTION protect_savvy_super_admin_organization();

COMMIT;
