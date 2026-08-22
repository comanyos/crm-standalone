# Parity Features — Full Parity with Kratos / Zitadel

Everything needed to match the feature surface of the 5 reference platforms.  
Assumes MVP (02-mvp-features.md) is complete.

---

## Auth Methods

### Passwords
- [x] Argon2id hashing with OWASP params
- [ ] Password complexity policy per-org (min length, character classes, breach check via HaveIBeenPwned API)
- [ ] Password history (last N hashes stored, reuse blocked)
- [ ] Forced password change on next login flag

### Passwordless
- [ ] Magic link (email OTP code or signed URL, 15 min expiry)
- [ ] SMS OTP (pluggable provider: Twilio, AWS SNS, Vonage)
- [ ] Login code via email (6-digit code, rate-limited, 5-min expiry)

### TOTP / MFA
- [ ] TOTP enrollment: generate secret → serve QR code (Google Authenticator / Authy)
- [ ] First-code verification before enrollment completes
- [ ] Backup codes (8 x 10-char codes, each single-use, stored hashed)
- [ ] MFA policy per-org: optional / required / required after N days
- [ ] Remember device (skip MFA for 30 days on trusted device)

### Social / External IdP
- [x] OAuth2 / OIDC upstream provider (code + userinfo, exchange implemented)
- [ ] Google (well-known OIDC)
- [ ] GitHub (custom OAuth2 + email API call)
- [ ] Microsoft / Entra ID (OIDC, tenant-aware)
- [ ] Apple Sign In (JWT token, special private key)
- [ ] Generic SAML 2.0 SP (consume SAML assertion from any IdP)

### Passkeys / WebAuthn
- [ ] Registration ceremony: `navigator.credentials.create` → store credential
- [ ] Authentication ceremony: `navigator.credentials.get` → verify assertion
- [ ] Multiple credentials per identity
- [ ] Credential label + last-used tracking

---

## Identity Management

### Fields and Traits
- [x] JSON-Schema-driven traits per org schema
- [ ] Admin API: `PUT /admin/identity/{id}/traits` — partial update merging
- [ ] Trait migration: when org schema changes, validate existing identities
- [ ] Custom identifier types beyond email (username, phone number)

### Account Linking
- [ ] Link social credential to existing account (same email auto-merge or explicit prompt)
- [ ] Unlink credential
- [ ] Prevent unlink when last credential would be removed

### Account Lifecycle
- [x] Active / deactivated / deleted states
- [ ] Soft delete: identity hidden, tokens/sessions revoked, data retained for audit
- [ ] Hard delete: GDPR right to erasure, cascade remove credentials/sessions/tokens
- [ ] Admin impersonation: admin can mint a session for any identity

---

## Session Management (Kratos parity)

- [x] Session create / revoke / extend (command layer)
- [x] Redis-backed session cache
- [x] WhoAmI endpoint
- [ ] Session list for identity: `GET /admin/identity/{id}/sessions`
- [ ] Global session revoke on password change
- [ ] `trust_for` field: trust level 0/1/2 per AAL
- [ ] IP + User-Agent binding (configurable) with suspicious activity detection
- [ ] Session activity log (last seen, IP changes)

---

## OAuth2 / OIDC (Zitadel parity)

- [x] Authorization Code + PKCE (S256 + plain)
- [x] Refresh token grant with rotation
- [x] Client credentials grant
- [x] JWKS endpoint
- [ ] Implicit grant (legacy, gated behind org setting)
- [ ] Device authorization grant (RFC 8628) — for TV / CLI flows
- [ ] Token introspection (`POST /oauth2/introspect`, RFC 7662)
- [ ] Token revocation (`POST /oauth2/revoke`, RFC 7009)
- [ ] Dynamic client registration (RFC 7591)
- [ ] Pushed Authorization Requests (PAR, RFC 9126)
- [ ] Resource Indicators (RFC 8707) for audience-scoped tokens
- [ ] Scope management UI + admin API
- [ ] Consent screen: per-scope user approval, remember choice
- [ ] `login_hint`, `prompt=login|consent|none|select_account` support

---

## Multi-Tenancy (Zitadel parity)

- [x] Instance → Org → Project → App hierarchy defined in domain
- [ ] Org self-service: org admin can manage their own users, settings, IdPs
- [ ] Org-level branding (logo, primary color, custom CSS)
- [ ] Org-level session + password + MFA policy
- [ ] Custom domain per org: `auth.mycompany.com` → org resolver
- [ ] Project roles + role assignments to identities
- [ ] Cross-org token: identity in org A can access resource in org B via project grant

---

## Admin API

- [ ] Full CRUD: orgs, projects, apps, users, roles, policies
- [ ] Bulk identity import (CSV / JSON)
- [ ] Audit log query API: `GET /admin/audit?actor=&resource=&from=&to=`
- [ ] Metrics endpoint: `GET /admin/metrics` (Prometheus format)

---

## Self-Service Flows (Kratos parity)

- [x] Login, registration, recovery, verification, settings flows (state machine)
- [ ] Flow UI schema: return JSON describing fields + validation errors for headless UI
- [ ] `continue_with` instructions in flow response (e.g., redirect after success)
- [ ] Browser-based flow with CSRF protection (double-submit cookie)
- [ ] API-based flow (no redirects, for native apps)
- [ ] After-flow hooks: trigger action/webhook on flow completion

---

## Status Tracker

- [ ] Password policy per-org
- [ ] TOTP + backup codes
- [ ] Magic link / email OTP
- [ ] WebAuthn / passkeys
- [ ] SAML SP
- [ ] Device authorization grant
- [ ] Token introspection + revocation
- [ ] Consent screen
- [ ] Org-level policies and branding
- [ ] Admin bulk import
- [ ] Audit log query API
