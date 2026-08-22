# AuthinCore — Roadmap

---

## Now — What Is Running (DB migration 030, all containers healthy)

| Feature | Status |
|---------|--------|
| Password login | ✅ Done |
| Magic link | ✅ Done |
| TOTP / MFA — enrollment + verification | ✅ Done |
| Passkeys (WebAuthn) | ✅ Done |
| SMS MFA | ✅ Done |
| Social login (OIDC) | ✅ Done |
| OAuth2 / OIDC provider | ✅ Done |
| PKCE | ✅ Done |
| SAML SP | ✅ Done |
| SAML IdP | ✅ Done |
| SCIM 2.0 | ✅ Done |
| Multi-tenant | ✅ Done |
| Admin UI | ✅ Done (basic) |
| gRPC API | ✅ Done |
| Fine-Grained Authorization (FGA / Zanzibar ReBAC) | ✅ Done |
| Settings — change password | ✅ Done |
| Settings — update profile traits | ✅ Done |
| TOTP enrollment QR + backup codes + first-code verify | ✅ Done |
| Session list per identity | ✅ Done |
| Session revoke-all | ✅ Done |
| `readyz` health check (probes Postgres + Redis) | ✅ Done |
| Courier dispatch background worker | ✅ Done |
| Account lockout / brute-force protection | ✅ Done |
| Refresh token rotation (RFC 6749 §10.4 single-use chain) | ✅ Done |
| Webhook dispatcher + event projection (HMAC-SHA256 signed, fire-and-forget) | ✅ Done |
| Policy engine (reputation / group / expression, fail-closed, login-flow) | ✅ Done |
| Actions runner (JS post-login hooks via goja sandbox) | ✅ Done |
| Blueprint API — declarative YAML org config (`POST/GET /api/v1/admin/blueprints`) | ✅ Done |
| Unit tests | ✅ Done |
| Fuzz tests | ✅ Done |
| CI pipeline (7 jobs) | ✅ Done |
| Helm chart | ✅ Done |
| Docker Compose (6 containers) | ✅ Done |
| Chaos Mesh experiments | ✅ Done |
| Docs (capabilities, integration, architecture, roadmap, glossary) | ✅ Done |
| Vymo demo app (plug-and-play JWT middleware) | ✅ Done |

---

## P0 — Evidence (required before calling it production-ready)

These are not code tasks — they are validation tasks. The code exists; it needs to be run against real systems.

| Task | Why |
|------|-----|
| Run federation tests against real Okta dev tenant | Find real SAML bugs before a customer does |
| Run federation tests against real Entra ID tenant | Find real SAML bugs before a customer does |
| Run `make load` (k6) → publish numbers in BENCHMARKS.md | Prove performance with real numbers |
| Run `make test-int` against live Postgres | Prove SQL correctness end-to-end |
| External security review (JWT + SAML + PKCE + FGA paths) | Find what automated tools miss |

---

## P1 — Production Operations

| Task | Why |
|------|-----|
| Structured error responses across all endpoints | JSON errors everywhere, not plain text strings |
| Prometheus `/metrics` endpoint | Grafana dashboards, alerting |
| OpenTelemetry distributed tracing | Debugging slow requests in production |
| Graceful shutdown (drain in-flight requests) | Zero-downtime deploys |
| 72-hour soak test (continuous login + token rotation) | Find memory leaks |

---

## P2 — Adoption

| Task | Why |
|------|-----|
| Go SDK published to pkg.go.dev | Easier integration, typed client |
| Node.js SDK published to npm | Cover JS/TS apps |
| Python SDK published to PyPI | Cover Python apps |
| Terraform provider | Enterprise infra-as-code requirement |
| Admin UI — SAML provider management | No more API-only config |
| Admin UI — SCIM token management | No more API-only config |
| Admin UI — OAuth2 client management | No more API-only config |
| Admin UI — FGA tuple viewer / editor | Visual permission management |
| Compatibility matrix (Okta ✅ Entra ✅ Google ✅) | Enterprise trust signal |
| Public benchmark results | Performance trust signal |

---

## P3 — Future

| Feature | Notes |
|---------|-------|
| Organization-level branding (custom login page per org) | White-label for enterprise |
| Step-up authentication | Require MFA only for sensitive actions |
| Device trust | Only allow logins from managed devices |
| Risk engine (impossible travel, new country block) | Behavioural auth layer |
| Passkey syncing across devices | iCloud Keychain / Google Password Manager |
| Bot detection | Cloudflare Turnstile / hCaptcha integration |
| FGA — ABAC attributes on tuples | "editor but only during business hours" |
| FGA — Wildcard subjects | Grant all users in an org access to an object |

---

## What Vymo needs first

1. **Run `make test-int`** → `DATABASE_URL=<dev>` to validate SQL end-to-end
2. **Get a real token** → register Vymo as OAuth2 client, test full login → FGA check flow
3. **Federation** → if Vymo has enterprise customers, run Okta SAML tests before promising it
4. **Benchmarks** → run `make load`, paste numbers into `BENCHMARKS.md`
