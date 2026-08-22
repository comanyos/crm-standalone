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

## The web session model

What the browser application holds, where it holds it, and what ends a
session. Implemented in `packages/auth-client` and
`apps/web/src/auth/`.

### What is stored, and where

| Item | Location | Lifetime |
| --- | --- | --- |
| Access token | React state only | The tab's memory. Never written to any storage. |
| Refresh token | `sessionStorage` | Cleared when the tab closes; survives a reload. |
| PKCE verifier + `state` | `sessionStorage`, keyed by `state` | Consumed on callback, and expired after 10 minutes regardless. |
| Cross-tab sign-out signal | `BroadcastChannel`, nothing persisted | The message itself. |

`localStorage` holds nothing. `sessionStorage` is the deliberate middle
ground: per-tab and cleared on close, unlike `localStorage`, while still
surviving the reload that makes "still signed in after F5" work.

Storing a refresh token at all is a trade-off, and it is forced. AuthinCore's
authorization endpoints send `X-Frame-Options: DENY` — correct clickjacking
hardening for an identity provider, not something to weaken for client
convenience — which rules out the hidden-iframe silent-renew pattern. A plain
background `grant_type=refresh_token` exchange is what remains, and it needs a
refresh token to present.

### Renewal

The access token is renewed about 60 seconds before its own `exp`, so a
request already in flight never races an expiry the backend would reject.
Concurrent renewals are de-duplicated into one exchange: AuthinCore rotates
the refresh token on every use, so two overlapping renewals would have the
second present a token the first already consumed. React's development-mode
double-invocation of effects surfaces this immediately, but it is not a
development-only concern.

A failed renewal is treated as "the user must sign in again": the session is
dropped and a toast explains why. There is no retry, because the failures that
reach this point — a revoked or expired refresh token — do not improve on
retry.

### What ends a session

| Trigger | Local session | AuthinCore session | Refresh token |
| --- | --- | --- | --- |
| Explicit sign-out | Cleared | Ended (`DELETE /api/v1/sessions/me`) | Revoked |
| Idle timeout (30 min) | Cleared | Left alone | Revoked |
| Another tab signs out | Cleared | Already ended by that tab | Revoked |
| Failed renewal | Cleared | Left alone | Already gone |

Explicit sign-out waits up to 3 seconds for the server calls to land before
showing the confirmation. Ending AuthinCore's own session is what stops a
subsequent `/authorize` from silently re-authenticating the same user with no
password, so showing "You're signed out" before that request lands would be a
claim the system had not yet made true.

The two rows that do **not** end the AuthinCore session are deliberate. An
idle background tab must not be able to sign the user out of the tab they are
actively working in, and a tab reacting to another tab's sign-out would only
be repeating a call that tab already made.

Every one of them revokes this tab's refresh token, because `sessionStorage`
is per-tab: leaving it in place would let a reload silently restore a session
the user was just told had ended.

### Idle timeout

Thirty minutes without deliberate interaction. Deliberate excludes
`mousemove`: a trackpad under a resting palm, or a page animating beneath the
pointer, would otherwise keep a session alive indefinitely and make the
timeout decorative. Keyboard, pointer-down, wheel, touch and returning to a
backgrounded tab all count.

### Multi-tab behaviour

Each tab has its own `sessionStorage`, so each holds its own refresh token and
renews independently. Signing out in one tab ends every other tab of the same
origin, over a `BroadcastChannel` carrying a bare `{type, at}` message — an
event, never anything session-derived, since any same-origin script can join a
named channel.

One case is worth knowing: duplicating a tab copies `sessionStorage`, so both
copies briefly hold the *same* refresh token. Because refresh tokens are
single-use, whichever renews second is rejected and that tab asks the user to
sign in again. This is rotation working as intended — the alternative is
accepting a replayed refresh token — but it looks like an unexplained sign-out
to a user who duplicated a tab and left both open past the renewal window.

Browsers without `BroadcastChannel` (Safari before 15.4) lose cross-tab
propagation only; sign-out, renewal and the idle timeout are unaffected.

## Write concurrency

Two different mechanisms guard CRM writes, and they answer different
questions. Both are needed; neither substitutes for the other.

### Optimistic concurrency: whose edit wins

Every entity carries `updated_at`. A client that round-trips the value it last
read is telling the server which version it edited, and a write whose
`updated_at` disagrees with the stored row is refused with
`ErrConflict`, surfaced as Connect's `Aborted` and shown to the user as "This
record was changed by someone else." A client that sends no `updated_at` opts
out and gets last-write-wins, which is what makes the check safe to add to an
endpoint that already has callers.

This applies to accounts, contacts, leads, opportunities, tasks and notes.
Leads and opportunities were the two that had the client half and not the
server half: both form pages sent the field, and both stores accepted and
discarded it, so a second editor's save silently overwrote the first.

### Row locks: what the server does with simultaneous writes

Optimistic concurrency only helps a caller who tells the server what it read.
It does nothing for two writes that arrive together, and nothing at all for a
write that is several statements long.

Two CRM operations are several statements long:

- **`ConvertLead`** creates or reuses a contact, attaches it to a matched or
  new account, optionally creates an opportunity, and marks the lead
  converted. Half of that is worse than none of it: a contact and account with
  no lead pointing at them are invisible orphans.
- **`UpdateOpportunity`** decides a stage transition, writes the row, appends
  the stage-history entry that transition produced, and — on the one
  transition into a won stage — enqueues `opportunity.closed_won`. The stage
  history is the pipeline's audit record, so a row that records a move the
  deal did not make is a reporting defect, not a cosmetic one.

`PgStore` runs each of these as one RLS-bound transaction and takes the
subject row with `SELECT ... FOR UPDATE` first. Under `READ COMMITTED` a
second caller blocks on that lock and then re-reads the committed row, so it
decides its transition from where the deal actually is: either a legal next
step or a rejected one, never a second branch out of a stage that has already
been left. Conversion gets its idempotency from the same lock — the loser
sees `status = converted` and returns the ids the winner produced rather than
creating a second opportunity, which has no natural key to deduplicate
against.

`MemStore` has no rows to lock, so it serializes the same three compound
paths (`ConvertLead`, `UpdateLead`, `UpdateOpportunity`) on one mutex. That is
coarser than a per-row lock and correct for a store whose point is being
simple. The two implementations are held to identical observable behaviour by
`internal/domain/revenue_store_parity_test.go`, because `MemStore` is what the
service runs on with `DATABASE_URL` unset and what `internal/api`'s suite
tests against.

### Never acquire a second connection inside a transaction

A transaction holds a pool connection for its whole life. Any step inside it
that reads through the store rather than through the transaction asks the pool
for a *second* connection — and with as many concurrent writers as the pool
has connections, every writer holds one and waits for another that will never
be free. The failure is not a wrong answer, it is the service hanging until
request contexts expire.

`txReader` (`internal/domain/pgstore_opportunity.go`) exists for this: it
serves accounts, contacts, leads, opportunities, pipelines, stages, custom
field definitions and the outbox from an open transaction, so the shared rule
helpers can run inside one. Reading definitions from the transaction also
means a write is validated against the snapshot it commits in.
`internal/domain/pgstore_revenue_workflow_test.go` pins this with a
deliberately two-connection pool and more writers than that.

## Archiving a pipeline or a stage

Archival is retirement, not deletion: the record stays, so historical
reporting still explains itself. That makes it look like a safe, purely
additive flag, and it is not.

`activePipelineStage` resolves an Opportunity's *current* stage before any
other rule runs, and refuses an archived pipeline or stage. A deal left
standing on something that gets archived is therefore not merely inconvenient
— it is permanently unwritable. It cannot advance, cannot be closed, and
cannot be renamed. It stays readable and keeps its amount, so it goes on
counting toward anything summed later. No application path can undo it.

So archival is **refused while open deals still stand on the thing being
archived** (`internal/domain/pipeline_archival.go`, enforced in both stores).
The refusal is an `ErrConflict` that names the blocking deals and the step
that clears them, because a refusal an administrator cannot act on is only a
different kind of dead end. `/settings/pipelines` shows that sentence
verbatim rather than the generic conflict line.

Only *open* deals block. A closed deal cannot be moved off — every transition
out of a terminal stage is refused — so counting it would make any stage that
has ever closed a deal permanently unarchivable, and the remedy the refusal
names would be impossible to follow.

What decides "open" is the stage the deal currently stands on, read through
the same stage definitions the transition rules use, rather than a status
copied onto the opportunity. A copy is a second source of truth, and the one
that goes stale.
