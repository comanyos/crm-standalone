#!/usr/bin/env bash
# Create encrypted logical backups of the local Compose PostgreSQL databases.
# This is deliberately an operator command: it never runs from `make up` and
# it refuses to write an unencrypted database dump to disk.
set -euo pipefail

backup_dir=${BACKUP_OUTPUT_DIR:?set BACKUP_OUTPUT_DIR to an existing protected directory}
age_recipient=${BACKUP_AGE_RECIPIENT:?set BACKUP_AGE_RECIPIENT to the recipient public key}

if [[ ! -d "$backup_dir" ]]; then
  echo "BACKUP_OUTPUT_DIR must already exist: $backup_dir" >&2
  exit 2
fi
if ! command -v age >/dev/null 2>&1; then
  echo "age is required to encrypt backups; refusing to create plaintext dumps" >&2
  exit 2
fi

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
umask 077

# db_run executes a database tool against one Compose service.
#
# BACKUP_LOCAL_TOOLS=1 runs the tool directly on this host instead, against
# whatever PGHOST/PGPORT/PGUSER/PGPASSWORD point at. That exists so
# scripts/test-backup-restore.sh can exercise this pipeline for real against a
# throwaway cluster; operators never set it.
db_run() {
  local service=$1
  shift
  if [[ -n ${BACKUP_LOCAL_TOOLS:-} ]]; then
    "$@"
  else
    docker compose exec -T "$service" "$@"
  fi
}

# backup_database <service> <database> <role>
#
# The role is a separate argument because it is not the database name:
# compose.yaml runs auth-postgres as `authincore` but crm-postgres as
# `postgres`, with the CRM's own crm_app/crm_drain roles created later by
# migrations/0003_rls_enforce.sql. Deriving the role from the database name
# addressed a `crm` role that has never existed.
backup_database() {
  local service=$1
  local database=$2
  local role=$3
  local archive="$backup_dir/${database}-${timestamp}.sql.gz.age"

  # --no-owner is safe: every object is owned by the bootstrap superuser, and
  # dropping the OWNER TO statements lets the dump restore under whichever
  # role performs the restore.
  #
  # Privileges are deliberately NOT excluded. 0003_rls_enforce.sql grants
  # crm_app the DML it needs and crm_drain its cross-tenant read, and those
  # grants live in the dump's GRANT statements. A --no-privileges dump
  # restores a database the application cannot read, which looks like a
  # successful restore until the first request fails.
  db_run "$service" pg_dump --format=plain --no-owner -U "$role" "$database" \
    | gzip -9 \
    | age --recipient "$age_recipient" --output "$archive"
  echo "created encrypted backup: $(basename "$archive")"
}

backup_database auth-postgres authincore authincore
backup_database crm-postgres crm postgres
