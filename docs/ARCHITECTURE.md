# Architecture

Savvy CRM is one Go application service plus its browser client and supporting
workers. Authentication is a small product-specific module inside
`services/crm-api`; there is no separate identity platform.

1. A user requests a six-digit email code or begins Google sign-in.
2. The CRM API verifies the challenge and creates an opaque random session.
3. Only a SHA-256 digest of the session token is stored in CRM Postgres.
4. Browsers receive the token as a Secure, HttpOnly, SameSite cookie. Native
   clients may present the same opaque token as a Bearer credential from OS
   secure storage.
5. On every protected request the server resolves the active user,
   organization membership, role, and scopes. Tenant and role values are never
   accepted from a browser form or provider-editable metadata.
6. The existing fail-closed RPC scope policy and PostgreSQL row-level security
   continue to enforce each tenant boundary.

## Native authentication boundary

The minimal auth schema owns users, organizations, memberships, passwordless
login challenges, OAuth state, and sessions. Business tables keep their
existing text `tenant_id`; an organization's UUID is used as that value.

`AUTH_BOOTSTRAP_ORG_NAME`, `AUTH_BOOTSTRAP_EMAIL`, and the optional stable
`AUTH_BOOTSTRAP_ORG_ID` create the first owner membership idempotently at
startup. Signup creates a new organization and its first owner only after the
email address or Google identity has been verified.

The role model remains `owner`, `crm_admin`, `crm_manager`,
`crm_member`, and `crm_viewer`. The server maps those roles to the fixed
CRM scope contract. A signup request cannot choose an authorization role;
profile fields such as job title and team size are descriptive onboarding
data only.

### Email OTP

`POST /auth/email/request` creates a short-lived challenge. Codes are random,
single-use, attempt-limited, and persisted only as an HMAC digest bound to the
challenge, email, and purpose. Login requests return a generic response so an
attacker cannot discover registered addresses. SMTP failure never produces a
false "sent" response.

`POST /auth/email/verify` consumes the challenge atomically. A login
challenge requires an active membership; a signup challenge transactionally
creates the user, organization, owner membership, onboarding profile, and
session.

### Google identity

Google account sign-in has a dedicated OAuth client and identity-only scopes.
It must not reuse the Gmail/Calendar integration client or request mailbox
permissions. State, PKCE, callback address, verified email, and account
linking are validated server-side before a session is issued.

### Sessions and CSRF

Session tokens are never written to localStorage or sessionStorage. Browser
requests use `credentials: include`; JavaScript receives only the safe
session profile. Logout deletes the server-side session before expiring the
cookie. Expiry and deactivation are immediate on the next request because
membership is loaded from the database rather than copied into a long-lived
JWT.

Cookie-authenticated unsafe requests must carry an allowed `Origin`. The
authenticated app allowlist can reach CRM RPCs; the separate marketing-site
allowlist can reach only capabilities, Google start, and email request/verify.
CORS is
an exact allowlist and production entries must be HTTPS. Bearer sessions are
reserved for native/mobile clients and are not exposed to the marketing site.

## Health and readiness

| Endpoint | Meaning |
| --- | --- |
| `GET /healthz` | The process is serving HTTP. |
| `GET /readyz` | CRM Postgres and the native session store are reachable. |
| `GET /metrics` | Internal metrics; token-protected in staging/production. |

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
