# Contributing

This repository is a standalone, multi-tenant CRM. `services/authincore` owns
identity/auth/FGA, `services/crm-api` owns CRM data and workflows, `apps/web`
owns the browser application. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
for the service boundary and [task.md](task.md) for the full delivery plan.

## Prerequisites

- Go 1.26 or newer (`go version`)
- Docker and Docker Compose v2 (`docker compose version`)
- Node.js 20+ and npm — this repo is an npm workspace (`apps/*`, `packages/*`);
  run `npm install` at the repo root once, not inside individual packages
- `openssl` (used by `make dev-key` to generate a local JWT signing key)
- Optional: [`buf`](https://buf.build) and the `protoc-gen-go` /
  `protoc-gen-connect-go` plugins, only if regenerating Connect/protobuf code

Do not install or rely on anything from a sibling CompanyOS workspace. This
repository must build and run using only what is checked in here plus the
tools above.

## First-time setup

```sh
cp .env.example .env      # edit with local-only values; never commit real secrets
make dev-key               # generates deploy/secrets/jwt_private_key.pem (gitignored)
make up                     # builds and starts the Compose stack
make bootstrap               # creates the first org/admin/roles/crm-web client (see README.md)
```

Then check `http://localhost:18080/healthz` (AuthinCore), `http://localhost:13001`
(Auth Admin UI), and `http://localhost:19431` (CRM API).

## Local commands

| Command | What it does |
| --- | --- |
| `make up` / `make down` / `make logs` / `make ps` | Manage the local Compose stack |
| `make bootstrap` | Idempotently reconcile the first org/admin/roles/OAuth client |
| `make test` | Run the Go test suites for `crm-api` and `authincore` |
| `make verify` | Validate Compose config and confirm no build artifacts are tracked |
| `make test-db-up` / `make test-db-down` | Start/stop disposable, `tmpfs`-backed Postgres instances (separate ports, separate from the dev databases) for integration tests |
| `cd services/crm-api && make ci` | `fmt` + `vet` + `build` + `test` for the CRM service |
| `cd services/authincore && make lint` / `make test` | Lint and test AuthinCore |
| `npm run build/test/lint --workspace=@crm/web` | Build/unit-test/lint the web app |
| `npm run test:e2e --workspace=@crm/web` | Playwright browser tests — needs the backend stack up and bootstrapped; see `apps/web/README.md` |

See [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for the full
development/test/staging/production environment matrix, required environment
variables per service, and secret ownership/rotation. Both services refuse to
start in staging/production with a development-default password, a non-`https`
issuer URL, or (AuthinCore) an ephemeral signing key — `APP_ENV` unset or
`development` runs exactly as it does today.

Each service also has its own `Makefile` with finer-grained targets
(`fmt`, `vet`, `tidy`, `generate`, `build`, `run`).

## Changing CRM roles or scopes

Three places have to move together, and each has a test that fails if it does
not:

1. `packages/contracts/rbac/crm-roles.json` — the source of truth for which
   scopes each built-in role grants, with a written rationale per role.
2. `services/authincore/cmd/authinctl/bootstrap.go` — provisions those roles
   into an organization. Pinned by `bootstrap_roles_test.go`.
3. `services/crm-api/internal/server/scopes.go` — maps every procedure to the
   scope it demands. Pinned by `role_matrix_test.go`, which also asserts each
   role reaches exactly its intended procedures.

The two services are separate Go modules and cannot share a package, so the
JSON file plus a test on each side is what keeps them agreeing. A scope
granted by one and named differently by the other is invisible at build time
and surfaces as one role being denied a screen it should have.

Adding an RPC additionally requires a `scopes.go` entry: the policy denies any
procedure it has no decision for, and `server.New` refuses to start when the
catalog does not cover everything the generated descriptors serve.

Roles are reconciled, so re-running `make bootstrap` against an existing
organization applies scope changes to its existing roles without touching
identities or credentials.

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

   These tests default to the instances `make test-db-up` starts, so no
   environment variable is needed: `crm-postgres-test` for `services/crm-api`
   (override with `TEST_DATABASE_URL`) and `auth-postgres-test` for
   `services/authincore`'s bootstrap suite (override with
   `AUTH_TEST_DATABASE_URL`; that suite applies AuthinCore's migrations
   itself). Note that a database they cannot reach is a
   `t.Skip`, not a failure — so if `make test-db-up` was never run, the whole
   tier passes without executing. Check for `--- SKIP` in `go test -v` output
   before treating a green run as proof. The connection must be a superuser:
   migration `0003_rls_enforce.sql` creates `crm_app` and `crm_drain`, and the
   tenant-isolation tests connect as both to prove row-level security applies
   to the role the service actually uses.

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
   `contacts.spec.ts` cover the account/contact journeys. Not yet wired into
   CI (they need the full stack up and bootstrapped, unlike the other CI
   tiers). Other user journeys are added as their screens land.

   Two things to know before adding a spec here. `auth.setup.ts` signs in
   once per run and every other spec reuses that session via
   `storageState` — AuthinCore rate-limits password logins per IP, so a spec
   that logs in itself will start failing runs at sign-in. And the suite runs
   single-worker on purpose: its tests all act as one identity inside one
   tenant and contend when parallelised.
4. **Manual/local verification** — used for the OIDC PKCE flow's consent and
   token-revocation edge cases beyond what `auth.spec.ts` automates.

CI (`.github/workflows/ci.yml`) currently runs tier 1 (formatting, linting,
unit tests, `apps/web`'s build) plus Compose config validation. Tier 2
(integration) and the `apps/web` browser-test tier are not yet wired into
CI — do not claim a milestone complete on the strength of tier 1 alone.

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
- `services/authincore/third_party/platform-contract` is a pinned,
  source-compatible snapshot of an external contract module, wired in via a
  local Go `replace` directive. Do not point that (or any) `replace`
  directive at a path outside this repository.

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
  target) before committing. Both services lint with
  [`golangci-lint`](https://golangci-lint.run) using each service's
  `.golangci.yml` — run `golangci-lint run ./...` from within
  `services/crm-api` or `services/authincore` before opening a change.
- **Web**: TypeScript strict mode, ESLint (flat config, `typescript-eslint` +
  `react-hooks` + `jsx-a11y` — the last one enforces the WCAG 2.2 AA
  baseline), and Prettier. `apps/web/eslint.config.js` and
  `.prettierrc.json` are the reference config; match them for new web code
  (`services/authincore/admin-ui`/`hosted-ui` predate this and have no lint
  config of their own — don't copy that gap into new code).
- CI (`.github/workflows/ci.yml`) runs `apps/web`'s build (`tsc -b` + `vite
  build`), which fails on type errors but not on lint warnings; run `npm run
  lint --workspace=@crm/web` locally before opening a change.

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
