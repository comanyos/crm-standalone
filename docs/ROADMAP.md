# Product delivery plan

Completion percentages measure verified acceptance criteria, not the presence
of source files. They will be updated with each delivered increment.

## M0 — Engineering baseline and governance (75%)

Dependencies: none.

- [x] Contribution guide, `.editorconfig`, Go lint policy, and CI
  (fmt/vet/lint/build/test + Compose config validation) are in place.
  `services/crm-api` lints at 0 issues; `services/authincore` has a
  pre-existing ~87-issue lint backlog predating this work, so its CI lint
  step runs informationally until that backlog is triaged.
- [x] Security reporting policy and supported-version policy published.
- [x] `services/crm-api`'s module path and NATS event-subject prefix no
  longer reference CompanyOS (renamed to `github.com/santoshguru/crm` and
  `crm.<entity>.<tenant>`). `services/authincore/third_party/
  platform-contract` is still an unlicensed vendored CompanyOS snapshot —
  open, larger decision (content, not just naming).
- [x] Commercial license/tier-gating code removed from both services
  (`internal/license/` in each, AuthinCore's `/api/v1/license` endpoints and
  admin-ui License page, `migrations/018_license.sql`) — it materially
  capped AuthinCore at one organization with no license active, which
  conflicted with this project's own multi-tenant requirement. Both
  AuthinCore build variants (`go build ./...` and `-tags enterprise`) and
  crm-api's lint/test/build all pass afterward.
- [ ] License provenance is **still not fully resolved** — `services/
  crm-api/LICENSE` is the Connect project's Apache-2.0 notice rather than
  this project's own license, AuthinCore has no `LICENSE` file for its
  claimed MIT terms, the `platform-contract` snapshot above is unlicensed,
  and a newly found migration seeds role/scope data for an unrelated
  "DBCore" product — see `docs/THIRD_PARTY_NOTICES.md`.
- [x] Environment/configuration matrices (`docs/CONFIGURATION.md`) and
  `APP_ENV`-gated production startup validation for both services — verified
  live via `make up` (boots normally in `development`) and by
  `internal/config` unit tests in each service (reject dev-default DB
  passwords, non-`https` issuer/JWKS/CORS URLs, and — AuthinCore — an
  ephemeral signing key, in staging/production). Disposable `--profile test`
  Postgres instances added (`make test-db-up`/`test-db-down`), verified
  healthy with migrations auto-applied and no data surviving teardown.
  Secret ownership/rotation documented for what's provisioned today;
  SMTP/OAuth/webhooks/object storage deferred to their Phase 8 decisions.
- [x] CRM SQL migrations are applied by a tracked Compose migration runner and
  recorded in `schema_migrations`, instead of depending on Postgres' one-time
  initdb hook. Verified against the existing development volume: the known
  baseline was recorded and `0012_pipeline_definitions` applied without a
  data reset.

Acceptance criteria: a new contributor can clone and run all documented
checks; no runtime/build path refers to the original CompanyOS workspace;
licensing is accurately represented before external distribution (not yet
true — see open findings above).

## M1 — AuthinCore bootstrap, tenant isolation, and RBAC (70%)

Dependencies: local Postgres/Redis/NATS stack; a local signing key; no external
provider credentials.

- [x] Isolated Compose architecture, migrations, CRM row-level security, JWT
  issuer/audience validation, and scope interceptor exist in source.
- [x] `authinctl bootstrap` reconciles a first organization, first admin,
  AuthinCore owner grant, CRM roles/scopes, and a public `crm-web` PKCE client.
- [x] Source test suites and embedded AuthinCore UI tests pass.
- [x] Local Compose verification proves bootstrap, Authorization Code + PKCE,
  consent, CRM JWKS validation/API access, refresh-token rotation, and
  revocation with no committed credentials.
- [x] Resource authority is derived from server-side role permissions; a
  browser-requested wildcard (for example `crm:*`) is filtered from human
  tokens and has a regression test proving a viewer cannot self-escalate.
- [x] Add Docker-backed bootstrap integration tests with two organizations.
  `services/authincore/cmd/authinctl/bootstrap_integration_test.go` runs
  against a real migrated database: a clean bootstrap creates the whole model
  and audits it, a second and third invocation insert nothing, and two
  organizations sharing an administrator email address and a client id stay
  completely separate. Added alongside: `authinctl reset-admin-password`
  (`make reset-admin-password`) for administrator recovery, because bootstrap
  deliberately never changes an existing password — with explicit
  per-account confirmation, an audit event carrying no secret, and a test
  proving a normal bootstrap does not rewrite a credential. Operator
  documentation is in `docs/BOOTSTRAP.md`.

  Found in passing, needing a decision: `migrations/025_dbcore_role_scopes.sql`
  installs a trigger that seeds three roles for the unrelated DBCore product
  into every organization created, and backfills existing ones. They carry no
  CRM scope — that is now asserted — but they are authority nobody in this
  product reviewed. Tracked with the other DBCore provenance items in M0.
- [~] Automate browser login, authorization-code exchange with PKCE, refresh,
  logout/revocation, JWKS validation, CRM API access, 401, and 403 behavior.
  The server-side half is done and passing. AuthinCore now has negative-path
  tests for authorization-code replay, redirect-URI substitution, a wrong and
  a missing PKCE verifier, refresh-token replay, and cross-client refresh
  redemption. The CRM validates active and previous signing keys and picks up
  a rotation without a restart, and a new `GET /readyz` reports `503` until it
  holds signing keys — kept separate from `/healthz`, so an AuthinCore blip
  takes an instance out of the load balancer instead of crash-looping it.
  Two fixes in passing: a token with an unknown key id could block a server
  goroutine for a minute before failing (attacker-chosen key id, so a cheap
  exhaustion vector), and the JWKS refresh interval was an hour, which made
  both the revocation window and readiness recovery an hour long.
  The web session model is now defined and documented in
  `docs/ARCHITECTURE.md`: what is stored and where (nothing in localStorage),
  what ends a session and what each trigger does to the AuthinCore session and
  the refresh token. Added with it: a 30-minute idle timeout and cross-tab
  sign-out, both ending only the local session and revoking that tab's refresh
  token, so a reload cannot restore a session the user was told had ended.
  CSRF state, the PKCE verifier and callback validation are covered as unit
  tests in `packages/auth-client` — driving a forged state through a real
  redirect would mean making the identity provider misbehave. Still missing at
  the browser tier: a two-tab spec for the new session policy. See
  The browser tier now also covers cross-tab sign-out across two real
  documents, that a locally ended session does not survive a reload while a
  live one does, and that no JWT reaches browser storage — 18/18 e2e passing.
  Two fixes came out of running it: the suite could not start at all on any
  machine that moved the dev server off port 3000 (the origin was hardcoded in
  three places while vite honours `VITE_WEB_PORT`), and a tab whose session
  ended elsewhere hung on "Redirecting to sign in…" forever instead of showing
  the signed-out confirmation. See `docs/ARCHITECTURE.md` for the two windows
  offline verification leaves open by design.
- [x] Prove API and database cross-organization isolation with two principals.
  Two organizations are seeded with a full record set each, then probed at
  four layers: all 36 CRM RPCs (`internal/api/tenant_isolation_test.go`), the
  store against live PostgreSQL (`internal/domain/pgstore_tenant_isolation_test.go`),
  row-level security read straight from the PostgreSQL catalog so a table
  added without a policy fails immediately, and the outbox dispatcher
  (`internal/dispatch/tenant_isolation_test.go`), whose one cross-tenant query
  is confined to the `crm_drain` role and to tenant *identifiers*. Every
  cross-organization read answers NotFound rather than PermissionDenied, so a
  refusal never confirms that a foreign id is real. Two findings were fixed in
  passing: tokens with no `exp` claim were accepted, and the default test DSN
  named an unrelated project's port, silently skipping the whole PostgreSQL
  tier. Tenant labelling of audit events and metrics stays open — neither
  surface exists yet (M6).
- [x] Scope enforcement is deny-by-default and the role matrix is pinned on
  both sides. `auth.ScopePolicy` refuses any procedure it has no entry for —
  the previous map enforced an unlisted procedure as public — and the server
  refuses to start if the catalog does not decide every procedure the
  generated descriptors say it serves. The four built-in roles
  (`crm_admin`/`crm_manager`/`crm_member`/`crm_viewer`) are specified with a
  rationale each in `packages/contracts/rbac/crm-roles.json`; AuthinCore's
  bootstrap and crm-api each have a test comparing themselves against it,
  because they are separate Go modules and drift between them is otherwise
  silent until a role hits a permission error in production. Enforcement is
  verified end to end over real HTTP with real signed tokens. Fixed in
  passing: `crm_member` and `crm_viewer` had no `crm:pipeline:read`, so a
  member could not create or edit an opportunity and a viewer could not open
  the board — existing organizations need `make bootstrap` re-run to pick the
  scope up.
- [ ] Add record-level FGA checks before exposing owner/editor/viewer
  controls. Blocked on the product owner's record-sharing policy, not on
  engineering: a scope in a token describes a type of record, never an
  instance, so "may edit accounts they do not own" cannot be expressed until
  someone decides who may grant it.

Acceptance criteria: a clean `make up` plus documented bootstrap command works
without committed credentials; an admin receives only tenant-local grants; a
valid AuthinCore token succeeds against CRM; wrong scope returns 403; a token
from org B cannot read or mutate org A records; JWKS validation survives a
service restart with the persistent development key.

## M2 — CRM web foundation and core records (75%)

Dependencies: M1 browser authentication contract and generated Connect client
or an equivalent typed contract client.

- [x] `apps/web` (React + TS strict + Vite), a design-system primitive set,
  an app shell (sidebar/topbar/command palette/toasts — desktop-only, not
  yet responsive to mobile/tablet), and the full auth session boundary:
  OIDC Authorization Code + PKCE login/callback, token renewal (refresh-token
  grant, not silent-iframe — AuthinCore's `X-Frame-Options: DENY` rules that
  out), logout, and error/not-found/unavailable states. Verified live end to
  end via `apps/web/tests-e2e/auth.spec.ts` (Playwright, 2/2 passing) and by
  hand against the real Compose stack.
- [x] Live accounts and contacts lists/detail/create/edit flows (Phase 3).
  Server-side search/filter/sort and keyset pagination, account hierarchy
  and activity timeline on the detail page, permission-aware actions,
  unsaved-change protection, optimistic-concurrency conflict handling, and
  non-blocking duplicate flags for both accounts and contacts. Verified
  live against the Compose stack. Related opportunities/leads on the
  account page is deliberately deferred to M3 — those list RPCs have no
  `account_id` filter yet, and adding one belongs with their own screens.
- [x] Loading, empty, and retryable-error states implemented as reusable
  primitives and now exercised by every account/contact screen, including
  distinct empty-tenant vs empty-search states and a tenant-safe not-found
  panel whose wording cannot distinguish "absent" from "another tenant's".
- [x] Browser tests for the login guard/session journey and for the
  account/contact journeys (`auth.spec.ts`, `accounts.spec.ts`,
  `contacts.spec.ts` — 14 tests, still not wired into CI since they need
  the live stack), plus 66 component/unit tests. The browser suite runs
  fully parallel; the intermittent failures that once kept it pinned to one
  worker were its own cancelled writes, not contention — see `task.md`
  Phase 3.2.

**Four real backend bugs were found and fixed while building and live-testing
this** (AuthinCore had never been exercised by a public/third-party-style
OAuth client before): no consent-approval UI existed at all (blocking every
public client's login), the consent endpoint only accepted JSON although
completing consent requires a real browser form-post navigation, a CSRF
check rejected genuine same-origin form posts once `Referrer-Policy:
no-referrer` made `Origin` come through as `"null"`, and a client-side
refresh-token race under React StrictMode. All four have regression tests.
See `task.md` Phase 2.3 for details.

Building the account/contact screens surfaced three more defects, all fixed
and covered by tests: a successful save prompted the user to discard the
changes it had just saved (the unsaved-changes guard blocked its own success
navigation); two account-detail sections reported "none" when their query had
actually failed; and the browser suite's sign-in was both racy and tripping
AuthinCore's per-IP login rate limit. See `task.md` Phase 3.2.

Acceptance criteria: no mock-only data path; a user can sign in, view only
authorized tenant data, create/update accounts and contacts through CRM API,
and see a clear recoverable error for an unavailable API. **Met for accounts
and contacts**, with one caveat: permission-denied behaviour is proven at the
component tier but not live, because no second, lower-scoped test principal
exists yet. Leads and opportunities screens remain M3.

## M3 — Revenue workflow (70%)

Dependencies: M2 core records and M1 scope enforcement.

- [x] Backend domain includes tenant-scoped leads, conversion, opportunities,
  stage history, activities, and database migrations.
- [x] Verify each backend workflow against Postgres/RLS, not only in-memory
  stores, including authorization and race/retry behavior. Leads, lead
  conversion and opportunities are now covered at two live tiers:
  `internal/domain/pgstore_revenue_workflow_test.go` exercises the store
  against real PostgreSQL (both as the owning superuser and as `crm_app`, the
  role the policies bind), and
  `internal/server/revenue_workflow_pg_test.go` drives the same workflows over
  real HTTP with real RS256 tokens, the real scope interceptor and that same
  RLS-bound store — the combination neither previous tier had, since scope
  enforcement was proven over `MemStore` and isolation over the store alone.
  Two organizations prove every cross-organization read *and* mutation
  answers NotFound; conversion is proven atomic by making it fail at its last
  step and showing the contact and account it had created are gone;
  stage history is proven contiguous under concurrent advances; and
  optimistic-concurrency conflicts are proven for both entities, at the store
  and over the wire. `internal/domain/revenue_store_parity_test.go` holds
  `MemStore` to the same observable contract, since that is the store the API
  suite and a `DATABASE_URL`-less deployment actually use.

  **Four defects were found and fixed, each with the regression test that
  fails without the fix** (see `docs/ARCHITECTURE.md` "Write concurrency"):

  1. Leads and opportunities had no optimistic-concurrency check, although
     both form pages had been round-tripping `updated_at` since they were
     built and `mutationErrorMessage` already had the conflict copy. The
     server accepted the field and discarded it, so a second editor's save
     silently overwrote the first. The field is now carried through
     `leadFromProto`/`opportunityFromProto` and compared in both stores.
  2. `UpdateOpportunity` ran its read, write, stage-history append and outbox
     enqueue as separate transactions with no lock between them. Concurrent
     stage advances each decided their transition from the same stale stage
     and each appended an entry leaving it, so the audit log branched and all
     but one of its entries described a move that never happened. It is now
     one RLS-bound transaction with `SELECT ... FOR UPDATE` on the deal.
  3. Both multi-statement write paths acquired a second pool connection from
     inside their own open transaction, to read custom-field definitions. With
     as many concurrent writers as the pool has connections, every writer
     held one and waited for another — the service hangs rather than
     answering wrongly. `ConvertLead` had this before this work; the fix
     covers both.
  4. `MemStore` had no serialization across its compound writes, so
     concurrent conversions of one lead each created their own contact,
     account and opportunity.

  Remaining at this tier: the metadata-layer tables
  (`custom_field_definitions`, `custom_object_definitions`, `custom_records`)
  are skipped by `TestUnscopedQuerySeesNothingInAnyTenantTable` because its
  fixture seeds no rows in them — the policies are asserted to exist, but not
  asserted to filter. That belongs with the metadata layer's own phase, not
  with the revenue workflow.
- [x] Build live Leads list/detail/create/edit screens, lifecycle controls,
  duplicate-contact warnings, and qualified-lead conversion confirmation using
  the generated CRM contract.
- [x] Build live Opportunity list/detail/create/edit/delete screens using the
  generated CRM contract. Opportunities are now bound to a tenant pipeline and
  stage; legacy records are migrated into the compatible Sales pipeline without
  losing their existing stage history. The form uses the organization default
  or a selected configured pipeline, and its server-side transitions use live
  stage definitions. A required close reason and close date are enforced from
  the configured terminal stage.
- [~] Introduce the tenant-scoped pipeline/stage schema: ordered stages,
  probability, terminal type, close-reason requirement, archive state, and
  RLS are now migrated and verified. Typed CRUD endpoints and the live
  permission-aware pipeline administration screen are implemented and covered
  by focused domain/API isolation tests. Administrators can now edit pipelines
  and stages, choose a default pipeline, and archive a pipeline while retaining
  its history; default changes are atomic. Opportunity `pipeline_id`/`stage_id`
  migration is complete; individual-stage archival and historical reporting
  remain pending. A live
  horizontal Kanban board now groups opportunities from the selected pipeline
  by their configured active stages; record changes still use the validated
  opportunity workflow rather than an unsafe drag-and-drop shortcut. Each
  opportunity now exposes its tenant-scoped, append-only stage history with
  timestamps through a typed read endpoint and live detail view.
- [ ] Add configurable pipelines/stages, stage validation, quotas, forecasts,
  and historical reporting semantics.
- [~] Build activities, tasks, notes, and timeline UI. A shared, live activity
  timeline is now available on account, contact, lead, and opportunity detail
  views; authorized users can append calls, emails, meetings, and notes using
  the existing tenant-scoped immutable Activity API. Tasks, reminders, activity
  list/calendar views, and attachment storage remain pending; attachment
  storage still needs object-storage ownership.

Acceptance criteria: a lead converts atomically to linked records; opportunity
stage history is accurate; every workflow is tenant-isolated and auditably
tested; the UI exposes it with permission-aware controls. **All four are now
met for leads, lead conversion and opportunities, verified against real
PostgreSQL with row-level security and over real HTTP with real tokens — see
the verification entry above for what was proven and the four defects it
found.** Still open in this milestone: individual-stage archival, historical
reporting semantics, quotas and forecasts, and the tasks/reminders/attachment
half of the activities entry. Attachment storage still needs object-storage
ownership from the product owner.

## M4 — Data productivity (25%)

Dependencies: M2/M3 stable record schemas and indexes.

- [~] Search, filtering, cursor pagination, saved views, tags, and bulk
  operations with server-side authorization. `SearchRecords` is now the
  tenant-scoped, bounded quick-search RPC used by the command palette. It
  searches account, contact, lead, opportunity, and task identifiers in the
  database with trigram indexes, has a dedicated `crm:search:read` scope, and
  has API isolation coverage. Entity filters/pagination exist where already
  implemented. **Tags are done**: tenant-scoped definitions and assignments
  for an explicit allowlist of record types, RLS-enforced, idempotent
  assign/remove, deny-by-default `crm:tag:*` scopes in the role matrix and
  AuthinCore bootstrap, a settings page, and an assign/remove panel on every
  taggable record's detail page — verified live against the running stack.
  Saved views and bulk operations remain pending.
- [~] Custom fields, deduplication/merge policy, CSV import validation, and
  export jobs with audit trails. Tenant administrators can already define
  typed custom fields and custom objects without runtime DDL; deduplication
  flags exist for accounts, contacts, and leads. Merge workflows, imports,
  exports, and their audit trails remain pending.
- [ ] Dashboard cards and basic reporting backed by query endpoints.

Acceptance criteria: large result sets remain paginated; bulk actions are
atomic or report per-record failures; imports are dry-runnable and exports are
tenant scoped.

## M5 — Communications and automation (75%)

Dependencies: provider ownership, OAuth credentials, retention policy, and
webhook security decisions from the product owner.

- [~] Email and calendar connection framework, encrypted credential storage,
  sync cursors, and communication timeline.

  **Done — the connection lifecycle.** A user can link, inspect, reconnect,
  disconnect and purge their own Google account end to end. Tenant-scoped
  durable storage (`migrations/0019`), one-time OAuth state with atomic
  consumption, an authenticated start RPC, an unauthenticated callback route
  that derives its tenant from the state record, server-side code exchange with
  PKCE, AES-256-GCM token sealing bound to `(tenant, owner)`, refresh with the
  reauthorization path, revocation-before-erasure on disconnect, outbox audit
  events, a bounded-label metric, and the Connected accounts page. Covered by
  store-parity tests across both stores, an end-to-end flow suite against a
  stub provider, and callback tests for the open-redirect and reflection
  surface. See [GOOGLE_INTEGRATION.md](GOOGLE_INTEGRATION.md).

  The web app also provides a provider-neutral task calendar and safe all-day
  `.ics` downloads.

  **Done — Gmail synchronisation.** Mail reaches contact timelines, on a
  background poller and on demand. Sync cursors with an incremental history
  walk and backfill resume, per-timeline-entry deduplication, contact matching
  by participant address, and Activities written in the same transaction as
  their index row. The privacy boundary is structural rather than a filter: a
  message is recorded only when a participant is already a Contact in the
  tenant, bodies are never fetched (`format=metadata`), and there is no body
  or snippet column to put one in.

  **Done — Calendar synchronisation.** Meetings involving known contacts reach
  their timelines, and the timeline is updated when one is moved or cancelled.
  Incremental sync on Google's own syncToken with a bounded first window,
  attendee and organizer matching that excludes every address Google marks as
  the account's own, timezone handling that keeps an all-day event on the day
  it actually falls, and a conflict policy: Google is authoritative, the
  mirror follows it in place, and only status and start changes reach the
  append-only timeline.

  **Done — data-subject erasure and retention.** Synced email and meetings can
  be erased on request, as redacted tombstones that keep deduplication working
  while removing every readable field, together with a suppression list that
  stops the next poll from re-collecting the same person. Retention
  (`SYNC_RETENTION_DAYS`, off by default) uses the same erasure inside the sync
  pass. Every erasure is audited through the outbox with counts and no content.

  **Not done — what the connection still owes.** Sync is one-way in both
  streams: the CRM never creates, moves or cancels an event, and `gmail.send`
  is requested but unused. Only the `primary` calendar is read. Recurring
  series sync as individual instances, so moving a series posts one entry per
  instance. Synced email and meetings attach to matched Contacts only, not to
  their Accounts or open Opportunities. `thread_id` and `recurring_event_id`
  are stored but nothing groups a conversation or a series in the UI. Sealing-key rotation still has no re-encryption path. Google verification submission stays deferred until a truthful demo
  can be recorded. Microsoft remains deliberately unconfigured.
- [~] Workflow engine, notifications, assignments, approvals, scheduled work,
  webhook signing/retries, and administration controls.

  **Done — the rule engine and in-app actions.** Rules fire on
  `lead.created`, `opportunity.stage_changed` and `opportunity.closed_won`,
  evaluate AND-only typed comparisons over allowlisted fixed fields, and
  create tasks, log activities and raise in-app notifications. The engine
  reads the same transactional outbox the bus does, on its own poller
  (`migrations/0023`, `internal/integration/workflow`).

  Two properties hold it up. Outbox delivery is at-least-once, so nothing
  decides whether work has been done by remembering: the unique key
  `(tenant, rule version, source event)` is the decision, and only the caller
  that wins the insert acts. And the event payload evaluates conditions but
  never drives a write — every action re-reads its target under the
  execution's own tenant scope, because the record may have been reassigned
  or deleted in between.

  Rules are versioned and immutable: editing publishes a new version and, by
  default, changes nothing about what is running, so an execution's ledger row
  still names the exact text that ran. Non-matching events are recorded as
  `skipped` rather than discarded, because "why didn't my rule fire" is the
  question authors ask. Failures are bounded codes, retries are capped at
  three with exponential backoff and resume from `actions_completed`, and a
  deleted record fails terminally on the first attempt. Loop prevention is
  structural — no trigger is an event a workflow action produces — and
  `AssertTriggersCannotLoop` checks that at startup rather than trusting it.

  `crm:workflow:*` is its own scope pair; notifications are the caller's own
  and neither RPC takes a recipient. The web app has an Automation screen
  whose every choice comes from `DescribeWorkflowCatalog`, so an editor cannot
  compose a rule the server would refuse, plus a polling notification bell.

  **Not done.** Approvals (a rule that waits needs a durable pause and a way
  to address the approver), time-based triggers and recurrence (need a
  scheduler), outbound webhooks with signing and retries (need an egress
  policy and a secret story), templated action text (needs a field-visibility
  model), custom fields in conditions (needs definition versioning), and
  multi-step chaining (needs the causation-depth limit the loop-prevention
  section names). See [WORKFLOW_AUTOMATION.md](WORKFLOW_AUTOMATION.md).

Acceptance criteria: provider credentials never reach browser storage or logs;
sync is idempotent; automations are auditable, tenant-isolated, and safely
retryable.

## M6 — Production readiness and release (25%)

Dependencies: target hosting, domains/TLS, production secrets/KMS, SMTP, and
backup ownership supplied by the product owner.

- [x] Containers use a persistent signing-key secret boundary and local health
  endpoints. CRM and AuthinCore now have safe request IDs, structured access
  logs, liveness/readiness endpoints, Prometheus HTTP/Connect metrics,
  database pool/query-latency gauges, NATS state, and safe CRM outbox/Auth
  OIDC/JWKS failure counters. Both ingress paths continue W3C `traceparent`
  and can export optional OTLP spans. The CRM browser → API → durable outbox
  → NATS path now persists/restores W3C context without putting request data
  in trace metadata. `docs/OBSERVABILITY.md` documents scrape/auth posture,
  labels, redaction, tracing, verification, and alert candidates. This is a
  local/internal observability foundation, not a production monitoring
  deployment.
- [ ] Add alert configuration, dashboards/SLOs/on-call, audit log export,
  trace propagation for integrations, retention controls, rate limits, security
  headers/CORS review, and dependency scanning. The Google connection's own
  outcome counter (`crm_google_oauth_flows_total`) is in place; the rest of the
  integration-specific metrics arrive with the sync jobs that need them.
- [~] Encrypted Compose logical backup/restore scripts, explicit destructive
  restore confirmation, and an expand/contract + rollback policy are in
  `docs/BACKUP_RESTORE.md`. They are local/operator tooling, not managed PITR
  or evidence of a restore drill.
- [ ] Select backup/PITR and retention policy, perform restore/upgrade drills,
  then add production deployment manifests, CI/CD, release checklist, and
  load/security tests.
- [ ] Confirm AuthinCore license provenance before external distribution.

Acceptance criteria: deployment and restore are rehearsed; no development
credentials are accepted in production; observability identifies tenant-safe
request failures; release sign-off includes security and license review.
