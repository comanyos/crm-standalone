# AuthinCore — Architecture

---

## Big Picture

```
┌─────────────────────────────────────────────────────────────────────┐
│                          YOUR APPS                                   │
│                                                                     │
│   ┌──────────────┐   ┌──────────────┐   ┌──────────────┐           │
│   │     Vymo     │   │  Billing App │   │   CRM App    │           │
│   │              │   │              │   │              │           │
│   │ "Is this user│   │ "Is this user│   │ "Is this user│           │
│   │  allowed in?"│   │  an admin?"  │   │  from Acme?" │           │
│   └──────┬───────┘   └──────┬───────┘   └──────┬───────┘           │
│          │                  │                  │                    │
│          └──────────────────┴──────────────────┘                    │
│                             │                                        │
│                    JWT token validation                              │
│                    (local, no network call)                         │
└─────────────────────────────┼───────────────────────────────────────┘
                              │
                    ┌─────────▼──────────┐
                    │                    │
                    │    AuthinCore      │
                    │                    │
                    │  :8080  REST/OIDC  │
                    │  :8081  gRPC       │
                    │  :3001  Admin UI   │
                    │                    │
                    └─────────┬──────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
     ┌────────▼──────┐ ┌─────▼─────┐ ┌──────▼──────┐
     │  PostgreSQL   │ │   Redis    │ │    NATS     │
     │  (SoR + audit)│ │(sessions + │ │  (events)   │
     │               │ │rate limits)│ │             │
     └───────────────┘ └───────────┘ └─────────────┘
```

---

## What Happens When a User Logs In

```
1. User types email + password
         │
2. AuthinCore verifies password (Argon2id hash comparison)
         │
3. Check account lockout (Redis) — block after N failed attempts
         │
3a. Policy engine eval (reputation + group + expression rules)
    → fail-closed: store error → HTTP 503, never silently pass
         │
4. If MFA enrolled → ask for TOTP code
         │
5. Create session (stored in Postgres + Redis)
         │
5a. Actions runner fires post-login JS hooks (non-blocking, warn-only)
         │
6. Issue JWT access token (RS256, signed with rotating key)
         │
7. User gets: access_token + refresh_token (single-use rotation chain)
         │
8. Event written to events table → webhook projection tail picks it up
    → HMAC-SHA256 signed delivery fired to all matching webhook endpoints
         │
9. User calls Vymo API with Bearer token
         │
10. Vymo reads JWT locally — no AuthinCore call needed
         │
11. Request proceeds with verified identity in context
```

---

## Internal Structure (CQRS)

AuthinCore separates reads and writes:

```
Write side (Commands)          Read side (Queries)
─────────────────────          ───────────────────
CreateUser                     GetUser
UpdateTraits                   ListUsers
ChangePassword                 GetSession
CreateOrg                      ListSessions
RevokeSession                  GetOrg
                               ListOrgs
        │                           │
        ▼                           ▼
   Postgres (writes)          Postgres (reads)
   Events table (audit)       Projection tables
```

Commands go through validation → write to DB → push event to audit log.
Queries read directly from optimised projection tables.

---

## Token Architecture

```
Login → AuthinCore signs JWT with RSA private key (rotates every 24h)
                                │
                    Two keys always valid:
                    - Current key (new tokens)
                    - Previous key (tokens issued before rotation)
                                │
App calls GET /oauth2/keys ─────┘ (public keys, cached 1h)
App validates JWT locally using cached public key
```

Why two keys? So rotation doesn't instantly invalidate all existing tokens.

---

## Multi-Tenant Data Model

```
Instance (the AuthinCore deployment)
    └── Org (your customer's company — e.g. "Acme Corp")
            └── Project (e.g. "Acme's Vymo workspace")
                    └── App (e.g. "Vymo Web", "Vymo Mobile")
                    └── User (belongs to Org, not Project)
```

Every DB table has `org_id`. No query ever runs without it. Tenant A cannot see Tenant B's data even if they try — the DB enforces it.

---

## Authentication Flow Engine

Every login is a "flow" — a state machine:

```
Flow created (type: login)
    │
    ├── Strategy: password     → check credentials
    ├── Strategy: totp         → check OTP code
    ├── Strategy: passkey      → WebAuthn challenge
    ├── Strategy: magiclink    → verify email token
    └── Strategy: oidc         → verify social token
    │
Flow completed → session created → token issued
```

You can chain strategies. Example: password + TOTP = two-factor login.

---

## How Your App Connects

```
AuthinCore                          Your App
──────────                          ────────
GET  /oauth2/keys          ←───── startup: fetch public keys (cache 1h)
POST /oauth2/token         ←───── user login: exchange code for token
GET  /oauth2/userinfo      ←───── optional: get fresh user info
POST /oauth2/revoke        ←───── user logout: revoke token

Every API call:
Authorization: Bearer <token>  ──► validate JWT locally (no network)
                                    read claims from token
                                    proceed
```

---

## Deployment

```
Single node (small):
  docker compose up → all services on one machine

Kubernetes (production):
  helm install authincore ./charts/authincore
  → 2+ replicas, pod anti-affinity, init container runs migrations
  → HPA scales on CPU

High availability:
  AuthinCore: stateless → scale horizontally freely
  Postgres: managed (RDS, Cloud SQL) or Patroni cluster
  Redis: managed (ElastiCache) or Redis Sentinel
```

---

## Security Layers

| Layer | Protection |
|-------|------------|
| Password storage | Argon2id (memory-hard, resistant to GPU cracking) |
| Token signing | RS256 with 2048-bit RSA, rotating keys |
| Token transport | Bearer token over HTTPS only |
| PKCE | Prevents code interception in browser flows |
| Rate limiting | Redis-backed per-IP and per-identity |
| Account lockout | After N failures, locks for M minutes |
| Session binding | Sessions tied to org + identity, not transferable |
| Audit log | Every action logged permanently |
| Refresh token rotation | Single-use chain — replayed token invalidates both sides (RFC 6749 §10.4) |
| Policy engine | Reputation / group / expression rules at login; fail-closed on store error |
| Webhook signatures | HMAC-SHA256 `X-Authincore-Signature` on every delivery |

---

## Webhook Event Fanout

```
Domain event written to events table
         │
event.Tail (background goroutine, polls every 500ms)
         │
WebhookProjection.Handle(ev)
         │
Dispatcher.Dispatch(ctx, orgID, eventType, payload)
    │
    ├── Load matching webhooks from store (ByOrgAndEvent)
    ├── Marshal canonical JSON payload
    └── For each enabled webhook:
            StoreEvent (delivery record, state=pending)
            go send(wh, ev)   ← fire-and-forget goroutine
                │
                HMAC-SHA256 sign payload
                POST to webhook URL with headers:
                  X-Authincore-Event: <type>
                  X-Authincore-Delivery: <uuid>
                  X-Authincore-Signature: sha256=<hex>
                UpdateEvent (state=delivered or failed)
```

Checkpoint only advances when all handlers succeed — a delivery failure retries on the next poll cycle without losing other events.

---

## Fine-Grained Authorization (FGA)

AuthinCore includes a Zanzibar-style ReBAC engine — the same model used by Google's internal authorization system and OpenFGA.

### Core Concept

Every permission is a **relation tuple**: `(object, relation, subject)`

```
lead:123     editor    user:alice
deal:456     viewer    group:sales#member   ← all sales team members
project:789  owner     user:bob
```

### Check Algorithm

1. **Direct** — does a matching tuple exist in the DB?
2. **Inherited** — does the subject have a higher relation that implies the requested one? (`owner → editor → viewer`)
3. **Group expansion** — does the subject belong to a group that has the relation?

Depth-limited to 10 hops to prevent cycles.

### Default Model

| Object type | Relations | Inheritance |
|-------------|-----------|-------------|
| `document` | owner, editor, viewer, approver, commenter | owner→editor→viewer; owner→approver; editor→commenter |
| `project` | owner, editor, viewer | owner→editor→viewer |
| `org` | owner, admin, member | owner→admin→member |
| `resource` | owner, editor, viewer | owner→editor→viewer |

Apps (e.g. Vymo) can define their own object types without schema migrations.

### API Endpoints

```
POST   /authz/tuples         # grant permission
DELETE /authz/tuples         # revoke permission
POST   /authz/check          # "can user X do Y on Z?" → {allowed: true/false}
POST   /authz/list-objects   # "which leads can alice edit?" → {object_ids: [...]}
POST   /authz/list-subjects  # "who can view this lead?" → {subjects: [...]}
```

### Storage

`relation_tuples` table with 4 indexes covering the three query shapes (check, list-objects, list-subjects). Scoped to `org_id` — cross-org leakage is impossible at the query level.

### Vymo Example

```
# Sales manager owns a lead
POST /authz/tuples
{"object_type":"lead","object_id":"lead:123","relation":"owner","subject_type":"user","subject_id":"alice-uuid"}

# Can alice approve lead:123?
POST /authz/check
{"object_type":"lead","object_id":"lead:123","relation":"approver","subject_type":"user","subject_id":"alice-uuid"}
→ {"allowed": true}   ← because owner implies approver in the document model
```
