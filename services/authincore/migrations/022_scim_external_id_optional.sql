-- SCIM externalId is optional under RFC 7643. NULL preserves uniqueness for
-- supplied identifiers while allowing multiple resources without one.
-- +goose Up

ALTER TABLE scim_users ALTER COLUMN external_id DROP NOT NULL;
ALTER TABLE scim_groups ALTER COLUMN external_id DROP NOT NULL;

-- +goose Down

UPDATE scim_users SET external_id = id::text WHERE external_id IS NULL;
UPDATE scim_groups SET external_id = id::text WHERE external_id IS NULL;
ALTER TABLE scim_users ALTER COLUMN external_id SET NOT NULL;
ALTER TABLE scim_groups ALTER COLUMN external_id SET NOT NULL;
