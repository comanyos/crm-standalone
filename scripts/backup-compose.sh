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

backup_database() {
  local service=$1
  local database=$2
  local archive="$backup_dir/${database}-${timestamp}.sql.gz.age"

  docker compose exec -T "$service" pg_dump --format=plain --no-owner --no-privileges -U "$database" "$database" \
    | gzip -9 \
    | age --recipient "$age_recipient" --output "$archive"
  echo "created encrypted backup: $(basename "$archive")"
}

backup_database auth-postgres authincore
backup_database crm-postgres crm
