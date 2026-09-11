#!/usr/bin/env bash
set -euo pipefail

cd /opt/savvy-crm

docker compose --env-file runtime.env -f compose.yaml ps

test "$(stat -c '%a' runtime.env)" = "600"
echo 'runtime_env_permissions=600'

mountpoint -q /var/lib/savvy-crm
test "$(docker info --format '{{.DockerRootDir}}')" = "/var/lib/savvy-crm/docker"
echo 'persistent_data_disk=mounted'

docker compose --env-file runtime.env -f compose.yaml exec -T postgres \
  psql --username postgres --dbname crm --tuples-only --no-align \
  --command "SELECT a.usename || ':' || s.ssl || ':' || coalesce(s.version, '') FROM pg_stat_activity a JOIN pg_stat_ssl s USING (pid) WHERE a.usename IN ('crm_app', 'crm_drain') ORDER BY a.usename;"

if docker compose --env-file runtime.env -f compose.yaml exec -T \
  -e PGPASSWORD=crm_app postgres \
  psql --host postgres --username crm_app --dbname crm --command 'SELECT 1' \
  >/dev/null 2>&1; then
  echo 'development_database_password_is_still_valid' >&2
  exit 1
fi
echo 'database_role_passwords=rotated'

docker compose --env-file runtime.env -f compose.yaml exec -T postgres \
  psql --username postgres --dbname crm --tuples-only --no-align \
  --command "SELECT 'migrations=' || count(*) || ',latest=' || max(version) FROM schema_migrations;"

systemctl start savvy-crm-backup.service
systemctl is-active --quiet savvy-crm-backup.timer
latest_backup=$(find /var/lib/savvy-crm/backups -maxdepth 1 -type f -name 'crm-*.dump' -print | sort | tail -1)
test -n "$latest_backup"
test -s "$latest_backup"
test -s "${latest_backup}.sha256"
docker run --rm --volume /var/lib/savvy-crm/backups:/backups:ro postgres:17-alpine \
  pg_restore --list "/backups/$(basename "$latest_backup")" >/dev/null
echo 'backup_upload_and_archive_readability=verified'
