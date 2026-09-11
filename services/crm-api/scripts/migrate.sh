#!/bin/sh
# Apply each CRM migration exactly once. Postgres' docker-entrypoint init
# directory only runs for a brand-new volume, which is insufficient once this
# standalone product receives a later migration.
set -eu

: "${PGHOST:?PGHOST is required}"
: "${PGPORT:?PGPORT is required}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"
: "${PGDATABASE:?PGDATABASE is required}"

psql_base() {
  psql -v ON_ERROR_STOP=1 -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" "$@"
}

psql_base <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
  version text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);
SQL

# Development databases created before this runner exist already have the
# original initdb migration set applied but no tracking rows. Mark only that
# known baseline; anything later is then applied normally below. A new,
# empty database has no accounts table and executes every migration.
if [ "$(psql_base -tAc "SELECT to_regclass('public.accounts') IS NOT NULL")" = "t" ] && \
   [ "$(psql_base -tAc "SELECT count(*) FROM schema_migrations")" = "0" ]; then
  for migration in /migrations/000[1-9]_*.sql /migrations/001[0-1]_*.sql; do
    [ -f "$migration" ] || continue
    version="$(basename "$migration" .sql)"
    psql_base -c "INSERT INTO schema_migrations (version) VALUES ('$version') ON CONFLICT DO NOTHING"
  done
fi

for migration in /migrations/[0-9]*.sql; do
  [ -f "$migration" ] || continue
  version="$(basename "$migration" .sql)"
  if [ "$(psql_base -tAc "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '$version')")" = "t" ]; then
    continue
  fi
  echo "Applying CRM migration $version"
  psql_base -f "$migration"
  psql_base -c "INSERT INTO schema_migrations (version) VALUES ('$version')"
done

# The original RLS migration creates login roles with development-only
# passwords so local databases can boot without extra configuration. A real
# deployment must rotate both roles after every migration run. Requiring the
# pair together prevents a partially secured database, and psql variables keep
# the generated values out of SQL string construction and command output.
if [ -n "${CRM_DB_APP_PASSWORD:-}" ] || [ -n "${CRM_DB_DRAIN_PASSWORD:-}" ]; then
  : "${CRM_DB_APP_PASSWORD:?CRM_DB_APP_PASSWORD is required when rotating database roles}"
  : "${CRM_DB_DRAIN_PASSWORD:?CRM_DB_DRAIN_PASSWORD is required when rotating database roles}"

  psql_base \
    -v app_password="$CRM_DB_APP_PASSWORD" \
    -v drain_password="$CRM_DB_DRAIN_PASSWORD" <<'SQL'
ALTER ROLE crm_app PASSWORD :'app_password';
ALTER ROLE crm_drain PASSWORD :'drain_password';
SQL
fi
