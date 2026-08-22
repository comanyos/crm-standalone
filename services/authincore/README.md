# AuthinCore

**Open-source, self-hosted Identity and Access Management (IAM) for any Go application.**

AuthinCore handles everything authentication and authorization — so your app doesn't have to.
One `docker compose up` and your app has login, MFA, SSO, fine-grained permissions, and more.

---

## Why AuthinCore

| Problem | AuthinCore's answer |
|---------|-------------------|
| Building auth from scratch takes months | Drop-in IAM — connect any app in under a day |
| Keycloak is 500MB and takes 60s to start | Single Go binary, ~12MB, starts in 50ms |
| Auth0 / Clerk charge per user | Self-hosted — 1 user or 1 million, same cost |
| Roles aren't granular enough | Built-in Zanzibar-style FGA — per-resource permissions |
| Vendor lock-in | MIT license, you own everything |

---

## Features

### Authentication
- **Password** — Argon2id hashing (stronger than bcrypt)
- **Magic Link** — passwordless email login
- **Social Login** — Google, GitHub, any OIDC provider
- **TOTP** — Google Authenticator / Authy with QR enrollment + backup codes
- **SMS MFA** — Twilio or Vonage
- **Passkeys / WebAuthn** — fingerprint, Face ID, hardware key

### Protocols
- **OAuth2 / OIDC** — Authorization Code + PKCE, Client Credentials, Refresh Tokens
- **SAML SP** — connect to Okta, Entra ID, Google Workspace
- **SAML IdP** — AuthinCore as the SSO authority for other apps
- **SCIM 2.0** — auto-provision users from Okta / Azure AD

### Authorization
- **RBAC** — roles in JWT, checked without a round-trip
- **Fine-Grained Authorization (FGA)** — Zanzibar / ReBAC model
  - `lead:123 editor user:alice` — Alice can edit this specific lead
  - `deal:456 viewer group:sales#member` — all sales members can view this deal
  - Relation inheritance (`owner → editor → viewer`)
  - Group expansion
  - `POST /authz/check` — real-time permission check

### Platform
- **Multi-tenant** — Instance → Org → Project → App hierarchy, complete DB isolation
- **Self-service flows** — registration, login, recovery, settings, verification, MFA enrollment
- **Sessions** — list, revoke single, revoke all
- **Service Accounts** — machine identities for background jobs and microservices
- **Webhooks** — `user.created`, `user.login`, `session.revoked`, and more
- **Audit Log** — every action stored permanently via hybrid event sourcing
- **Admin UI** — basic visual management (port 3001)
- **gRPC API** — typed microservice calls via connect-go (port 8081)

---

## Quick Start

**Requirements:** Docker, Docker Compose

```bash
git clone https://github.com/YOUR_USERNAME/authincore.git
cd authincore

# Start all services (Postgres, Redis, NATS, AuthinCore, Admin UI)
docker compose up -d

# Run migrations
docker exec authincore-authincore-1 /authincore migrate

# Verify
curl http://localhost:8080/healthz
# {"status":"ok"}

curl http://localhost:8080/readyz
# {"status":"ok"}
```

**Services running after startup:**

| Service | URL | What it is |
|---------|-----|------------|
| AuthinCore API | http://localhost:8080 | Main REST + OIDC server |
| Admin UI | http://localhost:3001 | Visual management |
| gRPC | localhost:8081 | connect-go typed API |
| Postgres | localhost:5432 | Primary database |
| Redis | localhost:6380 | Sessions + rate limiting |
| NATS | localhost:4222 | Message bus |

---

## Connect Your App in 3 Steps

### Step 1 — Register your app as an OAuth2 client

```bash
curl -X POST http://localhost:8080/api/v1/apps \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "My App",
    "type": "confidential",
    "redirect_uris": ["https://myapp.com/callback"],
    "grant_types": ["authorization_code", "refresh_token"]
  }'
```

### Step 2 — Add JWT middleware to your Go app

```go
import "github.com/authincore/authincore/internal/auth"

ac := auth.New("http://localhost:8080")

mux.Handle("/api/me",     ac.RequireAuth(handler.Me))
mux.Handle("/api/admin",  ac.RequireRole("admin")(handler.Admin))
mux.Handle("/api/secure", ac.RequireMFA(handler.Secure))
```

### Step 3 — Read identity from every request

```go
claims := auth.FromContext(r.Context())
claims.Subject   // user UUID
claims.OrgID     // which org
claims.Email     // email
claims.Roles     // ["admin", "member"]
claims.AAL       // 1=password only, 2=MFA verified
```

**JWT validation is local — no round-trip to AuthinCore on every request.**

---

## Fine-Grained Authorization (FGA)

Beyond roles — per-resource, per-user permissions.

The `/authz` endpoints are authenticated. The org is taken from the caller's
token (`org_id` claim) or the request's resolved tenant — never from the body —
so a token scoped to one org cannot read or modify another org's tuples.

```bash
# Grant alice editor access to lead:123
curl -X POST http://localhost:8080/authz/tuples \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "object_type": "lead", "object_id": "lead:123",
    "relation": "editor",
    "subject_type": "user", "subject_id": "alice-uuid"
  }'

# Check — "can alice view lead:123?"
curl -X POST http://localhost:8080/authz/check \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "object_type": "lead", "object_id": "lead:123",
    "relation": "viewer",
    "subject_type": "user", "subject_id": "alice-uuid"
  }'
# {"allowed": true}  ← alice is editor, editor implies viewer
```

Supports relation inheritance, group expansion, and strict cross-org isolation.
Admins can browse and edit tuples visually in the admin UI under **Authorization**.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        AuthinCore                           │
│                                                             │
│  REST / OIDC (8080)          gRPC / connect-go (8081)       │
│         │                           │                       │
│  ┌──────▼───────┐         ┌─────────▼────────┐             │
│  │  HTTP Server  │         │   gRPC Server    │             │
│  │  Flow Engine  │         │  Identity/Session│             │
│  │  OIDC/SAML/  │         │  Flow Services   │             │
│  │  SCIM/FGA    │         └──────────────────┘             │
│  └──────┬───────┘                                           │
│         │                                                   │
│  ┌──────▼──────────────────────────────────┐               │
│  │              CQRS Layer                 │               │
│  │   Commands (write)  │  Queries (read)   │               │
│  └──────┬──────────────────────────────────┘               │
│         │                                                   │
│  ┌──────▼──────┐  ┌──────────┐  ┌──────────┐              │
│  │  Postgres   │  │  Redis   │  │   NATS   │              │
│  │  (primary)  │  │(sessions)│  │ (events) │              │
│  └─────────────┘  └──────────┘  └──────────┘              │
└─────────────────────────────────────────────────────────────┘
```

- **CQRS** — writes and reads through separate code paths
- **Hybrid event sourcing** — relational tables as source of truth, `events` table for permanent audit
- **Multi-tenant** — `org_id` on every table, enforced at middleware level
- **Rotating JWT keys** — 2-key RS256 window, rotated every 24h

Full details: [docs/architecture.md](docs/architecture.md)

---

## Self-Service Flows

| Flow | Endpoint |
|------|---------|
| Registration | `POST /self-service/registration` |
| Login | `POST /self-service/login` |
| Password recovery | `POST /self-service/recovery` |
| Email verification | `POST /self-service/verification` |
| Change password / update profile | `POST /self-service/settings` |
| TOTP enrollment | `POST /self-service/settings` with `method: "totp"` |

Each flow is a state machine — `GET` the endpoint to get a flow ID, then `POST` with method + body.

---

## OIDC Endpoints

```
GET  /.well-known/openid-configuration   Discovery document
GET  /oauth2/keys                         JWKS (public signing keys)
GET  /oauth2/authorize                    Authorization
POST /oauth2/token                        Token exchange
POST /oauth2/introspect                   Token introspection
POST /oauth2/revoke                       Token revocation
GET  /oauth2/userinfo                     UserInfo
```

---

## Development

```bash
# Unit tests
go test ./... -count=1

# With race detector
go test ./... -race

# Integration tests (requires running Postgres)
DATABASE_URL=postgres://authincore:authincore@localhost:5432/authincore \
  go test -tags=integration ./test/integration/... -v

# Fuzz tests (60s)
go test ./test/fuzz/... -fuzz=FuzzLoginFlow -fuzztime=60s

# Lint
golangci-lint run ./...

# Build binary
make build
```

---

## Deployment

### Docker Compose

```bash
docker compose up -d
docker exec authincore-authincore-1 /authincore migrate
```

### Kubernetes (Helm)

```bash
helm install authincore ./charts/authincore \
  --set config.databaseUrl="postgres://..." \
  --set config.redisAddr="redis:6379" \
  --set config.issuer="https://auth.yourdomain.com"
```

The Helm chart includes an init container that runs migrations before startup, HPA, and liveness/readiness probes.

---

## Project Structure

```
authincore/
├── cmd/
│   ├── authincore/     # Main server binary
│   ├── outpost/        # Edge proxy (token validation at the edge)
│   └── authinctl/      # CLI tool
├── internal/
│   ├── api/http/       # REST handlers
│   ├── api/grpc/       # gRPC handlers (connect-go)
│   ├── command/        # CQRS write side
│   ├── query/          # CQRS read side
│   ├── flow/           # Self-service flow engine + strategies
│   ├── fga/            # Fine-grained authorization engine
│   ├── protocol/       # OIDC, SAML SP, SAML IdP
│   ├── scim/           # SCIM 2.0 handler
│   ├── storage/        # Postgres, Redis implementations
│   └── tenancy/        # Multi-tenant middleware
├── migrations/         # goose SQL migrations (001–017)
├── charts/             # Helm chart
├── test/
│   ├── integration/    # Integration tests (real Postgres)
│   ├── load/           # k6 load tests
│   ├── fuzz/           # Go fuzz tests
│   ├── federation/     # SAML tests (Okta, Entra ID, Google)
│   └── chaos/          # Chaos Mesh experiments
├── admin-ui/           # React + Vite + Tailwind (port 3001)
└── docs/               # Architecture, capabilities, integration guide
```

---

## Documentation

| Doc | What's in it |
|-----|-------------|
| [docs/capabilities.md](docs/capabilities.md) | Every feature explained in plain English |
| [docs/integration-guide.md](docs/integration-guide.md) | 6 integration patterns with real curl examples |
| [docs/architecture.md](docs/architecture.md) | Internal design, CQRS, token flow, FGA |
| [docs/merits-and-limitations.md](docs/merits-and-limitations.md) | Honest strengths and gaps |
| [docs/roadmap.md](docs/roadmap.md) | What's done, what's next |
| [docs/glossary.md](docs/glossary.md) | Plain English definitions of every term |

---

## Comparison

| | AuthinCore | Keycloak | Zitadel | Auth0 |
|---|---|---|---|---|
| Self-hosted | ✅ | ✅ | ✅ | ❌ |
| Binary size | ~12MB | ~500MB | ~80MB | N/A |
| Startup time | ~50ms | 30–60s | ~2s | N/A |
| Fine-grained FGA | ✅ | ❌ | Partial | Partial |
| Multi-tenant | ✅ | ✅ | ✅ | ✅ |
| Per-user pricing | $0 | $0 | $0 | $0.02–$0.07 |
| SAML SP + IdP | ✅ | ✅ | ✅ | ✅ |
| SCIM 2.0 | ✅ | ✅ | ✅ | ✅ |

---

## Current Status

AuthinCore is **feature-complete** — all auth flows, FGA, SAML, SCIM, and multi-tenancy are implemented and running.

It has **not yet been battle-tested in production.** Before production use:

- [ ] Run integration tests against your database: `make test-int`
- [ ] Run load tests and verify performance: `make load`
- [ ] Test federation against your SSO provider (Okta / Entra ID)
- [ ] Get a security review

See [docs/merits-and-limitations.md](docs/merits-and-limitations.md) for a full honest assessment.

---

## License

MIT — do whatever you want with it.

---

## Contributing

Issues and PRs are welcome. Please open an issue before starting large changes.
