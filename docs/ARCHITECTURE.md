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

## Token verification, key rotation, and readiness

The CRM API verifies AuthinCore's tokens **offline**, against a JWK Set it
fetches and caches. It does not call AuthinCore per request. That single
decision explains most of the behaviour below, including the parts that are
trade-offs rather than wins.

### Health versus readiness

| Endpoint | Question | Behaviour |
| --- | --- | --- |
| `GET /healthz` | Is the process up? | Always `200` while serving. Never consults AuthinCore. |
| `GET /readyz` | Can this instance do its job? | `200` when the JWK Set cache holds at least one key; `503` otherwise. |

An orchestrator should use `/healthz` for liveness and `/readyz` for
readiness, and the split matters. A liveness probe that failed when AuthinCore
was briefly unreachable would restart the CRM, turning a recoverable upstream
blip into a crash loop. A readiness probe that stayed green in the same
situation would keep routing traffic to an instance where **every**
authenticated request fails — which users experience as their own credentials
being wrong, not as an outage.

Start-up is deliberately tolerant: if the first JWKS fetch fails, the service
still starts, and `/readyz` reports `503` until keys arrive. `Ready` does more
than read the cache — an empty cache triggers a fetch — so an instance
recovers on its own, without a restart, within
`jwksUnknownKIDInterval` (10s) of the provider coming back.

### Key rotation

A token whose key id is not in the cache triggers an out-of-band refresh, so a
rotation takes effect without restarting the CRM: publish the new key
alongside the old one, start signing with it, and tokens signed by either
verify for as long as both are published. That refresh is rate-limited to once
per 10 seconds, because the key id comes from the token and would otherwise let
any caller drive one upstream fetch per request.

The wait for that limiter is capped at one second. The library's default is a
full minute, which is exploitable: a handful of tokens carrying unknown key ids
would each occupy a server goroutine for a minute before returning the
rejection they were always going to return.

### Two windows that are not instant

Both follow from offline verification, and both are bounded rather than closed:

- **Revoking a signing key** is not immediate. Withdrawing a key from the JWK
  Set does not invalidate cached copies of it, and nothing triggers a refresh
  for a key id that is still cached — so tokens signed by a withdrawn key keep
  verifying until the next scheduled refresh, up to `jwksRefreshInterval`
  (15 minutes).
- **Deactivating an organization or user** does not invalidate access tokens
  already issued. They stay valid until they expire; AuthinCore's refusal to
  refresh them is what ends the session. The window is the access-token
  lifetime. Tokens with no `exp` claim are rejected outright, so the window
  cannot be unbounded.

Closing either window entirely means giving up offline verification for token
introspection, or maintaining a revocation list. That is a security-hardening
decision (task.md §10.1), not an oversight — but nobody should assume
revocation is instant in the meantime.

Both windows are pinned by tests
(`internal/auth/jwks_rotation_test.go`, `internal/auth/tenant_resolution_test.go`).
