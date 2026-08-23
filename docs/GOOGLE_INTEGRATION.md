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

## Gmail synchronisation

### What is synced, and what is not

**A message becomes a CRM record only if one of its participants is already a
Contact in the same tenant.**

This is the decision everything else follows from, so it is worth stating why
rather than just what. A connected mailbox holds a person's entire
correspondence — their doctor, their bank, their family — and every CRM
Activity is readable by any colleague holding `crm:activity:read`. A sync that
ingested a mailbox wholesale would publish a user's private mail to their
employer. So an unmatched message leaves no row, no counter, and no record of
its subject line; it is examined in memory and forgotten.

For a message that *does* match, what is kept is an envelope:

| Kept | Not kept |
| --- | --- |
| Provider message and thread id | Message body |
| Subject line (bounded to 500 characters) | Snippet or preview |
| Sender address, direction | Attachments |
| Time Google recorded the message | Recipients other than the matched Contact |

The body is not merely unstored — it is never fetched. Every read uses
`format=metadata` with an explicit header allowlist, so Gmail returns headers
and nothing else. A later change that adds a body column would have nothing to
put in it without also widening that request, which is exactly the review such
a change deserves. `gmail_messages` correspondingly has no body or snippet
column: retention policy for message content is still an open product decision
(`docs/ROADMAP.md` M5), and an absent column is the only version of "not
decided yet" that cannot leak.

### How a pass works

```
                  ┌──────────────── first pass ────────────────┐
read history cursor ──▶ walk `newer_than:90d -in:chats` ──▶ mark complete
                  └────────────────────────────────────────────┘
                  ┌──────────── every pass after ──────────────┐
                    users.history.list from the stored cursor
                  └────────────────────────────────────────────┘

for each message id:  fetch envelope (metadata only)
                      match participants against Contacts
                      no match ──▶ drop it, record nothing
                      match    ──▶ one Activity per matched Contact
```

Details that are easy to get wrong, each pinned by a test:

- **The backfill reads its history cursor before walking, not after.** A
  cursor taken at the end skips every message that arrived during the walk —
  those are neither in the pages already read nor after the cursor.
- **An incremental pass saves the new cursor only after its last page.** Gmail
  returns the mailbox's *current* history id on every page, so storing it
  early skips whatever the remaining pages held.
- **An expired cursor restarts a backfill.** Google keeps roughly a week of
  history and answers 404 beyond that. Resuming from a position Gmail has
  forgotten would silently lose everything in the gap, so `ErrHistoryExpired`
  is distinct from an empty page.
- **Direction comes from Gmail's `SENT` label**, not from comparing the `From`
  address to the account's — aliases and delegated sending both break that.
- **The mailbox owner's own address is excluded from matching.** People are
  often Contacts in their own organization's CRM; without this every message
  would match them.
- **Synced email is `Activity.Kind = "email_synced"`**, distinct from a
  hand-logged email. A timeline that cannot tell "someone recorded that they
  emailed the customer" from "the system observed an email" cannot be audited,
  and only one of the two is evidence.

### Idempotency

`gmail_messages` is keyed per timeline entry — `(tenant, owner, message,
subject_type, subject_id)` — not per message. An email to three Contacts
belongs on three timelines, and keying this way makes each of those entries
individually idempotent: re-syncing cannot double-post, and a Contact created
after the message arrived still gets its entry on a later pass.

The index row and the Activity are written in one transaction, index row
first with `ON CONFLICT DO NOTHING`. The primary key is therefore what decides
whether an entry is new, so two replicas racing on one message serialize there
rather than both concluding it is. Splitting the two writes would leave either
an Activity that gets re-created next pass, or an index row for a timeline
entry that silently never appears.

### Scheduling

A background poller runs every minute (jittered), claims up to 25 mailboxes
per cycle, and syncs each under its own tenant scope. Each mailbox is leased
for five minutes so two replicas do not walk the same one; the lease expires
rather than needing release, so a replica that dies mid-pass cannot make a
mailbox permanently unsyncable. One mailbox's failure never stops the cycle —
a single revoked credential must not stop everyone else's mail from syncing.

One pass examines at most 100 messages. A large mailbox therefore needs
several, which the cursor makes free: the next pass resumes exactly where the
last stopped. `SyncGoogleMailbox` runs one pass on demand for the caller's own
mailbox, and is bounded the same way, so it cannot be turned into unbounded
work by pressing a button repeatedly.

### Disconnecting

Disconnect drops the sync cursor and **keeps the deduplication index**. That
is the non-obvious half: the index is what remembers which messages have
already reached a timeline, so deleting it would mean a user who disconnects
and reconnects gets a second copy of every email in their CRM.

Neither disconnect nor purge deletes the Activities. They are CRM records the
organization has since worked from, and deleting a customer's correspondence
history because somebody unlinked a mailbox would be a surprise, not a privacy
win. Erasing synced content on request is a separate operation this module
does not have yet — see **What is not built yet**.

## Calendar synchronisation

The same privacy rule as Gmail, and for a sharper reason: a calendar names who
somebody met and when. An event is recorded only when one of its attendees is
already a Contact in the tenant, and the field mask sent to Google excludes
`description` and `location`, so neither crosses the network.

### What makes calendars harder than mail

Events are **mutable**. Meetings move and meetings get cancelled, and telling
a customer the wrong time is a real cost. Mail never does either, so calendar
sync needs two things Gmail's does not:

- **A mirror.** `calendar_events` holds Google's current state for an event and
  is updated in place, while the CRM timeline stays append-only — the property
  that makes an Activity worth having as evidence.
- **A material-state fingerprint.** `state_key` covers status and start, and
  nothing else. A new fingerprint appends a timeline entry; the same one
  updates the mirror silently.

The title is deliberately not material even though it is stored. Renaming
"Call" to "Discovery call" is not news, and if it counted it would look to a
reader exactly like a rescheduled meeting. So the timeline says only three
things, and each says it once:

| Transition | Timeline entry |
| --- | --- |
| first seen | `Meeting scheduled: <title> (<when>)` |
| start moved | `Meeting rescheduled: …` |
| status became cancelled | `Meeting cancelled: …` |

Google keeps returning cancelled events on subsequent syncs, which is exactly
why the fingerprint exists: without it the customer's timeline would fill with
repeat cancellations.

### Timezones

This is the part that goes wrong quietly, so it is handled end to end and
asserted in both directions.

Google sends an all-day event as a bare date — `"date": "2026-08-01"` —
meaning *the whole of 1 August wherever this calendar lives*. Reading that as
UTC puts the day boundary in the wrong place for exactly the users who notice:
in Asia/Kolkata the event starts 5½ hours into 1 August, and in
America/Los_Angeles it starts the evening before.

So: the date is parsed in the event's own zone, `all_day` and `time_zone` are
stored together, a CHECK constraint refuses an all-day event without its zone,
and every timeline entry renders in the event's zone rather than the reader's.
A meeting at 09:00 in London does not read as 04:00 to the customer who
attended it, and an all-day event on 1 August never reads as 31 July.

When an event carries no zone of its own — common for all-day events — the
calendar's own default is used, read once per pass. An unrecognized zone name
falls back to UTC and *stores* UTC, which is at least true.

### Matching

Attendees and the organizer, minus every address belonging to the connected
account. "Belonging to the connected account" is more than one address:
Google marks its own rows with `self` on both the organizer and the attendee
list, and that is the answer to trust. A user with aliases, a delegated
mailbox, or a Google Group membership organizes meetings under addresses the
connection has never heard of; comparing against the single configured address
misses them, and the failure is quiet — the account matches itself as a
Contact and every meeting it attends lands on its own timeline.

### The cursor

A first pass lists a bounded window — 90 days back, 180 days forward, the
future window longer because upcoming meetings are the ones somebody is about
to walk into — and Google returns a `syncToken` on the final page. Later
passes send that token and nothing else: Google rejects a sync token combined
with a time window, so the two request shapes stay strictly separate.

A token Google has discarded comes back as HTTP 410. That restarts the window
rather than resuming, for the same reason an expired Gmail history cursor
does: an unknown amount changed while the token was invalid.

One pass examines at most 250 events. The poller runs every five minutes to
Gmail's one — calendars change far less often than mailboxes — as its own
loop rather than sharing a pass, so a Gmail quota problem cannot stop meetings
reaching timelines and an expired calendar cursor cannot delay anybody's mail.

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

`crm_google_oauth_flows_total{outcome}` counts connection lifecycle outcomes
and `crm_gmail_sync_passes_total{outcome}` counts sync passes. In both, the
only label is a bounded reason-code set; no tenant, user, mailbox address or
provider message is ever a metric label.

`crm_calendar_sync_passes_total{outcome}` is the calendar equivalent.

`crm_gmail_timeline_entries_total` and `crm_calendar_timeline_entries_total`
count entries written by sync and have no labels at all — a per-tenant or
per-mailbox breakdown of how much somebody emails, or how many meetings they
take, is not something a metrics endpoint should carry.

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

- **Writing to Google** — sync is one-way in both streams. The CRM never
  creates, moves or cancels an event, and `gmail.send` is requested but
  nothing sends. Google is authoritative for everything synced; there is no
  write-back and therefore no two-way conflict to resolve.
- **Calendars other than `primary`** — a user with a separate work calendar
  syncs only their main one.
- **Recurring series** — instances are synced individually (`singleEvents`),
  so moving a whole series posts one timeline entry per affected instance.
  `recurring_event_id` is stored but nothing groups them.
- **Account and opportunity matching** — sync attaches an email to matched
  Contacts only. Rolling that up to the Contact's Account, or onto an open
  Opportunity, needs a rule for which of several open deals an email belongs
  to, and that is a product decision.
- **Erasing synced content on request** — disconnecting stops sync but keeps
  the Activities already created. A deliberate "delete the email this CRM
  holds about me" operation does not exist, and a data-subject erasure request
  cannot be served without one.
- **Thread grouping in the UI** — `thread_id` is stored but nothing reads it;
  a conversation shows as separate timeline entries.
- **Sealing-key rotation** with re-encryption.
- **Microsoft** — deliberately not configured. Do not add it without an
  explicit request.

## Verification status

Google marks `gmail.readonly` as a restricted scope; `gmail.send` and
`calendar.events` require verification. The Cloud project
(`akshara-telugu-260624`) is configured for it, and submission remains
deliberately deferred until the sync features exist and a truthful demo video
can be recorded. Until verification completes, only test users listed on the
OAuth consent screen can connect.
