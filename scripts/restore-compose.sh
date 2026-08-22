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

umask 077
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

# db_run mirrors backup-compose.sh — see the comment there for
# BACKUP_LOCAL_TOOLS.
db_run() {
  local service=$1
  shift
  if [[ -n ${BACKUP_LOCAL_TOOLS:-} ]]; then
    "$@"
  else
    docker compose exec -T "$service" "$@"
  fi
}

# ---------------------------------------------------------------------------
# Phase 1 — prepare and verify EVERY input before anything is dropped.
#
# The previous ordering dropped each schema and then piped a decryption into
# psql, so a wrong age identity or a truncated archive destroyed the database
# and had nothing to put back. Nothing below is destructive; the first
# irreversible statement runs in phase 2, by which point every archive is
# known to decrypt, decompress, and contain a dump.
# ---------------------------------------------------------------------------

prepare_archive() {
  local database=$1
  local archive=$2
  local plain="$staging/${database}.sql"

  if ! age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" "$archive" \
    | gzip --decompress >"$plain" 2>/dev/null; then
    echo "cannot decrypt or decompress the ${database} archive: $archive" >&2
    echo "nothing has been modified; check BACKUP_AGE_IDENTITY_FILE and the archive" >&2
    exit 2
  fi
  if [[ ! -s "$plain" ]]; then
    echo "the ${database} archive decrypted to an empty dump: $archive" >&2
    exit 2
  fi
  # A plain-format pg_dump always carries its own header. Its absence means
  # the archive is not a dump, whatever else decrypted cleanly.
  if ! grep -qm1 '^-- PostgreSQL database dump' "$plain"; then
    echo "the ${database} archive is not a PostgreSQL plain-format dump: $archive" >&2
    exit 2
  fi
  echo "verified ${database} archive: $(basename "$archive")"
}

# require_roles fails before any drop when a role the dump's GRANT statements
# reference is missing. Roles are cluster-global and pg_dump does not carry
# them, so restoring into a cluster that never ran the migrations would abort
# part-way through under ON_ERROR_STOP — after the schema had been dropped.
require_roles() {
  local service=$1
  local database=$2
  local role=$3
  shift 3

  local missing=()
  for required in "$@"; do
    if [[ "$(db_run "$service" psql -tAX -U "$role" -d "$database" \
      -c "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$required')")" != "t" ]]; then
      missing+=("$required")
    fi
  done
  if ((${#missing[@]} > 0)); then
    echo "refusing restore: ${database} is missing role(s): ${missing[*]}" >&2
    echo "apply the migrations first — the dump's GRANT statements reference them" >&2
    exit 2
  fi
}

prepare_archive authincore "$RESTORE_AUTH_ARCHIVE"
prepare_archive crm "$RESTORE_CRM_ARCHIVE"
require_roles crm-postgres crm postgres crm_app crm_drain

# ---------------------------------------------------------------------------
# Phase 2 — destructive. Every input above is verified by this point.
# ---------------------------------------------------------------------------

restore_database() {
  local service=$1
  local database=$2
  local role=$3
  local plain="$staging/${database}.sql"

  # Dropping public is intentional: a logical restore must not merge old rows
  # with the existing development database.
  db_run "$service" psql -v ON_ERROR_STOP=1 -U "$role" -d "$database" <<'SQL'
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
GRANT ALL ON SCHEMA public TO PUBLIC;
SQL
  db_run "$service" psql -v ON_ERROR_STOP=1 -U "$role" -d "$database" <"$plain"
  echo "restored ${database}"
}

echo "Restoring local Compose databases. Stop application containers before continuing."
restore_database auth-postgres authincore authincore
restore_database crm-postgres crm postgres
echo "Restore completed. Run migrations and the smoke checks in docs/BACKUP_RESTORE.md before routing traffic."
