# Architecture

AuthinCore and CRM remain separate services.

1. The browser signs in through AuthinCore using OIDC Authorization Code + PKCE.
2. AuthinCore issues a JWT whose issuer and audience are its public URL.
3. CRM validates the token locally using AuthinCore's JWKS endpoint.
4. AuthinCore org_id is the CRM tenant boundary.
5. CRM enforces RPC scopes and Postgres row-level security.
6. Record-level owner/editor/viewer authorization will use AuthinCore FGA.

## Bootstrap and authorization

`authinctl bootstrap` is the repeatable first-tenant provisioning boundary. It
creates or reconciles one organization (matched by canonical domain), a first
administrator (matched by email), an OIDC public SPA client, and the CRM role
model. It is opt-in through the Compose `tools` profile and reads only runtime
environment variables, so passwords and client secrets are never committed.

The initial administrator receives the AuthinCore `owner` role and `crm_admin`.
The CRM role model is `crm_admin`, `crm_manager`, `crm_member`, and
`crm_viewer`; all grants are tenant-local AuthinCore permissions which become
JWT scopes at token issuance. The CRM API accepts only the exact CRM scopes
(or `crm:*`) and maps the authenticated `org_id` claim to its tenant scope.

CRM and AuthinCore use separate Postgres databases. Redis belongs to AuthinCore;
NATS is shared only as transport, with service-specific subjects.
