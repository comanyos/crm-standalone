# Contributing

This repository is a standalone, multi-tenant CRM. `services/crm-api` owns
native authentication, tenant authorization, CRM data, and workflows;
`apps/web` owns the browser application. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the boundary and
[task.md](task.md) for the full delivery plan.

## Prerequisites

- Go 1.26 or newer (`go version`)
- Docker and Docker Compose v2 (`docker compose version`)
- Node.js 22+ and npm — this repo is an npm workspace (`apps/*`, `packages/*`);
  run `npm install` at the repo root once, not inside individual packages
- Optional: [`buf`](https://buf.build) and the `protoc-gen-go` /
  `protoc-gen-connect-go` plugins, only if regenerating Connect/protobuf code

Do not install or rely on anything from a sibling CompanyOS workspace. This
repository must build and run using only what is checked in here plus the
tools above.

## First-time setup

```sh
cp .env.example .env      # edit with local-only values; never commit real secrets
make bootstrap             # creates the first organization/owner membership
make up                    # builds and starts the complete Compose stack
```

Then check `http://localhost:19431/healthz` (CRM API) and the configured CRM
web origin.

## Local commands

| Command | What it does |
| --- | --- |
| `make up` / `make down` / `make logs` / `make ps` | Manage the local Compose stack |
| `make bootstrap` | Idempotently create the first organization and owner membership |
| `make test` | Run the CRM API and web unit tests |
| `make verify` | Validate Compose config and confirm no build artifacts are tracked |
| `make test-db-up` / `make test-db-down` | Start/stop disposable, `tmpfs`-backed Postgres instances (separate ports, separate from the dev databases) for integration tests |
| `cd services/crm-api && make ci` | `fmt` + `vet` + `build` + `test` for the CRM service |
| `npm run build/test/lint --workspace=@crm/web` | Build/unit-test/lint the web app |
| `npm run test:e2e --workspace=@crm/web` | Playwright browser tests — needs the backend stack up and bootstrapped; see `apps/web/README.md` |

See [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for the full
development/test/staging/production environment matrix, required environment
variables per service, and secret ownership/rotation. The CRM refuses to start
in staging/production with development database passwords, a weak OTP pepper,
insecure cookies, or non-HTTPS public origins.

Each service also has its own `Makefile` with finer-grained targets
(`fmt`, `vet`, `tidy`, `generate`, `build`, `run`).

## Changing CRM roles or scopes

Two places have to move together, and tests fail if they do not:

1. `packages/contracts/rbac/crm-roles.json` — the source of truth for which
   scopes each built-in role grants, with a written rationale per role.
2. `services/crm-api/internal/server/scopes.go` — maps every procedure to the
   scope it demands. Pinned by `role_matrix_test.go`, which also asserts each
   role reaches exactly its intended procedures.

Native auth resolves a membership's role and maps it to this fixed contract
server-side. A browser or signup profile can never supply its own role/scopes.

Adding an RPC additionally requires a `scopes.go` entry: the policy denies any
procedure it has no decision for, and `server.New` refuses to start when the
catalog does not cover everything the generated descriptors serve.

Role-to-scope changes apply on the next authenticated request because scopes
are derived from the current membership rather than copied into a long-lived
token.

## Test tiers

1. **Unit tests** (`go test ./...` within each service) — domain invariants,
   validation, authorization decisions, parsing. Fast, no external
   dependencies, run on every change.
2. **Integration tests** — exercise real PostgreSQL migrations, row-level
   security, and Connect/RPC endpoints. Run `make test-db-up` first to start
   the disposable test databases (see
   [docs/CONFIGURATION.md](docs/CONFIGURATION.md)); these are not yet fully
   separated from unit tests in `go test ./...`, so treat any test that opens
   a database connection as this tier.

   These tests default to the `crm-postgres-test` instance that
   `make test-db-up` starts (override with `TEST_DATABASE_URL`). Note that a
   database they cannot reach is a
   `t.Skip`, not a failure — so if `make test-db-up` was never run, the whole
   tier passes without executing. Check for `--- SKIP` in `go test -v` output
   before treating a green run as proof. The connection must be a superuser:
   migration `0003_rls_enforce.sql` creates `crm_app` and `crm_drain`, and the
   tenant-isolation tests connect as both to prove row-level security applies
   to the role the service actually uses.

   **This tier runs in CI.** The `crm-api` job starts the same disposable
   instance `make test-db-up` does and sets
   `REQUIRE_LIVE_POSTGRES=1`, which turns "the database is unreachable" from a
   skip into a failure. That matters because a skip is indistinguishable from
   a pass in every summary anyone reads: before CI started a database, every
   PostgreSQL test in the repository skipped on every pull request while the
   run reported green. Locally the same tests still skip, so a contributor
   without the test instance running is not blocked — run `go test -v` and
   look for SKIP if you need to know which tier you actually exercised, or set
   `REQUIRE_LIVE_POSTGRES=1` yourself to be told.

   Which database a test connects to, and what happens when it cannot, are
   both decided in `internal/pgtest` so the two packages below cannot drift
   onto different instances or different policies.

   Two packages now use this database: `internal/domain` and, for the revenue
   workflows end to end, `internal/server`. `go test ./...` runs them in
   separate processes at the same time, so they coordinate over one Postgres
   advisory lock — `internal/domain` owns the schema and takes the lock
   exclusively while it applies migrations (once per test binary);
   `internal/server` runs no DDL of its own and holds the same lock in shared
   mode for each of its Postgres-backed tests. Without that, the migrations'
   `ACCESS EXCLUSIVE` locks deadlock at random against the other package's
   queries. A third package joining this tier must take the shared lock the
   same way — see `migrationLockKey` in either file.
3. **Browser tests** — end-to-end coverage of critical user journeys, in
   `apps/web/tests-e2e/`, run via `npm run test:e2e --workspace=@crm/web`
   against a live backend. `auth.spec.ts` covers sign-in, the protected
   dashboard, reload-while-valid, and sign-out; `accounts.spec.ts` and
   `contacts.spec.ts` cover the account/contact journeys. CI builds and starts
   a disposable backend stack, bootstraps its own tenant, and runs both the
   desktop and mobile Chromium projects. Other user journeys are added as
   their screens land.

   Two things to know before adding a spec here. `auth.setup.ts` signs in
   once per run and every other spec reuses that session via
   `storageState` — native auth rate-limits OTP requests, so a spec that logs
   in itself can exhaust the test account's window. Tests use unique
   fixture names and wait for writes to settle so the desktop projects can run
   in parallel without cancelling each other's requests.
4. **Manual/local verification** — used for real inbox delivery and Google
   consent/account-linking edge cases beyond what `auth.spec.ts` automates.

CI (`.github/workflows/ci.yml`) runs tier 1, the PostgreSQL-backed tier 2,
the Playwright browser tier, dependency vulnerability scans, and Compose
config validation.

## Generated-code policy

- Connect/protobuf code under `services/*/gen/` is generated with `buf
  generate` from `services/*/proto/`. Never hand-edit files under `gen/`;
  change the `.proto` source and regenerate instead.
- Regenerate and commit generated code in the same change as the proto edit
  that produced it — do not let `gen/` drift from `proto/`.
- `packages/contracts` holds the generated TypeScript/Connect-ES client for
  `apps/web` (`@bufbuild/protoc-gen-es` v2 — it emits both message types and
  service descriptors, so no separate `protoc-gen-connect-es` plugin is
  needed). Regenerate with `npm run generate --workspace=@crm/contracts`
  after editing `services/crm-api/proto/*.proto`. Never hand-duplicate
  request/response shapes in UI code instead.

## Migration policy

- Migrations are append-only. Once a migration has shipped (merged to `main`),
  never edit it — write a new migration to change behavior.
- Each migration must be reversible in intent (forward/rollback-safe) and
  should not assume it runs against an empty database.
- New tenant-owned tables must ship in the same migration (or an immediately
  following one) as their `tenant_id` column, tenant-aware indexes/keys, and
  `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY` statements. See
  `services/crm-api/migrations/0003_rls_enforce.sql` for the existing pattern.
- Run migrations against a disposable database in CI/local testing (`make
  test-db-up`), never directly against a shared or production database as
  part of development.

## Formatting and linting

- **Go**: `gofmt` is authoritative; run `make fmt` (or the service's `fmt`
  target) before committing. Run `golangci-lint run ./...` from
  `services/crm-api` before opening a change.
- **Web**: TypeScript strict mode, ESLint (flat config, `typescript-eslint` +
  `react-hooks` + `jsx-a11y` — the last one enforces the WCAG 2.2 AA
  baseline), and Prettier. `apps/web/eslint.config.js` and
  `.prettierrc.json` are the reference config; match them for new web code.
- CI (`.github/workflows/ci.yml`) runs `apps/web`'s build (`tsc -b` + `vite
  build`), tests, and lint as blocking checks.

## Before opening a change

- Run the relevant `make test` / `make ci` / `golangci-lint run` locally.
- Never commit `.env`, `deploy/secrets/`, database dumps, provider exports, or
  any real credential, token, or private key.
- If your change touches tenancy, RLS, authorization, or a public API
  contract, add or update the tests that prove isolation/authorization, not
  just the happy path.
- Update `task.md` and `docs/ROADMAP.md` only for checklist items whose
  stated acceptance criteria you actually verified — see the progress-update
  rule at the bottom of `task.md`.
