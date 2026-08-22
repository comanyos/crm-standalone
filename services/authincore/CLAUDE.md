# AuthinCore

Unified Go IAM platform (Kratos + Zitadel + Keycloak + authentik + FusionAuth merits). See [ARCHITECTURE.md](./ARCHITECTURE.md) for the full design.

## Stack

- **Backend**: Go 1.23, connect-go (single proto → gRPC + REST + gRPC-Web), pgx (Postgres), Redis, NATS
- **Admin UI**: React 18 + TypeScript + Vite + Tailwind in `admin-ui/`
- **Proto**: `buf` for lint/generate (`buf.yaml`, `buf.gen.yaml`), generated code in `gen/`
- **Migrations**: SQL files in `migrations/`

## Architecture rules (enforce in review)

- **CQRS**: writes go through `internal/command/`, reads through `internal/query/`. Never mix.
- **Events are audit, relational is system-of-record**: every command writes the relational row AND appends to the `events` table in the *same transaction*. Never dual-write outside a tx.
- `internal/domain/` is pure — zero I/O, no imports of storage/api packages.
- API handlers (`internal/api/`) are thin transport only — no business logic.
- Two API planes: public/self-service (unauthenticated) vs admin (machine token + RBAC). Never expose admin operations on the public plane.
- Tenancy: instance → org resolution happens in `internal/tenancy/` from Host header or JWT — never trust a tenant ID from the request body.
- JS execution (actions, policy expressions) only via the goja sandbox with timeout enforcement.

## Common commands

```bash
make docker-up        # Postgres + Redis + NATS
make migrate          # apply migrations
make run              # server on :8080
make ci               # fmt + vet + lint + test + build (run before any commit)
make test             # unit tests
make test-int         # integration tests (needs docker-up)
make proto            # regenerate from proto/ via buf
make admin-dev        # admin UI dev server
```

## Conventions

- Standard Go style; run `make fmt` / rely on the post-edit gofmt hook.
- Table-driven tests; integration tests separated via build tags (`make test-int`).
- New auth strategy: implement `flow.Strategy` in `internal/flow/strategy/<name>/`, register in `cmd/authincore/main.go`, add credential migration. See ARCHITECTURE.md.
- New action trigger: constant in `internal/actions/runner.go`, then `runner.Fire(...)` at the trigger point.
- Migrations are append-only — never edit an applied migration; add a new one.
- Webhooks must be HMAC-SHA256 signed (`internal/webhook/`).

## Security-sensitive areas (always involve security-reviewer agent)

- `internal/token/` (JWT minting, PATs), `internal/session/`, `internal/protocol/oidc/`, `internal/protocol/saml/`
- `internal/flow/strategy/` (password, webauthn, totp, code, oidc)
- `internal/actions/` and `internal/policy/engine/` (sandboxed JS)
- Anything touching credential storage in `internal/identity/`
