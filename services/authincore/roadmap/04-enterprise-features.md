# Enterprise Features

Features needed to sell to enterprises and replace on-prem Keycloak / FusionAuth deployments.

---

## SAML 2.0

### Service Provider (SP)
- [ ] Consume SAML Response from any IdP (Okta, AD FS, PingFederate, OneLogin)
- [ ] Parse `saml2:Assertion`, validate signature, verify `NotBefore` / `NotOnOrAfter`
- [ ] Map SAML attributes to identity traits via configurable attribute map
- [ ] SP metadata endpoint: `GET /saml/sp/metadata` → XML
- [ ] Support HTTP-Redirect and HTTP-POST bindings
- [ ] Library: `github.com/russellhaering/gosaml2` + `github.com/beevik/etree`

### Identity Provider (IdP)
- [ ] Emit SAML Response signed with org keypair for downstream SPs
- [ ] IdP metadata endpoint: `GET /saml/idp/metadata`
- [ ] Support HTTP-POST and HTTP-Artifact bindings
- [ ] Single Logout (SLO) — IdP-initiated and SP-initiated

---

## LDAP / Active Directory

- [ ] LDAP sync: bind to AD, import users + groups on schedule
- [ ] Incremental sync using `uSNChanged` watermark
- [ ] Real-time LDAP authentication fallback (pass-through bind)
- [ ] Group → role mapping: AD group DN → authincore role
- [ ] Library: `github.com/go-ldap/ldap/v3`
- [ ] Config: `ldap_sync` blueprint block with `host`, `bind_dn`, `base_dn`, `filter`, `attribute_map`

---

## User-Managed Access (UMA 2.0)

- [ ] Resource server registration API
- [ ] Permission ticket endpoint
- [ ] Requesting party token (RPT) issuance
- [ ] Policy association: identity can grant access to resource to another identity
- [ ] Introspect RPT at resource server
- [ ] Based on OAuth 2.0 UMA Grant (RFC-compliant)

---

## Advanced Audit Log

- [ ] Structured audit events: actor, action, resource, org, timestamp, IP, session_id
- [ ] Written atomically with every command (eventstore already wired)
- [ ] Query API: `GET /admin/audit?actor=&resource_type=&action=&from=&to=&limit=&cursor=`
- [ ] Export: CSV, JSONL, or stream to S3 / GCS bucket
- [ ] Retention policy per-org (default 2 years, configurable)
- [ ] Tamper detection: each event includes HMAC of previous event hash (mini-blockchain)

---

## Organization Federation

- [ ] B2B federation: org A can trust org B's IdP (OIDC or SAML)
- [ ] Cross-org token exchange (RFC 8693 token exchange grant)
- [ ] JIT provisioning: unknown user from trusted IdP auto-created in target org
- [ ] Organization claim (`org_id`) propagated into access token

---

## Enterprise SSO Policies

- [ ] Enforce SSO: block password login when org has active SAML/OIDC connection
- [ ] Domain-based org routing: `user@mycompany.com` → resolve org from email domain
- [ ] Login policy: allowed auth methods per org (whitelist / blacklist)
- [ ] Session policy: max session duration, idle timeout, concurrent session limit
- [ ] IP allowlist / CIDR blocklist per org

---

## Hardware Security Keys (Advanced WebAuthn)

- [ ] Enterprise attestation verification (TPM, packed, Android key)
- [ ] Attestation CA trust anchors configurable per org
- [ ] FIDO2 UV (user verification) policy: `required` or `preferred`
- [ ] Resident keys (discoverable credentials) for true passwordless

---

## Compliance & Data Residency

- [ ] PII field encryption at rest (field-level AES-256-GCM, key per org)
- [ ] GDPR erasure: `POST /admin/identity/{id}/erase` — replace PII with tombstone record
- [ ] Data export: `GET /admin/identity/{id}/export` — returns all data held
- [ ] Data residency: pluggable storage backend selectable per org (future multi-region)
- [ ] SOC 2 controls: key rotation API, audit log immutability, admin MFA enforcement

---

## Admin Role-Based Access Control

- [ ] Built-in roles: `instance_admin`, `org_admin`, `org_viewer`, `support_agent`
- [ ] Custom admin roles with permission matrix
- [ ] Admin API key management (M2M for CI/CD pipelines)
- [ ] Admin audit: all admin API calls logged with actor

---

## Reporting & Analytics

- [ ] Login success / failure rates per org (time series)
- [ ] MFA adoption rate
- [ ] Active users DAU/MAU
- [ ] Token issuance volume
- [ ] Export to Prometheus, DataDog, or custom webhook

---

## Status Tracker

- [ ] SAML SP (consume assertions)
- [ ] SAML IdP (emit assertions)
- [ ] LDAP sync + pass-through auth
- [ ] UMA 2.0 resource server + permission ticket
- [ ] Audit export (S3 / CSV)
- [ ] Cross-org token exchange
- [ ] Enterprise SSO enforcement
- [ ] IP allowlist per org
- [ ] Field-level PII encryption
- [ ] GDPR erasure endpoint
- [ ] Built-in admin RBAC
