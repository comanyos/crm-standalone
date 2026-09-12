# Environments and configuration

Savvy CRM has one Go application service, one PostgreSQL database, optional
NATS transport, and a static browser application. Native authentication runs
inside `services/crm-api`.

## Environments

`APP_ENV` accepts `development`, `test`, `staging`, or `production`.

- Development permits loopback HTTP, the documented local database passwords,
  and the optional deterministic test OTP.
- Test has the same local allowances and is intended only for isolated,
  disposable infrastructure.
- Staging and production enforce HTTPS origins, Secure cookies, durable
  Postgres, strong secrets, complete provider configuration, and reject
  `AUTH_DEV_OTP`.

Configuration is read from environment variables. The root `.env` is a local,
gitignored convenience; deployed secrets belong in the platform's secret
manager.

## CRM API

| Variable | Local default | Production rule |
| --- | --- | --- |
| `CRM_ADDR` | `:9431` | Internal listen address |
| `DATABASE_URL` | none outside Compose | Required, TLS enabled, strong password |
| `OUTBOX_DRAIN_DATABASE_URL` | none outside Compose | Required when the RLS-protected application pool cannot drain across tenants |
| `NATS_URL` | empty outside Compose | Optional transport |
| `CORS_ALLOWED_ORIGINS` | authenticated web-app origin in Compose | Exact comma-separated HTTPS app origins; grants the full CRM API surface |
| `AUTH_CORS_ALLOWED_ORIGINS` | local marketing preview origins in Compose | Exact comma-separated HTTPS marketing origins; grants only the public auth/onboarding allowlist |
| `CRM_PUBLIC_BASE_URL` | `http://localhost:19431` | Public HTTPS API origin |
| `CRM_WEB_APP_BASE_URL` | empty | Exact HTTPS application origin when integrations redirect back |
| `CRM_METRICS_TOKEN` | empty in local development | At least 16 characters in staging/production |
| `LEAD_CAPTURE_SECRET_KEY` | unset/off | Base64 32-byte key when inbound lead capture is enabled |
| `LEAD_SLA` | `15m` | Untouched-new-lead threshold shared by My Day and SLA email/in-app escalation; 1 minute through 168 hours |

### Meta / Facebook / Instagram Lead Ads

The native connector is disabled only when this entire group is empty. Setting
part of the group fails startup so the UI cannot advertise a connection that
will lose its callback, webhook verification, or encrypted credentials.

| Variable | Rule |
| --- | --- |
| `META_APP_ID` | Savvy-owned Meta app ID |
| `META_APP_SECRET` | Server-side secret; never expose in Vite or a browser |
| `META_OAUTH_REDIRECT_URL` | Exact callback; production: `https://crmapi.savvyadvi.com/integrations/meta/callback` |
| `META_WEBHOOK_VERIFY_TOKEN` | Independent random value, at least 16 characters |
| `META_TOKEN_KEY` | Base64-encoded 32-byte key used to seal user and Page access tokens |
| `META_GRAPH_VERSION` | Explicitly pinned provider version; currently `v25.0` |

The Page webhook callback is
`https://crmapi.savvyadvi.com/integrations/meta/webhook`. Savvy CRM verifies the
subscription challenge and every `X-Hub-Signature-256`, retrieves the full lead
server-side, and never returns an access token through the CRM API. The OAuth
grant requests Page discovery, Business discovery, webhook subscription, lead
retrieval, and read-only ad attribution permissions. Complete app review and a
test-lead run before enabling paid traffic.

## Native authentication

| Variable | Default | Rule |
| --- | --- | --- |
| `OTP_PEPPER` | development-only Compose value | Independent secret of at least 32 bytes; production rejects the development value |
| `AUTH_OTP_TTL` | `10m` | Short challenge lifetime |
| `AUTH_SESSION_TTL` | `720h` | Server-side session lifetime |
| `AUTH_COOKIE_NAME` | `savvy_session` | Use a deployment-specific name only when multiple environments share a host |
| `AUTH_COOKIE_SECURE` | `false` locally | Must be `true` in staging/production |
| `AUTH_LOGIN_SUCCESS_URL` | local web origin in Compose | Exact allowed HTTPS application destination |
| `AUTH_JOIN_RESULT_URL` | local marketing signup URL in Compose | Fixed HTTPS marketing destination for Google organization-join results |
| `AUTH_DEV_OTP` | unset | Exactly six digits; accepted only with `APP_ENV=test` |
| `AUTH_BOOTSTRAP_ORG_NAME` | unset | Pair with bootstrap email for the initial workspace |
| `AUTH_BOOTSTRAP_EMAIL` | unset | Normalized Super Admin identity; production Savvy value is `support@savvyadvi.com` |
| `AUTH_BOOTSTRAP_ORG_ID` | generated | Optional stable UUID for an imported tenant |

The browser gets an HttpOnly, SameSite session cookie. JavaScript does not
receive or persist the session token. A future native client may present the
same opaque token as a Bearer value from OS secure storage.

The Super Admin manages registered users, organization status, access, and the
`free`, `pro`, or `enterprise` plan through `/admin`. It is platform-only: it
has no CRM data scopes, and the app redirects it away from tenant screens. A
workspace `crm_admin` can manage only its own organization and users. This plan
value is authoritative account metadata; payment collection and
feature-entitlement enforcement are separate commercial controls and are not
implied by changing the value. The Savvy support identity has additional
database-trigger protection described in [BOOTSTRAP.md](BOOTSTRAP.md).

### Passwordless email delivery

| Variable | Typical Google Workspace local value |
| --- | --- |
| `SMTP_HOST` | `smtp.gmail.com` |
| `SMTP_PORT` | `465` |
| `SMTP_USERNAME` | verified mailbox |
| `SMTP_PASSWORD` | dedicated provider/app credential |
| `SMTP_FROM` | `Savvy CRM <support@savvyadvi.com>` |
| `SMTP_TLS` | `true` for implicit TLS on 465 |
| `SMTP_REQUIRE_STARTTLS` | `false` on 465; `true` with port 587 |

A partial SMTP configuration is an error where real OTP delivery is required.
Never substitute a contact-form forwarding service for transactional email.
See [EMAIL_DELIVERY_SETUP.md](EMAIL_DELIVERY_SETUP.md).

### Google account sign-in

These variables configure identity-only sign-in:

- `AUTH_GOOGLE_CLIENT_ID`
- `AUTH_GOOGLE_CLIENT_SECRET`
- `AUTH_GOOGLE_REDIRECT_URL` (local:
  `http://localhost:19431/auth/google/callback`)

All are required together. This must be a different OAuth client from the CRM
Gmail/Calendar connection. Sign-in requests `openid email profile`; it never
requests mailbox or calendar scopes.

## Google CRM integration

The following independent settings enable a user to connect Gmail and Google
Calendar from inside an authenticated CRM session:

- `GOOGLE_OAUTH_CLIENT_ID`
- `GOOGLE_OAUTH_CLIENT_SECRET`
- `GOOGLE_OAUTH_REDIRECT_URL`
- `GOOGLE_OAUTH_TOKEN_KEY`

The token key is base64-encoded 32 random bytes used to seal stored provider
credentials. Partial configuration fails startup. See
[GOOGLE_INTEGRATION.md](GOOGLE_INTEGRATION.md).

## Outbound workflow webhooks

`WORKFLOW_WEBHOOK_SECRET_KEY` is an independent base64-encoded 32-byte key
used to seal the signing secret generated for each outbound webhook endpoint.
Leave it empty to disable endpoint creation in local/offline deployments. In
the Azure beta it is required and stored in Key Vault as
`workflow-webhook-secret-key`. Do not reuse `LEAD_CAPTURE_SECRET_KEY`, a Google
token key, or a Meta token key; those credentials have separate rotation and
incident boundaries.

## Browser application

The public build-time settings are:

| Variable | Default |
| --- | --- |
| `VITE_CRM_API_URL` | `http://localhost:19431` |
| `VITE_CRM_AUTH_PORTAL_URL` | `http://localhost:4173/?auth=login#top` |

Both must be absolute HTTPS URLs in a production build (loopback HTTP is
allowed locally). The app loads `GET /auth/session` and sends
`credentials: include` on protected CRM requests. When no session exists, it
opens the marketing site's combined Login / Sign-up dialog and supplies only a
validated relative `returnTo` path. The marketing dialog performs email-code
or Google authentication directly with the API.

Production values are `https://crmapi.savvyadvi.com` and
`https://crm.savvyadvi.com/?auth=login#top`. Successful auth returns to
`https://crmapp.savvyadvi.com`; no browser Bearer token is placed in that URL.

## Rate limiting and challenge behavior

Email request responses are intentionally generic for login so registered
addresses cannot be enumerated. Issuance is limited to 5 requests per email
address per 15 minutes and a shared durable ceiling of 300 email-plus-Google
starts per 15 minutes. Challenges also have short expiry, bounded verification
attempts, and atomic single use; a resend retires the previous challenge for
the same email and flow. Provider or SMTP failure must not produce a
successful "sent" response.

Production should add an edge rate limit keyed by trusted client IP and route,
while keeping the database-backed per-email limits as the authoritative
cross-instance boundary. Do not trust an arbitrary `X-Forwarded-For` header
unless it comes from the configured load balancer/proxy.

## Disposable integration database

`crm-postgres-test` is behind Compose's `test` profile, uses tmpfs, and
listens on port 15533. Start it with `make test-db-up`; stop it with
`make test-db-down`. Tests use `TEST_DATABASE_URL` and can set
`REQUIRE_LIVE_POSTGRES=1` to turn an unavailable database into failure.

## Secret ownership and rotation

| Secret | Rotation consequence |
| --- | --- |
| CRM database credentials | Update the database and deployment secret, then roll the API |
| `OTP_PEPPER` | Invalidates outstanding short-lived OTP challenges |
| SMTP credential | Update provider and secret store, then recreate the API task/container |
| Google sign-in client secret | Rotate in Google and the deployment together |
| Gmail/Calendar client secret and token key | Follow the integration re-consent/re-encryption runbook |
| Meta app secret and token key | Rotate the app secret in Meta/Key Vault; token-key rotation requires reconnecting every tenant unless ciphertext is re-encrypted first |
| Session tokens | Stored only as digests; revoking database rows signs sessions out |

Never put secrets in a Vite variable, source file, image layer, log, issue,
screenshot, or chat. Recreating a Compose service is required after changing
its environment; `docker compose restart` alone does not reload variables.

## Acceptance verification

Before promotion:

1. Configuration validation succeeds and no secret appears in
   `docker compose config` output retained by CI.
2. Existing-user login sends and consumes a real one-time code.
3. Unknown login email remains generic and creates no session.
4. Verified signup creates exactly one user, organization, owner membership,
   onboarding profile, and session.
5. Logout removes the server session and expires the cookie.
6. Cross-tenant and role-matrix tests pass under the restricted `crm_app`
   database role.
7. Google login state, callback, verified-email, and account-linking tests pass
   when that client is enabled.
