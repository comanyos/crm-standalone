# AuthinCore — What It Can Do

AuthinCore is a self-hosted Identity and Access Management (IAM) platform.
It handles everything related to **who a user is** and **what they are allowed to do**.

Current version: **DB migration 030**, all 30 migrations applied, server running on port 8080.

---

## 1. Login Methods

| Method | Status | What it means |
|--------|--------|--------------|
| **Password** | ✅ Live | Email + password with Argon2id hashing |
| **Magic Link** | ✅ Live | Passwordless — one-time link sent to email |
| **Google / GitHub / Any OIDC** | ✅ Live | "Login with Google" style social login |
| **TOTP (Authenticator App)** | ✅ Live | Google Authenticator / Authy — 6-digit code every 30s |
| **SMS MFA** | ✅ Live | OTP via Twilio or Vonage |
| **Passkeys (WebAuthn)** | ✅ Live | Fingerprint / Face ID / hardware key |

---

## 2. User Management

- Create, read, update, delete users (identities)
- Custom user profile fields (traits) — you define the schema
- JSON Schema validation on traits
- Password reset flow
- Email verification flow
- Account lockout after failed login attempts (Redis-backed, configurable window)

---

## 3. Organisation Management (Multi-Tenant)

- Full multi-tenant: **Instance → Org → Project → App**
- Complete org isolation — users, sessions, tokens never leak across orgs
- Invite users to orgs by email with role assignment
- One user can belong to multiple orgs

---

## 4. OAuth2 / OIDC Provider

AuthinCore issues tokens to your apps — it acts as the central "login server".

- Authorization Code Flow (web apps)
- PKCE S256/plain (mobile / SPA — prevents token theft)
- Client Credentials (machine-to-machine)
- Refresh tokens (30-day default)
- Token introspection
- Token revocation
- Consent screen
- Full OIDC discovery (`/.well-known/openid-configuration`)

---

## 5. SAML (Enterprise SSO)

**SAML SP (Service Provider)** — AuthinCore connects TO enterprise IdPs:
- Connect your app to Okta, Entra ID, Google Workspace, Ping, Keycloak
- Users log in via their company's existing SSO

**SAML IdP (Identity Provider)** — AuthinCore IS the SSO hub:
- Other apps connect TO AuthinCore for SSO
- AuthinCore acts as the central identity authority

---

## 6. SCIM 2.0 — Automatic User Provisioning

When a company uses Okta or Azure AD to manage employees:
- New employee added in Okta → automatically created in AuthinCore
- Employee deactivated → automatically deactivated
- No manual user management needed

Supported: Create, Update, Deactivate, Delete users and groups. Filter parser is Okta-compatible.

---

## 7. Fine-Grained Authorization (FGA)

Zanzibar / ReBAC model — beyond roles. Per-resource, per-user permissions.

| Example | Meaning |
|---------|---------|
| `lead:123 editor user:alice` | Alice can edit this specific lead |
| `deal:456 viewer group:sales#member` | Everyone in the sales group can view this deal |
| `project:789 owner user:bob` | Bob owns this project (inherits editor + viewer) |

- **Check**: "can user X do Y on object Z?" — `POST /authz/check`
- **List objects**: "which leads can alice edit?" — `POST /authz/list-objects`
- **List subjects**: "who can view this lead?" — `POST /authz/list-subjects`
- Relation inheritance (owner → editor → viewer)
- Group expansion
- Org-scoped — cross-org leakage impossible at the DB level

---

## 8. Sessions

- Secure session management with Redis
- Session expiry
- Revoke a single session (logout from one device)
- Revoke all sessions (logout everywhere)

---

## 9. Roles and Permissions (RBAC)

- Assign roles to users within an org
- Role checked in JWT — apps read it without calling AuthinCore again
- Policy engine for custom rules (IP allow-list, time-of-day, risk score)

---

## 10. Service Accounts

- Machine identities (no human user)
- For background jobs, CI/CD, microservices
- Long-lived tokens scoped to specific permissions

---

## 11. Audit Log

- Every action (login, token issued, password changed, user created) logged permanently
- Hybrid event sourcing — relational DB for queries, `events` table for audit trail
- Append-only — history never deleted

---

## 12. Webhooks

- Notify your app when events happen in real time
- Events: `user.created`, `user.login`, `session.revoked`, `org.created`, etc.
- HMAC-SHA256 signed payloads — your endpoint can verify every delivery is genuine
- Fire-and-forget delivery with per-webhook configurable timeouts
- Delivery log persisted in Postgres — every attempt recorded (state, error, timestamp)
- Event projection tail polls the `events` table and fans out to all matching webhooks
- `X-Authincore-Signature` header: `sha256=<hex>` for verification

---

## 13. Policy Engine

Declarative access rules that run at login time — before a session is issued.

- **Reputation check** — block logins from flagged IPs
- **Group membership** — allow or deny based on the user's org groups
- **Expression rules** — custom boolean expressions (time-of-day, risk score, country)
- Fail-**closed**: if the policy store is unreachable, login returns HTTP 503 (never silently passes)
- Scoped per org + binding key (e.g. `flow.login.post`)
- Only fires on login flows — registration and recovery are unaffected

---

## 14. Actions Runner

JavaScript hooks that execute post-login without blocking or failing the login.

- Hooks registered per org and event (`post-login`, etc.)
- Sandboxed via goja (pure-Go JS engine — no Node.js, no subprocess)
- Non-fatal: a failing hook emits a warning log; the login still succeeds
- Payload includes identity ID, org ID, session ID, IP, and user-agent
- Use cases: custom analytics, Slack alerts, enrichment calls, audit side-effects

---

## 15. Blueprint API

Declarative YAML configuration for orgs — apply a full org setup in one HTTP call.

- `POST /api/v1/admin/blueprints` — apply a blueprint (create/update flows, settings, policies)
- `GET  /api/v1/admin/blueprints` — list all blueprints applied to the org
- `dry_run: true` — validate and preview changes without writing anything
- Auth-gated — requires admin token
- Idempotent — applying the same blueprint twice is safe

---

---

## 16. API Surface

| Interface | Port | Use |
|-----------|------|-----|
| REST / OIDC | 8080 | Web apps, integrations |
| gRPC (connect-go) | 8081 | Typed SDK, microservices |
| Admin UI | 3001 | Visual management |

---

## 17. Refresh Token Rotation

AuthinCore enforces single-use refresh token chains (RFC 6749 §10.4).

- Every token refresh invalidates the previous refresh token and issues a new pair
- Replaying a consumed refresh token returns HTTP 400 — no silent re-use
- Rotation chain is broken if a token is stolen and replayed — both attacker and legitimate user are logged out
- 30-day default TTL, configurable per client

---

## What AuthinCore does NOT do

- Does not store your business data (orders, tasks, invoices — that's your app)
- Does not send marketing emails (only transactional auth emails)
- For coarse RBAC, AuthinCore tells you roles in the JWT — your app enforces them
- For fine-grained permissions, use the FGA API (`/authz/check`) — AuthinCore evaluates the rule, your app acts on the result
