.PHONY: dev-key auth-ui up down logs ps test verify bootstrap test-db-up test-db-down

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

verify:
	docker compose config --quiet
	@test ! -e services/crm-api/bin/crm
	@test ! -e services/authincore/authincore

# Example: BOOTSTRAP_ORG_NAME=Acme BOOTSTRAP_ORG_DOMAIN=localhost \
# BOOTSTRAP_ADMIN_EMAIL=admin@localhost BOOTSTRAP_ADMIN_PASSWORD='change-me-now' make bootstrap
bootstrap:
	docker compose --profile tools run --rm auth-bootstrap

# Disposable Postgres instances for integration tests only — separate ports,
# tmpfs storage, never the development databases. See CONTRIBUTING.md.
test-db-up:
	docker compose --profile test up -d auth-postgres-test crm-postgres-test

test-db-down:
	docker compose --profile test rm -sf auth-postgres-test crm-postgres-test
