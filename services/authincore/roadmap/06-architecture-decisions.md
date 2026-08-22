# Architecture Decisions (ADRs)

Key design decisions made when building authincore, with rationale.

---

## ADR-001 — Go as the implementation language

**Decision:** Go 1.23  
**Alternatives considered:** Rust, TypeScript (Node.js), Kotlin  
**Rationale:**
- All 5 reference platforms that work well at scale use Go (Kratos, Zitadel) or JVM (Keycloak, FusionAuth) — Go gives near-JVM throughput without JVM startup cost
- Single static binary: no runtime, no classpath, trivial container
- First-class `crypto` stdlib covers argon2, SHA-256, RSA, AES — no third-party crypto boilerplate
- Excellent concurrency primitives (goroutines, channels) for flow engine + event bus without actor overhead

---

## ADR-002 — Hybrid Persistence: Relational SoR + Event Audit Log

**Decision:** Postgres relational tables = system of record; `events` table = append-only audit log written in the same transaction as the mutation  
**Alternatives considered:** Pure event sourcing (rebuild state from events), Outbox pattern (events written async)  
**Rationale:**
- Pure event sourcing (like some Zitadel internals) makes reads complex and slow — every read requires replaying N events or maintaining a separate projection
- Outbox async: audit log could lag or fail, losing events in crash scenarios
- Same-transaction write: audit completeness guaranteed, no dual-write problem
- Read side (`internal/query/`) is plain SQL — simple, fast, no projection rebuild
- Trade-off: event table cannot rebuild current state (it's audit-only, not the SoR) — acceptable because relational tables are durable and versioned via migrations

---

## ADR-003 — connect-go + buf for API layer

**Decision:** `connectrpc.com/connect` (connect-go) with buf-generated types  
**Alternatives considered:** Pure gRPC-gateway, net/http + chi, fiber  
**Rationale:**
- Single proto definition → gRPC + gRPC-Web + REST (Connect protocol) with no extra gateway binary
- No reflection required for browser clients — gRPC-Web and Connect work over plain HTTP/1.1
- buf lint + breaking-change detection catches API regressions at CI time
- Simpler than Keycloak's JAX-RS or FusionAuth's Jersey framework — one transport layer to maintain

---

## ADR-004 — Argon2id for password hashing

**Decision:** Argon2id with OWASP-recommended params: memory=64MB, iterations=1, parallelism=4  
**Alternatives considered:** bcrypt (cost 12), scrypt, PBKDF2  
**Rationale:**
- Argon2id is the winner of the Password Hashing Competition (PHC) and is OWASP's first recommendation since 2022
- Memory-hard: GPU/ASIC attacks are economically impractical at 64 MB per hash
- bcrypt is capped at 72 bytes input — truncates long passwords silently, a well-known vulnerability in older apps
- `alexedwards/argon2id` wraps `golang.org/x/crypto/argon2` with safe defaults and upgrade path

---

## ADR-005 — RS256 JWT with JWKS rotation

**Decision:** RS256 (RSA + SHA-256) for access and ID tokens, published via JWKS  
**Alternatives considered:** HS256 (HMAC-shared-secret), ES256 (ECDSA)  
**Rationale:**
- RS256: private key signs, public key verifies — resource servers never need the signing secret
- ES256 is marginally smaller tokens but RS256 has wider client-library support (critical for enterprise compatibility)
- HS256 requires sharing the secret with every resource server — secret sprawl, rotation is an operational nightmare
- JWKS endpoint allows hot key rotation: mint tokens with new `kid`, old key stays in JWKS until tokens expire

---

## ADR-006 — goja for Actions/Hooks sandboxing

**Decision:** `github.com/dop251/goja` — JavaScript ES5.1+ runtime in pure Go  
**Alternatives considered:** V8 via cgo (otto/goja alternative), Lua (gopher-lua), WASM  
**Rationale:**
- authentik uses a Python sandbox; we chose JS because more operators already know JS for IAM hooks
- goja is pure Go (no cgo): single binary, no libv8 dependency, easier cross-compilation
- Timeout enforcement via `vm.Interrupt()` — prevents runaway scripts
- Known limitation: one goja VM per invocation (expensive). **Mitigation in roadmap:** worker pool of pre-warmed VMs
- WASM considered for better isolation, but goja sandbox is sufficient for pre/post-hooks that don't need syscall access

---

## ADR-007 — CQRS with explicit Command / Query split

**Decision:** `internal/command/` for writes, `internal/query/` for reads — no shared repository interface  
**Alternatives considered:** Single repository pattern, DDD aggregate with repository  
**Rationale:**
- Command side needs transactional writes + eventstore push in same TX
- Query side benefits from read-optimized SQL (joins, aggregates) that would be awkward in a generic repo
- Clear ownership: command handler owns consistency invariants; query handler owns read performance
- Matches Zitadel's internal design — proven at scale

---

## ADR-008 — NATS JetStream for async event bus

**Decision:** NATS JetStream for outbound async delivery (webhooks, email queue, cross-instance sync)  
**Alternatives considered:** Postgres LISTEN/NOTIFY, Redis Streams, Kafka  
**Rationale:**
- Postgres LISTEN/NOTIFY: no persistence, no replay — unsuitable for webhook reliability
- Redis Streams: viable but adds another programming model (XADD/XREAD) when Redis is already used for cache/rate-limit
- Kafka: operationally heavy for a mid-scale IAM deployment
- NATS JetStream: single binary, persistent streams with at-least-once delivery, subject-based routing for multi-tenant fan-out, Go client is first-class

---

## ADR-009 — Declarative YAML blueprints (authentik-style)

**Decision:** `authinctl blueprint apply <file.yaml>` for idempotent provisioning  
**Alternatives considered:** Terraform provider, Helm chart, REST API only  
**Rationale:**
- IaC-native: operators declare desired state, tool handles create-or-update-or-skip
- Enables GitOps: check blueprints into version control alongside application config
- Terraform provider is valuable but requires Terraform — adds dependency on HashiCorp toolchain
- Blueprint engine is a thin wrapper around admin API — Terraform provider can be built on top later

---

## ADR-010 — Instance → Org → Project → App tenancy hierarchy

**Decision:** Four-level hierarchy matching Zitadel  
**Alternatives considered:** Two-level (instance → tenant), three-level (org → project → client)  
**Rationale:**
- Two-level: fine for simple SaaS, breaks for enterprises with multiple internal product teams sharing one authincore deployment
- Zitadel's four-level hierarchy solves the "multiple products, multiple orgs" problem cleanly
- Project grants allow identity in Org A to be authorized for Project in Org B — essential for platform-as-a-service use cases
- Trade-off: slightly more complex object model; mitigated by Resolver that walks the hierarchy automatically

---

## ADR-011 — Outpost forward-auth proxy (authentik-style)

**Decision:** Separate `outpost` binary: `cmd/authincore-outpost/` — HTTP forward-auth for Nginx/Traefik/Envoy  
**Alternatives considered:** Sidecar injection, Envoy ext_authz gRPC, middleware library  
**Rationale:**
- Forward-auth pattern: proxy sends `GET /forward-auth` with request headers → 200 (pass) or 401/302 (redirect to login)
- Language-agnostic: works for any upstream regardless of framework
- Envoy ext_authz also supported (same logic, different transport) — both can be served from same outpost binary
- Decoupled binary allows outpost upgrades without core server restarts

---

## Status Tracker

All ADRs above are finalized decisions — no pending items.  
Add new ADRs here as significant design decisions are made during implementation.
