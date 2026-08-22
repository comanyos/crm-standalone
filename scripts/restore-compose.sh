#!/usr/bin/env bash
# Restore encrypted logical backups into the local Compose databases.
# This is intentionally destructive and requires an exact acknowledgement.
set -euo pipefail

: "${RESTORE_AUTH_ARCHIVE:?set encrypted AuthinCore archive path}"
: "${RESTORE_CRM_ARCHIVE:?set encrypted CRM archive path}"
: "${BACKUP_AGE_IDENTITY_FILE:?set age identity file path}"

if [[ ${RESTORE_CONFIRM:-} != "RESTORE_LOCAL_COMPOSE_DATABASES" ]]; then
  echo "refusing restore: set RESTORE_CONFIRM=RESTORE_LOCAL_COMPOSE_DATABASES" >&2
  exit 2
fi
if ! command -v age >/dev/null 2>&1; then
  echo "age is required to decrypt backups" >&2
  exit 2
fi
for archive in "$RESTORE_AUTH_ARCHIVE" "$RESTORE_CRM_ARCHIVE" "$BACKUP_AGE_IDENTITY_FILE"; do
  if [[ ! -f "$archive" ]]; then
    echo "required restore input does not exist: $archive" >&2
    exit 2
  fi
done

restore_database() {
  local service=$1
  local database=$2
  local archive=$3

  # Stop application traffic first. Dropping public is intentional: a logical
  # restore must not merge old rows with the existing development database.
  docker compose exec -T "$service" psql -v ON_ERROR_STOP=1 -U "$database" -d "$database" <<'SQL'
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
GRANT ALL ON SCHEMA public TO PUBLIC;
SQL
  age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" "$archive" \
    | gzip --decompress \
    | docker compose exec -T "$service" psql -v ON_ERROR_STOP=1 -U "$database" -d "$database"
}

echo "Restoring local Compose databases. Stop application containers before continuing."
restore_database auth-postgres authincore "$RESTORE_AUTH_ARCHIVE"
restore_database crm-postgres crm "$RESTORE_CRM_ARCHIVE"
echo "Restore completed. Run migrations and the smoke checks in docs/BACKUP_RESTORE.md before routing traffic."
