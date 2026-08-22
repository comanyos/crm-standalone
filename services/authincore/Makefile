.PHONY: build run dev dev-services dev-stop dev-ui test migrate lint proto fmt docker-up docker-down

BINARY     := authincore
OUTPOST    := outpost
CTL        := authinctl
BUILD_DIR  := ./bin

## Build all binaries (OSS)
build:
	go build -o $(BUILD_DIR)/$(BINARY)  ./cmd/authincore
	go build -o $(BUILD_DIR)/$(OUTPOST) ./cmd/outpost
	go build -o $(BUILD_DIR)/$(CTL)     ./cmd/authinctl

## Build enterprise binaries (with SAML, SCIM, multi-org, custom branding)
build-enterprise:
	go build -tags enterprise -o $(BUILD_DIR)/$(BINARY)-enterprise  ./cmd/authincore
	go build -tags enterprise -o $(BUILD_DIR)/$(OUTPOST)-enterprise ./cmd/outpost
	go build -tags enterprise -o $(BUILD_DIR)/$(CTL)-enterprise     ./cmd/authinctl

## Run the server (requires docker-up)
run:
	go run ./cmd/authincore

## Start backing services (Postgres + Redis only)
dev-services:
	docker compose -f docker-compose.dev.yml up -d
	@echo "Waiting for Postgres..."
	@docker compose -f docker-compose.dev.yml exec postgres sh -c 'until pg_isready -U authincore; do sleep 1; done'
	@echo "Services ready."

## Stop dev backing services
dev-stop:
	docker compose -f docker-compose.dev.yml down

## Run server with .env.local (OSS mode)
dev:
	@export $$(cat .env.local | grep -v '^#' | xargs) && go run ./cmd/authincore

## Run server in enterprise mode with .env.local
dev-enterprise:
	@export $$(cat .env.local | grep -v '^#' | xargs) && go run -tags enterprise ./cmd/authincore

## Start admin UI dev server
dev-ui:
	cd admin-ui && npm run dev

## Run tests
test:
	go test ./... -count=1 -race -timeout=120s

## Run tests with coverage
test-coverage:
	go test ./... -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## Run linter
lint:
	golangci-lint run ./...

## Format code
fmt:
	gofmt -s -w .
	goimports -w .

## Generate proto stubs (requires buf)
proto:
	buf generate

## Apply migrations (requires goose)
migrate:
	goose -dir ./migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir ./migrations postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir ./migrations postgres "$(DATABASE_URL)" status

## Start local infra (Postgres + Redis)
docker-up:
	docker compose up -d

## Stop local infra
docker-down:
	docker compose down

## Full reset (drop + recreate DB)
reset:
	docker compose down -v
	docker compose up -d
	sleep 3
	$(MAKE) migrate

## Tidy deps
tidy:
	go mod tidy

## Vet
vet:
	go vet ./...

## All checks (CI)
ci: fmt vet lint test build

## Admin UI
.PHONY: admin-install admin-dev admin-build
admin-install:
	cd admin-ui && npm install

admin-dev:
	cd admin-ui && npm run dev

admin-build:
	cd admin-ui && npm run build

## -----------------------------------------------------------------------
## Test suite targets (added by test suite generation)
## -----------------------------------------------------------------------
.PHONY: test-cover test-fuzz test-int sec load load-tenant scan audit

## Run tests with coverage profile and open HTML report
test-cover:
	go test ./... -race -count=1 -coverprofile=coverage.out -coverpkg=./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@go tool cover -func=coverage.out | tail -1

## Run each fuzz target for 30 seconds (requires Go 1.18+)
test-fuzz:
	go test -fuzz=FuzzLoginBody   ./test/fuzz/ -fuzztime=30s
	go test -fuzz=FuzzSCIMFilter  ./test/fuzz/ -fuzztime=30s
	go test -fuzz=FuzzOAuthParams ./test/fuzz/ -fuzztime=30s

## Run integration tests (requires DATABASE_URL env var)
test-int:
	go test -tags=integration ./test/integration/... -race -count=1 -timeout=300s -v

## Run gosec + govulncheck security checks
sec:
	gosec -quiet ./...
	govulncheck ./...

## k6 login storm load test
load:
	k6 run test/load/login_storm.js

## k6 multi-tenant load test
load-tenant:
	k6 run test/load/tenant_load.js

## Trivy image vulnerability scan
scan:
	trivy image --exit-code 1 --severity HIGH,CRITICAL authincore:latest

## Full quality audit: lint + security + image scan
audit: lint sec scan
