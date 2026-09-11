.PHONY: up down logs ps test verify bootstrap test-db-up test-db-down backup-local restore-local

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

ps:
	docker compose ps

test:
	cd services/crm-api && go test ./...
	npm run test --workspace=@crm/web

verify:
	docker compose config --quiet
	@test -z "$$(git ls-files -- services/crm-api/bin/crm)"

# Example: AUTH_BOOTSTRAP_ORG_NAME=Acme \
# AUTH_BOOTSTRAP_EMAIL=admin@example.com make bootstrap
# The CRM performs an idempotent startup bootstrap; passwordless users have no
# application password to create or reset.
bootstrap:
	@test -n "$${AUTH_BOOTSTRAP_ORG_NAME}" || (echo "AUTH_BOOTSTRAP_ORG_NAME is required" && exit 1)
	@test -n "$${AUTH_BOOTSTRAP_EMAIL}" || (echo "AUTH_BOOTSTRAP_EMAIL is required" && exit 1)
	docker compose up -d --build crm-api

# Disposable Postgres instances for integration tests only — separate ports,
# tmpfs storage, never the development databases. See CONTRIBUTING.md.
test-db-up:
	docker compose --profile test up -d crm-postgres-test

test-db-down:
	docker compose --profile test rm -sf crm-postgres-test

# Encrypted logical local backups only. See docs/BACKUP_RESTORE.md; callers
# must provide an age recipient and an existing protected output directory.
backup-local:
	./scripts/backup-compose.sh

# Destructive local restore only. The script requires an exact acknowledgement
# and encrypted archive inputs; see docs/BACKUP_RESTORE.md.
restore-local:
	./scripts/restore-compose.sh
