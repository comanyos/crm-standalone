# AuthinCore Architecture

AuthinCore is a unified Go IAM platform that combines the best of:

| Platform | Merits Taken |
|---|---|
| **Kratos** (Ory) | Headless API-first, self-service flow state machines, JSON-schema identity traits, pluggable credential types |
| **Zitadel** | Event-sourced audit log (CQRS), instance→org→project→app hierarchy, Actions (custom hooks), hybrid relational+event persistence |
| **Keycloak** | Realm isolation (Org), UMA fine-grained authz, client scopes/protocol mappers, identity federation |
| **authentik** | Declarative blueprints, composable policy engine (expression/group/reputation), outpost proxy architecture |
| **FusionAuth** | Application-scoped tenancy, rich user search DSL, webhooks + lambda functions, themed UI |

## Persistence Decision

**Relational tables (Postgres) are the system of record for reads.**
Events (in `events` table) are written in the same transaction as the relational write — providing atomic audit trail and replay without dual-write risk. This is the same direction Zitadel moved to.

## Package Layout

```
internal/
  domain/          Pure entities, zero I/O (identity, session, org, flow, errors)
  eventstore/      Append-only audit log (Pusher/Querier interfaces + postgres impl)
  command/         CQRS write side — validates, writes relational SoR, appends events
  query/           CQRS read side — relational projections, search
  flow/            Self-service flow engine (login/registration/recovery/verification/settings)
    strategy/      Pluggable auth methods (password, oidc, totp, webauthn, code)
  identity/        JSON-schema trait validation, credential storage
  session/         Session domain
  token/           JWT minting + PAT generation
  protocol/oidc/   OIDC/OAuth2 provider endpoints
  protocol/saml/   SAML IdP
  idp/             Upstream federation/brokering
  policy/engine/   Authentik-style policy evaluation (expression/group/reputation via goja)
  authz/           UMA/RBAC permission checks
  actions/         Custom JS hook runner (goja sandbox, timeout enforced)
  webhook/         Outbound webhook dispatcher with HMAC-SHA256 signing
  courier/         Async email/SMS delivery (recovery codes, verification)
  blueprint/       Declarative YAML config apply/diff (authentik blueprints)
  tenancy/         instance→org resolution from Host header or JWT
  storage/
    database/      pgx pool
    cache/         Redis (sessions, flow state, rate limits)
  api/
    interceptor/   authn, tenant, audit connect-go interceptors
    http/          HTTP server wiring + OIDC/flow endpoints
```

## API

- **Protocol**: connect-go (single proto → gRPC + REST + gRPC-Web)
- **Transport**: thin handlers only — no business logic in handlers
- **Two planes**: public/self-service (no auth) vs admin/management (machine token + RBAC)

## Running locally

```bash
make docker-up       # start Postgres + Redis + NATS
make migrate         # apply migrations
make run             # start server on :8080
```

## Adding a new auth strategy

1. Implement `flow.Strategy` interface in `internal/flow/strategy/<name>/strategy.go`
2. Register it in `cmd/authincore/main.go` via `flow.NewManager(repo, strategy1, strategy2, ...)`
3. Add a migration for any credential storage needed

## Adding a new action trigger

1. Add trigger constant to `internal/actions/runner.go`
2. Call `runner.Fire(ctx, orgID, "your.trigger", payload)` at the trigger point
3. Users configure bindings via `ActionService.BindExecution`
