#!/usr/bin/env bash
# Round-trip check for scripts/backup-compose.sh and scripts/restore-compose.sh:
# seed -> back up -> destroy -> restore -> assert the data and the application
# role's privileges both came back.
#
# It runs against a throwaway PostgreSQL cluster it creates and deletes itself,
# so it needs no Docker and touches no development database. The two scripts
# under test run their real pipelines (pg_dump, gzip, age, psql); only the
# `docker compose exec` wrapper is bypassed, via BACKUP_LOCAL_TOOLS.
#
# The point is the CRM half: those scripts addressed a `crm` role that has
# never existed, so neither had ever run successfully against the CRM database.
set -euo pipefail

# PostgreSQL refuses to initdb or start as root. Re-exec as an unprivileged
# user so this works in a root container as well as on a normal CI runner,
# which already runs unprivileged and skips this.
if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
  for unprivileged in postgres nobody; do
    if id "$unprivileged" >/dev/null 2>&1; then
      exec su -s /bin/bash "$unprivileged" -c \
        "TEST_PG_PORT='${TEST_PG_PORT:-}' bash $(printf '%q' "${BASH_SOURCE[0]}")"
    fi
  done
  echo "running as root and no unprivileged user available; skipping" >&2
  exit 0
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
port=${TEST_PG_PORT:-55432}
pgbin=""

cleanup() {
  if [[ -n "$pgbin" && -d "$work/data" ]]; then
    "$pgbin/pg_ctl" -D "$work/data" -m immediate stop >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

for candidate in /usr/lib/postgresql/*/bin "$(dirname "$(command -v initdb 2>/dev/null || echo /nonexistent)")"; do
  if [[ -x "$candidate/initdb" && -x "$candidate/pg_ctl" ]]; then
    pgbin=$candidate
    break
  fi
done
if [[ -z "$pgbin" ]]; then
  echo "no PostgreSQL server binaries found (initdb/pg_ctl); skipping" >&2
  exit 0
fi
for tool in age gzip pg_dump psql; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required" >&2; exit 2; }
done

echo "== starting a throwaway cluster on port $port =="
mkdir -p "$work/data"
"$pgbin/initdb" -D "$work/data" -U postgres --auth=trust >/dev/null
"$pgbin/pg_ctl" -D "$work/data" -o "-p $port -k $work -c listen_addresses=" -w start >/dev/null

export PGHOST=$work PGPORT=$port PGUSER=postgres
export BACKUP_LOCAL_TOOLS=1

# Mirror the parts of the real CRM database the scripts depend on: the
# `postgres` superuser, the crm database, and the crm_app/crm_drain roles that
# 0003_rls_enforce.sql creates and grants to.
echo "== seeding =="
psql -q -v ON_ERROR_STOP=1 -d postgres -c "CREATE DATABASE crm"
psql -q -v ON_ERROR_STOP=1 -d postgres <<'SQL'
CREATE ROLE crm_app LOGIN PASSWORD 'crm_app';
CREATE ROLE crm_drain LOGIN PASSWORD 'crm_drain' BYPASSRLS;
SQL
psql -q -v ON_ERROR_STOP=1 -d crm <<'SQL'
CREATE TABLE accounts (tenant_id text NOT NULL, id text PRIMARY KEY, name text NOT NULL);
INSERT INTO accounts VALUES ('tenant-a', 'acc-1', 'Northwind'), ('tenant-b', 'acc-2', 'Contoso');
GRANT USAGE ON SCHEMA public TO crm_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO crm_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO crm_drain;
SQL
echo "== generating an age key =="
age-keygen -o "$work/identity.txt" 2>/dev/null
recipient=$(age-keygen -y "$work/identity.txt")
mkdir -p "$work/backups"

echo "== backup =="
BACKUP_OUTPUT_DIR="$work/backups" BACKUP_AGE_RECIPIENT="$recipient" \
  "$repo_root/scripts/backup-compose.sh"

crm_archive=$(ls "$work"/backups/crm-*.sql.gz.age)

echo "== a wrong identity must not destroy anything =="
age-keygen -o "$work/wrong.txt" 2>/dev/null
set +e
RESTORE_CONFIRM=RESTORE_LOCAL_COMPOSE_DATABASE \
RESTORE_CRM_ARCHIVE="$crm_archive" \
BACKUP_AGE_IDENTITY_FILE="$work/wrong.txt" \
  "$repo_root/scripts/restore-compose.sh" >/dev/null 2>&1
wrong_status=$?
set -e
if ((wrong_status == 0)); then
  echo "FAIL: restore accepted a wrong age identity" >&2
  exit 1
fi
survivors=$(psql -tAX -d crm -c "SELECT count(*) FROM accounts")
if [[ "$survivors" != "2" ]]; then
  echo "FAIL: a failed restore destroyed data (accounts=$survivors, want 2)" >&2
  exit 1
fi
echo "   verify-before-drop held: both rows intact after the failed restore"

echo "== destroying the data the restore must bring back =="
psql -q -v ON_ERROR_STOP=1 -d crm -c "DELETE FROM accounts WHERE tenant_id = 'tenant-b'"

echo "== restore =="
RESTORE_CONFIRM=RESTORE_LOCAL_COMPOSE_DATABASE \
RESTORE_CRM_ARCHIVE="$crm_archive" \
BACKUP_AGE_IDENTITY_FILE="$work/identity.txt" \
  "$repo_root/scripts/restore-compose.sh"

echo "== asserting =="
restored=$(psql -tAX -d crm -c "SELECT count(*) FROM accounts")
if [[ "$restored" != "2" ]]; then
  echo "FAIL: expected 2 accounts after restore, got $restored" >&2
  exit 1
fi
# The regression that --no-privileges caused: the restore succeeds, and the
# application role cannot read a row.
app_can_read=$(psql -tAX -U crm_app -d crm -c "SELECT count(*) FROM accounts" 2>&1 || echo DENIED)
if [[ "$app_can_read" != "2" ]]; then
  echo "FAIL: crm_app cannot read restored data (got: $app_can_read)" >&2
  echo "      the dump dropped its GRANT statements" >&2
  exit 1
fi
drain_can_read=$(psql -tAX -U crm_drain -d crm -c "SELECT count(*) FROM accounts" 2>&1 || echo DENIED)
if [[ "$drain_can_read" != "2" ]]; then
  echo "FAIL: crm_drain cannot read restored data (got: $drain_can_read)" >&2
  exit 1
fi

echo "PASS: round trip restored the CRM database; crm_app and crm_drain kept their grants"
