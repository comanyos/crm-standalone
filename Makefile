.PHONY: dev-key auth-ui up down logs ps test verify bootstrap reset-admin-password test-db-up test-db-down backup-local restore-local

dev-key:
	mkdir -p deploy/secrets
	@test -f deploy/secrets/jwt_private_key.pem || openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out deploy/secrets/jwt_private_key.pem
	chmod 600 deploy/secrets/jwt_private_key.pem

up: dev-key
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f

ps:
	docker compose ps

auth-ui:
	cd services/authincore/hosted-ui && npm ci && npm run build

test: auth-ui
	cd services/crm-api && go test ./...
	cd services/authincore && go test ./...

# Validation only needs a syntactically valid CRM_WEB_AUTH_ORG_ID, so fall back
# to a placeholder on a fresh clone where `make bootstrap` has not run yet. A
# real value in the environment or .env still wins, and compose.yaml's `:?`
# guard still fails a real `make up` that is genuinely missing it.
verify:
	CRM_WEB_AUTH_ORG_ID="$${CRM_WEB_AUTH_ORG_ID:-00000000-0000-0000-0000-000000000000}" \
	  docker compose config --quiet
	@test ! -e services/crm-api/bin/crm
	@test ! -e services/authincore/authincore

# Example: BOOTSTRAP_ORG_NAME=Acme BOOTSTRAP_ORG_DOMAIN=localhost \
# BOOTSTRAP_ADMIN_EMAIL=admin@localhost BOOTSTRAP_ADMIN_PASSWORD='change-me-now' make bootstrap
bootstrap:
	docker compose --profile tools run --rm auth-bootstrap

# Administrator password recovery. Deliberately not part of `bootstrap`:
# re-running bootstrap must never change an existing password, so this is the
# only path that does. RESET_ADMIN_CONFIRM must equal RESET_ADMIN_EMAIL.
# Example: RESET_ORG_DOMAIN=localhost RESET_ADMIN_EMAIL=admin@localhost \
# RESET_ADMIN_CONFIRM=admin@localhost RESET_ADMIN_PASSWORD='new-password-here' \
# make reset-admin-password
reset-admin-password:
	docker compose --profile tools run --rm auth-reset-password

# Disposable Postgres instances for integration tests only — separate ports,
# tmpfs storage, never the development databases. See CONTRIBUTING.md.
test-db-up:
	docker compose --profile test up -d auth-postgres-test crm-postgres-test

test-db-down:
	docker compose --profile test rm -sf auth-postgres-test crm-postgres-test

# Encrypted logical local backups only. See docs/BACKUP_RESTORE.md; callers
# must provide an age recipient and an existing protected output directory.
backup-local:
	./scripts/backup-compose.sh

# Destructive local restore only. The script requires an exact acknowledgement
# and encrypted archive inputs; see docs/BACKUP_RESTORE.md.
restore-local:
	./scripts/restore-compose.sh
