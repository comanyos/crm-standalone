# AuthinCore — Release & Commercial Model

How AuthinCore is shipped — **OSS**, self-hosted **Enterprise**, and managed **SaaS** — how licensing works, and exactly what infrastructure you (the vendor) must run. Grounded in the code: `internal/license/`, the `enterprise` build tag, the multi-tenant `internal/tenancy/`, and `../authincore-license-server/`.

---

## Three ways to ship it (pick one or run all three)

| | **OSS** | **Enterprise (self-host)** | **SaaS (you host)** |
|---|---|---|---|
| Who runs the server | the user | the customer | **you** |
| Who runs datastores | the user | the customer | **you** |
| Feature gating | none (OSS build) | Ed25519 **license token** | **subscription/plan** in your DB |
| License server needed? | no | **yes** (tiny) | no — you control the deployment |
| Your ops burden | ~zero | one small license server | **full platform ops** (HA, backups, billing) |
| Typical pricing | free | annual contract | **per-MAU / per-seat** |
| Sold like | — | Keycloak EE, FusionAuth | Auth0, Clerk, WorkOS |

The rest of this doc details each. Sections 1–8 cover OSS + Enterprise; **section 9 covers SaaS**.

---

## 1. OSS vs Enterprise — the feature line

Only **6 features** are license-gated (`internal/license/license.go`). Everything else is free OSS.

| | OSS (free, Apache-2.0) | Enterprise (license required) |
|---|---|---|
| **Auth** | password, magic link, TOTP/MFA, passkeys, SMS MFA, social login (OIDC), OAuth2/OIDC provider, PKCE | — |
| **Sessions** | refresh-token rotation, account lockout | — |
| **Authz** | FGA/ReBAC, roles/RBAC | — |
| **Platform** | webhooks, policies, actions, blueprints, admin UI, gRPC API, single-org tenancy | — |
| **Federation** | — | **SAML** (SP + IdP), **SCIM** provisioning |
| **Scale** | — | **multi-org** (>1 org) |
| **Governance** | — | **audit export**, **SSO enforcement**, **custom branding** |

> The enterprise value is the **O365 / Okta** story: SAML + SCIM + multi-org are what large customers pay for.

```
  ┌──────────────────── OSS  (free) ────────────────────────────────┐
  │  password · magic link · TOTP · passkeys · SMS MFA               │
  │  social/OIDC · OAuth2 provider · PKCE · sessions · lockout       │
  │  FGA/ReBAC · roles · webhooks · policies · actions · blueprints  │
  │  single-org tenancy · admin UI · gRPC API                        │
  └─────────────────────────────────────────────────────────────────┘
  ┌──────────────── ENTERPRISE  (license required) ─────────────────┐
  │  ① SAML (SP+IdP)   ② SCIM   ③ multi-org                          │
  │  ④ audit export    ⑤ SSO enforcement   ⑥ custom branding         │
  └─────────────────────────────────────────────────────────────────┘
```

---

## 2. Two binaries (how the split is enforced)

The split is **compile-time** (Go build tags) **plus runtime** (license check):

```
  go build ./cmd/authincore                  (//go:build !enterprise)
   ──►  OSS binary   enterprise code NOT compiled in. No license. No phone-home.
   (`make build` produces this)

  go build -tags enterprise ./cmd/authincore (//go:build enterprise)
   ──►  ENT binary   SAML/SCIM/etc. routes present, each wrapped in
                     requireFeature(f) → HTTP 402 if the license doesn't grant f.
   (`make dev-enterprise` runs this; a `build-enterprise` target is worth adding)
```

An OSS user **cannot** unlock enterprise features — the code isn't in their binary.
Relevant files: `routes_oss.go` / `routes_enterprise.go`, `license_gate_oss.go` / `license_gate.go`, `deps_oss.go` / `deps_enterprise.go`.

---

## 3. Licensing trust model

A license is an **Ed25519-signed JWT**. The license server signs it with a private key; AuthinCore verifies it with an **embedded public key** — offline.

```
        YOU (vendor)                              CUSTOMER
 ┌───────────────────────┐              ┌────────────────────────────┐
 │  LICENSE SERVER        │  ① mint     │  AuthinCore (ENT binary)   │
 │  Ed25519 PRIVATE key   │ ──license──►│  embeds Ed25519 PUBLIC key │
 │                        │   token     │  ② verifies OFFLINE        │
 │  POST   /licenses      │             │  ③ periodic revocation     │
 │  DELETE /licenses/{jti}│ ◄─③ check ──│     check (online, cached) │
 │  GET .../revoked       │             │                            │
 └───────────────────────┘              └────────────────────────────┘
```

**License server endpoints** (`../authincore-license-server/cmd/server/main.go`):

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/licenses` | Mint a license (licensee, features, max_orgs, expiry) |
| `DELETE` | `/licenses/{jti}` | Revoke a license |
| `GET` | `/licenses/{jti}/revoked` | Revocation check (AuthinCore calls this) |
| `GET` | `/licenses` | List issued licenses (audit) |

**Key operational property:** because licenses are offline-verifiable, a customer's AuthinCore keeps working **even if the license server is down**. Only the revocation check is online, and it caches / can fail-open. → your license server does **not** need high uptime.

---

## 4. The two keypairs (do not confuse)

```
 KEY 1 — LICENSE signing (Ed25519)        ← ONE global pair, commercial
   PRIVATE → license server (you)
   PUBLIC  → embedded in AuthinCore
   purpose: prove a license is genuine

 KEY 2 — USER-TOKEN signing (RS256, rotating)   ← per AuthinCore deployment
   PRIVATE → inside each AuthinCore
   PUBLIC  → served at /oauth2/keys (JWKS), verified by the SDKs
   purpose: sign user login tokens (nothing to do with licensing)
```

---

## 5. What infrastructure you must run

```
 ════════ VENDOR SIDE (what you maintain) ════════════════════════════
 1. LICENSE SERVER       1 small VM + small Postgres        ALWAYS-ON*
    (authincore-license-server)   issues/revokes licenses
    *low uptime needs — offline-verifiable licenses

 2. KEY CUSTODY          secrets manager / HSM / Vault
    the Ed25519 PRIVATE key   ← crown jewels; never log/commit/leak

 3. RELEASE DISTRIBUTION a container registry (GHCR / Docker Hub)
    ship OSS + ENT images

 4. (optional) BILLING   Stripe + small panel that calls POST /licenses
    on payment → automated license issuance

 ════════ CUSTOMER SIDE (self-hosted enterprise) ════════════════════
    AuthinCore (ENT) + Postgres + Redis + NATS   ← on THEIR infra
    contacts your license server only for activation + revocation
```

**Minimum to be in business: #1 + #2.** One tiny license server and a safe place for one private key.

### Sizing
- **License server:** 1 vCPU / 1–2 GB + a small Postgres. It handles a handful of mint/revoke calls — trivial load.
- **Customer AuthinCore (self-host):** 2 vCPU / 4 GB start (see `BENCHMARKS.md`).
- **If you also host SaaS:** you run multi-tenant AuthinCore + Postgres/Redis/NATS in addition to the license server.

---

## 6. Pricing tiers (suggested, mapped to the 6 gated features)

| Tier | Price model | Features | `max_orgs` |
|---|---|---|---|
| **Community** | Free (OSS) | everything except the 6 gated | 1 |
| **Team** | flat / per-seat | SSO enforcement, custom branding, audit export | 1 |
| **Business** | per-seat | + SAML, SCIM | small cap (e.g. 5) |
| **Enterprise** | annual contract | all 6, priority support, SLA | unlimited |

The license `features[]` array + `max_orgs` claim are the only knobs — tiers are just preset combinations you pass to `POST /licenses`.

---

## 7. License-issuance runbook

**Issue (on sale):**
```
POST /licenses
{
  "licensee":  "Acme Corp",
  "tier":      "business",
  "features":  ["saml","scim","sso_enforcement","custom_branding","audit_export"],
  "max_orgs":  5,
  "expires_at":"2027-06-13T00:00:00Z"
}
→ returns a signed Ed25519 JWT  →  send to customer (portal/email)
```
Customer activates it in their AuthinCore admin UI (License page) or via `POST /api/v1/license`.

**Revoke (refund / churn):**
```
DELETE /licenses/{jti}
→ customer's AuthinCore disables enterprise features within its
  revocation-check window
```

**Renew:** issue a new license with a later `expires_at`; customer re-activates.

**Rotate KEY 1 (rare, high-stakes):** ship a new AuthinCore release with the new embedded public key, re-issue all active licenses with the new signing key, coordinate the cutover. Treat as a planned migration — requires the **security-reviewer** agent.

---

## 8. How OSS users are "managed"

They aren't — and that's the point. OSS users pull the image from the registry, run it, and never contact you. You manage OSS through **GitHub**: releases, issues, docs, community. Conversion to Enterprise happens when they hit a gated feature (SAML/SCIM/multi-org) and request a license.

---

## 9. SaaS model — you host it (managed service)

Instead of shipping a binary, you run AuthinCore for customers (the Auth0 / Clerk / WorkOS model). A customer signs up, gets a tenant, and configures everything in the admin UI — **zero install on their side**.

### What's different from the license model

```
  Enterprise (self-host)              SaaS (you host)
  ─────────────────────               ───────────────
  customer runs the binary            YOU run the binary (multi-tenant)
  Ed25519 license token gates    →    customer's PLAN gates (a row in your DB)
  license server required             NO license server — you control the deploy
  one tenant per deployment           many tenants in one deployment
```

AuthinCore is already **multi-tenant** (`internal/tenancy/` resolves instance → org from the Host header or JWT), so one deployment serves many customers. You run the **enterprise build** (all features compiled in) and drive the same `requireFeature(...)` checks from the customer's **subscription/plan** instead of a signed license token — a small adaptation of `license.Manager` to read plan state from your billing DB.

### What you must run (heavier than the license model)

```
 ════════ SaaS PLATFORM (all yours, multi-tenant) ═══════════════════
 1. AuthinCore (ENT)      ≥2 instances behind a load balancer (HA, stateless)
 2. Postgres              managed, with backups + read replica
 3. Redis + NATS          managed / clustered
 4. HOSTED LOGIN UI       per-tenant login pages + custom domains + TLS
                          ⚠️ this is the missing UI piece — SaaS is BLOCKED on it
 5. Billing              Stripe metered (per-MAU) + plan → feature mapping
 6. Ops                  monitoring/alerting (/metrics, /readyz exist),
                         log aggregation, automated backups, status page
 7. Onboarding          tenant signup + provisioning automation
 (later) Compliance     SOC 2 / data-residency as you move upmarket
```

### Pricing (SaaS is usually per-MAU)

| Plan | Price model | Notes |
|---|---|---|
| Free | $0, capped MAUs | acquisition; the 6 enterprise features off |
| Pro | per-MAU | SSO enforcement, branding, audit on |
| Business | per-MAU (higher) | + SAML, SCIM, multi-org |
| Enterprise | annual + SLA | custom domains, SSO, priority support |

### Honest blockers for SaaS specifically

- **The hosted login UI is mandatory here.** In self-host, the customer can live in the admin UI; in SaaS, *their end-users* must log in through **your** hosted pages — so the missing login/MFA/social/consent screens (see the UI audit) **block** a SaaS launch. This is the #1 prerequisite.
- **Custom domains + per-tenant TLS** (e.g. `auth.customer.com`) — not built yet.
- **Tenant isolation hardening** — multi-tenant exists, but SaaS raises the bar (noisy-neighbor, per-tenant rate limits, data-residency).
- **Ops maturity** — backups, HA, on-call, status page: real cost that the license model avoids.

> **Rule of thumb:** the license model lets you sell **this week** with near-zero ops. SaaS is a bigger investment (hosted login UI + platform ops + billing) but a bigger, recurring business. Many vendors start with the license model and add SaaS once the hosted login UI and ops are ready.

---

## TL;DR

- **Three models from one codebase:** OSS (they run it, free), Enterprise (they run it, you run a tiny license server), SaaS (you run everything, gate by plan).
- **One codebase → two binaries** (OSS / Enterprise) via build tag. Already built.
- **Enterprise = SAML + SCIM + multi-org + audit + SSO-enforce + branding.**
- **License model:** run exactly one small license server + a vault for one Ed25519 private key. Offline-verifiable licenses keep your infra tiny and low-stakes.
- **SaaS model:** you run the full multi-tenant platform — bigger recurring business, but it's **blocked on the hosted login UI** (end-users log in through your pages) plus platform ops + billing.
- **Sequencing:** the license model can ship now; SaaS needs the hosted login UI first.
