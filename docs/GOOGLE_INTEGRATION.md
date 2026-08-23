# Google CRM integration

The CRM connects to Gmail and Google Calendar on behalf of individual users.
It asks for exactly these scopes and no others:

- `https://www.googleapis.com/auth/gmail.readonly`
- `https://www.googleapis.com/auth/gmail.send`
- `https://www.googleapis.com/auth/calendar.events`

The list lives in `internal/integration/googleoauth` and is served to the web
UI through `GetGoogleConnection`, so the consent screen the user sees and the
permissions the page describes cannot drift apart.

## Whose connection is it?

A connection belongs to one person, not to an organization. It is keyed on
`(tenant_id, owner_id)`, and every RPC acts on the calling principal's own
connection — none of them takes an owner at all.

That is a deliberate constraint rather than a simplification. Gmail and
Calendar consent is granted by a person over their own mailbox; a tenant-wide
connection would mean one user's consent exposing their mail to everyone else
in the organization, and an owner field on the request would let one user
disconnect (or start an authorization against) a colleague's account. No scope
granularity fixes either, so the shape of the API rules them out.

`ListGoogleConnections` is the one administrative view. It returns status only,
and it has its own scope (`crm:integration_admin:read`) rather than sharing
`crm:integration:read`: otherwise anyone who can check their own connection
could enumerate every colleague's linked mailbox address.

## The authorization flow

```
Browser            crm-api                                  Google
   |                  |                                       |
   |-- StartGoogle --->|                                       |
   |   Connection      | generate state + PKCE verifier        |
   |                   | seal verifier, write google_oauth_    |
   |                   |   states row (tenant, actor, expiry)  |
   |<-- consent URL ---|                                       |
   |------------------ navigate to consent screen ------------>|
   |                   |                                       |
   |<----------------- redirect with ?state&code --------------|
   |-- GET /integrations/google/callback ------>|              |
   |                   | resolve tenant from state             |
   |                   | consume state (once, atomically)      |
   |                   | open sealed verifier                  |
   |                   |------- exchange code + verifier ----->|
   |                   |<------ access + refresh token --------|
   |                   |------- read account address --------->|
   |                   | seal token set, upsert connection     |
   |                   | enqueue audit event                   |
   |<-- 303 to the web app with ?google=connected              |
```

### Why the callback is unauthenticated

Google returns the user through a top-level browser navigation. It carries no
`Authorization` header and cannot be made to carry one, so the callback route
sits outside the authenticating interceptor. Three things stand in for
authentication:

1. **The one-time state record.** Written server-side before the redirect,
   carrying the tenant and actor, and redeemable exactly once — the check and
   the mark are a single `UPDATE` whose `WHERE` clause does the testing, so two
   concurrent callbacks race on the row lock and one wins. A state that can be
   redeemed twice is a replayable authorization.
2. **The sealed PKCE verifier**, bound by AEAD associated data to that state's
   tenant. A state row belonging to another tenant cannot be exchanged.
3. **Google's own check** that the code was issued to this client for this
   exact redirect URI.

A state is consumed even when the authorization failed — including when the
user denied consent. An unconsumed state left behind by a failed attempt is one
that can still be replayed.

### Where the browser goes afterwards

The final redirect is the one part of the flow an attacker can influence, so
neither half of it comes from the request:

- The **origin** is `CRM_WEB_APP_BASE_URL`, from configuration.
- The **path** is the `return_path` recorded when the flow started, validated
  on the way in to be a same-origin absolute path. Protocol-relative values
  (`//evil.example`) and backslash variants are rejected, not sanitized.

With no `CRM_WEB_APP_BASE_URL` set, the callback renders a small self-contained
result page instead of guessing an origin from a `Host` or `Referer` header.
The page interpolates nothing from the request.

Failures reach the browser as bounded reason codes defined in
`internal/integration/google` — `access_denied`, `provider_error`,
`invalid_request`, `invalid_state`, `exchange_failed`, `storage_failed`,
`not_configured` — never as a provider message, which can carry authorization
material.

## Credential handling

- Access tokens, refresh tokens and PKCE verifiers are sealed with AES-256-GCM
  before they reach the database. The store persists opaque bytes and has no
  way to open them.
- The associated data binds a token ciphertext to `(tenant, owner)` and a
  verifier ciphertext to `(tenant, state)`, under distinct prefixes. A row
  copied into another tenant, onto another owner, or across the two kinds fails
  to open rather than decrypting into a usable credential.
- `GoogleConnectionView` is a separate browser-facing type with no ciphertext
  field, so returning one cannot be a one-line mistake.
- `GOOGLE_OAUTH_CLIENT_SECRET` never enters the domain layer, a response body,
  a log line, or an error string.
- Table `CHECK` constraints refuse a disconnected row that still holds token
  material, and a connected row that holds none.

### Refresh

`Service.AccessToken` is the single seam that decides when to refresh. Google
omits `refresh_token` from most refresh responses, so the prior one is carried
forward; storing the empty value would kill the connection an hour later. A
refresh Google rejects moves the connection to `reauthorization_required`,
erases the dead credential, and records a bounded reason code — the UI then
asks the user to reconnect instead of the integration going quiet.

### Disconnect

Revoke at Google first, then erase locally. The other order strands a live
grant at the provider with no refresh token left to withdraw it. A provider
outage therefore fails the disconnect and keeps the credential, so the user can
try again; a token Google has already forgotten (HTTP 400) counts as success,
because it describes the state the user asked for.

`purge` additionally deletes the row. Without it a disconnected tombstone
remains, which is what lets the audit trail answer who ended the access and
when. The purge **event** outlives the row on purpose.

## Audit and metrics

Lifecycle events go through the module's transactional outbox on subject
`crm.google_connection.<tenant>`:

| Event | When |
| --- | --- |
| `google_connection.authorized` | a token exchange succeeded (first connect and every reconnect) |
| `google_connection.reauthorization_required` | a stored credential stopped working |
| `google_connection.disconnected` | a user withdrew the connection |
| `google_connection.purged` | the connection record was deleted |

Payloads carry the connection id, owner, account address, status, granted
scopes and a reason code — never credential material or message content.

`crm_google_oauth_flows_total{outcome}` counts lifecycle outcomes. The only
label is the bounded reason-code set; no tenant, user, mailbox address or
provider message is ever a metric label.

## Configuration

Set these in the environment that runs `services/crm-api`. Never add their
values to `.env.example`, source control, browser configuration, or chat.

| Variable | Purpose |
| --- | --- |
| `GOOGLE_OAUTH_CLIENT_ID` | OAuth web-client ID (not secret) |
| `GOOGLE_OAUTH_CLIENT_SECRET` | OAuth web-client secret |
| `GOOGLE_OAUTH_REDIRECT_URL` | Registered callback; local: `http://localhost:19431/integrations/google/callback` |
| `GOOGLE_OAUTH_TOKEN_KEY` | Base64-encoded 32 random bytes for AES-256-GCM |
| `CRM_WEB_APP_BASE_URL` | Origin the callback returns the browser to, e.g. `https://crm.example.com` |

Generate the sealing key with `openssl rand -base64 32` and inject it through
the same deployment-secret mechanism as the client secret.

The redirect URL must match the OAuth client's registered redirect URI and the
served route (`/integrations/google/callback`) **exactly** — Google compares the
whole string, including scheme, host and port. `TestCallbackPathMatchesThe
ConfiguredRedirect` pins the path half of that.

The CRM refuses to start on a partial Google configuration: a client ID with no
sealing key would mean offering a Connect button and then being unable to store
what it receives. In staging and production the redirect URL and
`CRM_WEB_APP_BASE_URL` must both be HTTPS.

A deployment with none of these set is a correctly running CRM that cannot
offer the feature. `GetGoogleConnection` reports `configured: false` and the UI
says so rather than showing a button that cannot work.

### Rotating the sealing key

There is no re-encryption path yet. Changing `GOOGLE_OAUTH_TOKEN_KEY` makes
every stored ciphertext fail to open, which surfaces as connections that cannot
refresh. Users must reconnect. Rotation with re-encryption is open work.

## What is not built yet

The connection lifecycle is complete; what it feeds is not.

- **Gmail sync** — sync cursors, message and thread deduplication, contact
  matching, and CRM timeline entries.
- **Calendar sync** — incremental sync, event linking, attendee matching,
  timezone handling, and a conflict policy.
- **Sending** — `gmail.send` is requested but nothing sends yet.
- **Microsoft** — deliberately not configured. Do not add it without an
  explicit request.

## Verification status

Google marks `gmail.readonly` as a restricted scope; `gmail.send` and
`calendar.events` require verification. The Cloud project
(`akshara-telugu-260624`) is configured for it, and submission remains
deliberately deferred until the sync features exist and a truthful demo video
can be recorded. Until verification completes, only test users listed on the
OAuth consent screen can connect.
