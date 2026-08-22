# AuthinCore — Merits and Limitations

Honest assessment. No marketing. Updated as of DB migration 030 — all self-service flows, webhook dispatcher, policy engine, actions runner, blueprint API, and refresh token rotation complete.

---

## Merits

### 1. You own everything
- Self-hosted — your data never leaves your servers
- No vendor lock-in — MIT license, fork it if needed
- No per-user pricing — 1 user or 1 million users, same cost

### 2. Tiny footprint
- Single Go binary (~12MB Docker image vs Keycloak's 500MB)
- Starts in ~50ms (vs Keycloak's 30–60 seconds)
- Runs on a $5/month server for small workloads

### 3. Everything in one binary
- SAML SP + IdP, SCIM, OIDC, passkeys, TOTP, SMS MFA, FGA, webhooks, policy engine, actions runner, blueprint API — no plugins, no extra services
- One `docker compose up`, one binary, one config file

### 4. Built for multi-tenant from day one
- Instance → Org → Project → App hierarchy
- Complete data isolation at the DB level — `org_id` on every table
- Every API call and FGA check is org-scoped

### 5. Fine-Grained Authorization (FGA) — not just roles
- Zanzibar / ReBAC model: per-resource, per-user permissions
- "Alice can edit lead:123, Bob can only view it" — not possible with roles alone
- Relation inheritance (owner → editor → viewer), group expansion, max-depth cycle guard
- Persistent in Postgres, 4 optimised indexes, no external dependency

### 6. Plug-and-play for your apps
- Any new app connects in under a day
- One file to copy (`auth/middleware.go`)
- JWT validation is local — zero latency on every request

### 7. Modern security defaults
- Argon2id for passwords (stronger than bcrypt, resistant to GPU attacks)
- RS256 JWT with rotating 2-key RSA pairs
- PKCE enforced for browser flows
- Redis-backed account lockout with configurable failure window
- Single-use refresh token rotation (RFC 6749 §10.4) — token replay is detectable and blocks both sides
- Policy engine fails **closed** — store error → login blocked, never silently passed
- Webhook deliveries HMAC-SHA256 signed — endpoints can verify authenticity

### 8. gRPC + REST
- REST for web integrations (port 8080)
- gRPC via connect-go for typed microservice calls (port 8081)
- Typed SDKs via `buf generate`

### 9. CI / deployment ready
- 7-job GitHub Actions pipeline (build, unit, fuzz, integration, lint, security, container)
- Helm chart with init-container migration pattern for Kubernetes
- Chaos Mesh experiments for DB/Redis kill scenarios

---

## Limitations

### 1. No production mileage yet
- AuthinCore has not been battle-tested in production
- Keycloak, Auth0, Zitadel have millions of users finding edge cases
- **Risk:** Unknown bugs in rare flows (SAML edge cases, clock skew, large token payloads)

### 2. No external security audit
- JWT validation, SAML parser, and FGA check paths have not been reviewed by an independent security researcher
- **Risk:** A subtle flaw in token validation could allow bypass
- **Status:** External review is P0 before production use

### 3. Federation is unproven against real tenants
- SAML IdP/SP code exists and compiles, test files are written
- Has **not** been run against real Okta / Entra ID tenants yet
- **Risk:** Interoperability bugs will appear on first real enterprise customer
- **Fix:** Run `test/federation/okta/` and `test/federation/entra/` against real tenants

### 4. Performance is unknown
- k6 load tests are written but never run
- No published benchmark numbers
- **Risk:** Unknown bottlenecks under real load
- **Fix:** Run `make load`, fill in `BENCHMARKS.md`

### 5. No SDK published
- Go stubs can be generated locally via `buf generate`
- Nothing published to npm / PyPI / pkg.go.dev
- Node.js and Python apps must wire JWKS manually
- **Fix:** SDK publishing is P2 roadmap

### 6. No Terraform provider
- Enterprise teams expect `terraform apply` to manage orgs and clients
- Currently everything is API-only
- **Fix:** P2 roadmap

### 7. Admin UI is minimal
- Can list users, orgs, sessions
- Cannot manage SAML providers, SCIM tokens, OAuth2 clients, or FGA tuples from the UI
- Must use API for advanced config

### 8. No hosted cloud option
- Must self-host
- Auth0 and Clerk offer zero-ops cloud
- **Intentional:** target audience is teams that require self-hosted IAM

### 9. No community yet
- No Stack Overflow answers, no Discord, no third-party tutorials
- **Fix:** Grows only with adoption

---

## Score vs Competitors (current honest position)

| Area | AuthinCore | Keycloak | Zitadel | Auth0 |
|------|-----------|---------|---------|-------|
| Features implemented | 10/10 | 10/10 | 9/10 | 10/10 |
| Production proven | 2/10 | 10/10 | 8/10 | 10/10 |
| Performance known | 2/10 | 8/10 | 9/10 | 10/10 |
| Security reviewed | 3/10 | 9/10 | 9/10 | 10/10 |
| FGA / ReBAC | 9/10 | 3/10 | 5/10 | 7/10 |
| Footprint / startup | 10/10 | 2/10 | 7/10 | N/A |
| Self-hosted | 10/10 | 10/10 | 10/10 | 2/10 |
| Ops simplicity | 9/10 | 3/10 | 7/10 | 10/10 |

AuthinCore's FGA and footprint are its current differentiators.
The gap to close is evidence: benchmarks, federation tests, security review.

---

## When to use AuthinCore

| Situation | Use AuthinCore? |
|-----------|----------------|
| Building a new SaaS product, want plug-and-play IAM | ✅ Yes |
| Self-hosting is required (compliance, data residency) | ✅ Yes |
| Multiple products sharing the same user base | ✅ Yes |
| Need fine-grained per-resource permissions (FGA) | ✅ Yes — better than Keycloak here |
| Enterprise customers who need SAML SSO | ✅ Yes (run federation tests first) |
| Need something production-ready tomorrow, zero risk | ❌ Use Keycloak or Auth0 |
| Need fully managed cloud IAM, zero ops | ❌ Use Auth0 or Clerk |
| Need 5 years of CVE history and community support | ❌ Use Keycloak |

---

## Summary

AuthinCore is the right choice if you are building your own products and want to own your IAM layer permanently — especially if you need fine-grained authorization beyond roles.

The gap between today and production-ready is not features — it is **evidence**: running the federation tests, publishing benchmarks, and getting an external security review.
