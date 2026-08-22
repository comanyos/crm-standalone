# Glossary — Terms Used in AuthinCore

Plain English definitions. No jargon.

---

## Identity & Authentication

| Term | What it means |
|------|--------------|
| **Identity** | A user record. Stores email, traits, credentials. One person can have identities in multiple orgs. |
| **Session** | Proof that a user is currently logged in. Stored in Redis + Postgres. Has an expiry. |
| **Access Token** | A short-lived JWT (15 min default) your app uses to verify who the user is. |
| **Refresh Token** | A long-lived token (30 days) used to get a new access token without re-login. |
| **JWT** | JSON Web Token — a signed packet of user information. Tamper-proof. Validated locally. |
| **RS256** | The algorithm used to sign JWTs. RSA + SHA-256. 2048-bit keys, rotated every 24h. |
| **JWKS** | Public keys AuthinCore exposes so apps can verify JWTs without calling back. |
| **AAL** | Authentication Assurance Level. 1 = password only. 2 = MFA verified. |
| **Argon2id** | The password hashing algorithm. Much stronger than bcrypt. Resistant to GPU attacks. |
| **MFA** | Multi-Factor Authentication — a second check beyond password (TOTP, SMS, passkey). |
| **TOTP** | Time-based One-Time Password — 6-digit code from Google Authenticator / Authy. |
| **Passkey / WebAuthn** | Login with fingerprint / Face ID / hardware key. No password needed. |
| **Magic Link** | Passwordless login — user clicks a one-time link sent to their email. |
| **Account Lockout** | Automatically blocks login after N failed attempts for M minutes (Redis-backed). |

---

## Protocols

| Term | What it means |
|------|--------------|
| **OIDC** | OpenID Connect — standard protocol for "Login with Google" style flows. Built on OAuth2. |
| **OAuth2** | The underlying protocol for issuing access tokens. OIDC is built on top of it. |
| **PKCE** | Proof Key for Code Exchange — prevents token theft in browser and mobile apps. AuthinCore enforces S256. |
| **SAML** | Older enterprise SSO standard. Used by Okta, Entra ID, Ping. AuthinCore speaks both sides. |
| **SAML SP** | Service Provider — your app relies on someone else's identity (Okta, Entra). |
| **SAML IdP** | Identity Provider — AuthinCore acting as the SSO authority for other apps. |
| **SCIM** | System for Cross-domain Identity Management — how Okta/Azure AD pushes users into your system. |
| **Client Credentials** | OAuth2 flow for machine-to-machine communication. No human user involved. |
| **Consent** | OAuth2 screen asking user "can this app access your profile?" — AuthinCore shows this. |
| **Introspect** | Ask AuthinCore "is this token still valid?" — for cases where local validation isn't possible. |

---

## Authorization

| Term | What it means |
|------|--------------|
| **RBAC** | Role-Based Access Control — "user is admin, manager, or member". Coarse-grained. Roles in JWT. |
| **FGA** | Fine-Grained Authorization — per-resource, per-user permissions. "Alice can edit lead:123, Bob cannot." |
| **ReBAC** | Relationship-Based Access Control — permissions defined by relationships between objects and users. |
| **Zanzibar** | Google's internal authorization system. AuthinCore's FGA engine is modelled on it. |
| **Relation Tuple** | The atomic unit of FGA: `(object_type:object_id, relation, subject_type:subject_id)`. |
| **Relation Inheritance** | "owner implies editor implies viewer" — having a higher relation grants all lower ones. |
| **Group Expansion** | A group can have a relation on an object; all members of the group inherit that relation. |
| **Check** | FGA operation: "can subject X do Y on object Z?" Returns true/false. |
| **ListObjects** | FGA operation: "which objects of type T can user X perform relation R on?" |
| **ListSubjects** | FGA operation: "who has relation R on object Y?" |
| **Policy Engine** | Custom rules beyond roles (IP allow-list, time-of-day, risk score). |

---

## Tenancy & Structure

| Term | What it means |
|------|--------------|
| **Instance** | Top-level AuthinCore deployment. One instance hosts many orgs. |
| **Org** | An organisation (your customer's company). Each org is fully isolated from others. |
| **Project** | A sub-unit within an org. Scopes OAuth2 clients and apps. |
| **App** | An OAuth2 client registered under a project. |
| **Trait** | A custom field on a user profile. You define the JSON Schema (name, phone, company, etc.). |
| **Invitation** | An email sent to bring a new user into an org with a specific role. |
| **Service Account** | A machine identity (no human). Used by services, background jobs, CI/CD. |

---

## Architecture

| Term | What it means |
|------|--------------|
| **Flow** | A login or registration process modelled as a state machine. Has steps, strategies, expiry. |
| **Strategy** | One authentication method inside a flow (password, TOTP, passkey, magic link, etc.). |
| **CQRS** | Command Query Responsibility Segregation — writes (commands) and reads (queries) are separate code paths. |
| **Event Sourcing** | Every change stored as an immutable event, not just the current state. Powers the audit log. |
| **Audit Log** | Permanent record of every action — who did what, when, from where. |
| **Webhook** | Notification AuthinCore sends to your app when something happens (user created, login, etc.). HMAC-SHA256 signed. |
| **Webhook Projection** | Background worker that tails the `events` table and fans domain events out to matching webhook endpoints. Checkpoint-based — retries on failure. |
| **Blueprint** | Declarative YAML config for an org. Applied via `POST /api/v1/admin/blueprints`. Idempotent; supports `dry_run`. |
| **Policy Engine** | Pluggable rule engine that runs at login time. Evaluates reputation, group membership, and expression rules. Fail-closed — store error blocks the login. |
| **Actions Runner** | JavaScript hook runner (goja sandbox). Fires user-defined scripts on events like `post-login`. Non-fatal — a failing hook warns but does not block login. |
| **Refresh Token Rotation** | RFC 6749 §10.4 single-use refresh token chain. Each `refresh_token` grant invalidates the old token and issues a new pair. Replay detection: if a consumed token is presented, both tokens are revoked. |
| **Courier** | AuthinCore's internal email/SMS dispatch worker. Queued in Postgres, delivered via SMTP or Twilio. |

---

## Infrastructure

| Term | What it means |
|------|--------------|
| **gRPC** | High-performance RPC protocol. AuthinCore exposes a typed API on port 8081. |
| **connect-go** | The Go library for gRPC. Browser-compatible. No `grpcwebproxy` needed. |
| **buf** | Tool to compile `.proto` files into typed Go/JS/Python code. |
| **Helm** | Kubernetes package manager. `helm install authincore` deploys to any K8s cluster. |
| **goose** | Database migration tool. AuthinCore uses it with embedded SQL files. |
| **Chaos testing** | Deliberately killing services (DB, Redis) to verify the system recovers correctly. |
| **Fuzz testing** | Sending random/malformed input to find crashes and security bugs automatically. |
| **k6** | Load testing tool. Used to measure AuthinCore throughput under concurrent logins. |
