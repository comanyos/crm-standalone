# Product delivery plan

Completion percentages measure verified acceptance criteria, not the presence
of source files. They will be updated with each delivered increment.

## M0 — Engineering baseline and governance (75%)

Dependencies: none.

- [x] Contribution guide, `.editorconfig`, Go lint policy, and CI
  (fmt/vet/lint/build/test + Compose config validation) are in place.
  `services/crm-api` is the only Go service and its checks are release gates.
- [x] Security reporting policy and supported-version policy published.
- [x] `services/crm-api`'s module path and NATS event-subject prefix no
  longer reference CompanyOS (renamed to `github.com/santoshguru/crm` and
  `crm.<entity>.<tenant>`). The unlicensed AuthinCore/platform-contract source
  was subsequently removed in full.
- [x] Commercial license/tier-gating code and the separate AuthinCore service
  were removed. Current identity is a small first-party package inside
  `crm-api`; the historical provenance findings remain recorded in
  `docs/THIRD_PARTY_NOTICES.md` and `docs/EXTRACTION.md`.
- [~] The repository now carries an Apache-2.0 project license and corrected
  copyright notice. A final automated SBOM/license scan and owner/legal
  confirmation remain required before public distribution.
- [x] Environment/configuration matrices (`docs/CONFIGURATION.md`) and
  `APP_ENV`-gated production startup validation for the CRM API — verified
  live via `make up` and by `internal/config` tests (rejecting development DB
  passwords, weak OTP peppers, insecure production cookies/origins, and
  partial SMTP/Google login configuration). Disposable `--profile test`
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

## M1 — Native authentication, tenant isolation, and RBAC (in progress)

Dependencies: CRM PostgreSQL and SMTP/Google credentials only when those
external login paths are exercised.

- [x] Remove the separate AuthinCore service, its second PostgreSQL database,
  Redis dependency, hosted/admin UIs, JWT/JWKS boundary, and web OIDC client.
- [x] Add first-party `crm-api` routes for email challenge request/verification,
  Google start/callback, session read, and logout.
- [x] Store OTPs, OAuth state, and opaque sessions only as digests in the CRM
  database; enforce OTP expiry, single use, attempt limits, resend limits, and
  revocable session expiry.
- [~] Carry web sessions in a Secure/HttpOnly/SameSite cookie and accept the
  same opaque value as a Bearer token. Bearer validation exists, but dedicated
  mobile issuance/exchange is still required. No JWT or refresh token is
  stored in the browser.
- [x] Keep users, organizations, active memberships, four CRM role keys, and
  onboarding metadata in the CRM database. Derive tenant and scopes from the
  verified session/membership; never accept a client-selected tenant or role.
- [~] Complete production-shaped abuse, account-enumeration, SMTP delivery,
  Google login, Origin/cookie, bootstrap, cross-tenant, revocation, and mobile
  one-time-handoff acceptance tests.
- [ ] Add dedicated mobile completion/exchange for both email and Google;
  current successful authentication issues only the web HttpOnly cookie.
- [ ] Design record-level sharing only after the product owner defines who may
  grant/view/edit individual records; the type-level scope policy remains
  deny-by-default.

Acceptance criteria: a clean stack bootstraps a first administrator without a
default password; email OTP and Google sign-in create/recover only intended
accounts; a revoked/expired opaque session is refused immediately; roles and
tenant come from an active local membership; a principal from organization B
cannot read or mutate organization A.

## Historical M1 — removed AuthinCore implementation

The section below records work completed against the former identity platform.
It is retained as engineering history only and is not an operational guide,
current architecture, or runnable acceptance procedure.

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

Historical acceptance criteria were met for that implementation. They were
superseded by the native-auth criteria above when AuthinCore was removed.

## M2 — CRM web foundation and core records (75%)

Dependencies: M1 native session contract and generated Connect client
or an equivalent typed contract client.

- [x] `apps/web` (React + TS strict + Vite), a design-system primitive set,
  an app shell (sidebar/topbar/command palette/toasts; responsive since
  the drawer/stacking/a11y work recorded in `task.md` § Web application — the
  earlier "desktop-only" note here was stale), and an auth session boundary
  now migrated to the CRM API's email-OTP/Google login and HttpOnly opaque
  session cookie. Login, session restore, logout, and unavailable/error states
  have component coverage; live SMTP and Google-account acceptance remain M1
  launch gates.
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

**Historical note:** four backend bugs were found while live-testing the former
AuthinCore OAuth client: no consent-approval UI existed at all (blocking every
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
the former identity service's per-IP login rate limit. See `task.md` Phase 3.2.

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

## M4 — Data productivity (70%)

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
  native CRM bootstrap, a settings page, and an assign/remove panel on every
  taggable record's detail page — verified live against the running stack.
  **Bulk operations are also started**: bounded (≤200 ids), per-item-
  authorized bulk RPCs for tag assign/remove, task complete/reopen, task
  reassignment, and account/lead owner reassignment — each id succeeds or
  fails independently and is reported back, not all-or-nothing — with
  selection UI on the tasks and accounts lists. **Saved views are also
  done**: private, per-owner filter/sort/column presets per entity list,
  RLS-enforced, owner-scoped the same way notifications are (no query
  shape can reach another user's view), with a save/apply/delete picker on
  the accounts list, verified live. Column selection is stored but not yet
  applied by any list UI. Sharing a view with teammates is out of scope
  until record-level authorization exists.
- [~] Custom fields, deduplication/merge policy, CSV import validation, and
  export jobs with audit trails. Tenant administrators can already define
  typed custom fields and custom objects without runtime DDL; deduplication
  flags exist for accounts, contacts, and leads. **Account merge is done**:
  field-by-field resolution, relationship migration (contacts,
  opportunities, child accounts, activities/tasks/notes, ownership history,
  tags, and dangling dedup flags on other accounts), an immutable audit
  trail, and an explicitly-documented no-undo policy — verified live, with
  two bugs live testing found and fixed. Contact and lead merge are not
  implemented. **CSV import/export for accounts is also done**: a
  documented product decision record (`docs/CSV_IMPORT_EXPORT.md`) written
  before implementation, a two-call preview/commit import with field
  mapping and per-row errors, and a bounded inline export with an audit
  trail — both verified live. Both are synchronous rather than truly
  asynchronous, and neither covers contacts, leads, opportunities, or
  tasks — named scope narrowings, not silent gaps.
- [x] Dashboard cards and basic reporting backed by query endpoints. A single
  bounded `GetDashboardReport` RPC (`crm:summary:read`, the same scope
  `GetSummary` already uses) composes one page (≤500 rows) each of a
  tenant's pipelines/opportunities/tasks/leads through pure, unit-tested
  aggregation functions (`internal/domain/reporting.go`): a per-pipeline
  stage breakdown with won/lost totals (from `PipelineStage.terminal_type`,
  never a stage's name or position), a task status summary with an overdue
  count, and the lead conversion funnel with its rate. It also surfaces the
  weighted pipeline forecast — a pre-existing, previously-unwired
  `PipelineForecast`/`DefaultStageProbabilities` rollup found already
  written and tested in the domain layer — labeled in the UI as a default
  weighting, not a confirmed methodology, per its own doc comment. Canonical
  metric semantics (currency: raw tenant amounts, no conversion; dates:
  UTC; pipeline status: from `terminal_type`) are documented directly in
  `reporting.go` rather than assumed. Covered by a cross-tenant isolation
  probe and, on the web side, a dashboard page (pipeline cards, task tiles,
  funnel, forecast table) — verified live against the running stack: a real
  opportunity's amount flowed through to its pipeline's stage count, the
  tenant total, and the forecast bucket ($4,200 at the Prospecting stage's
  10% default weight correctly showed as a $420 weighted forecast).

Acceptance criteria: large result sets remain paginated; bulk actions are
atomic or report per-record failures; imports are dry-runnable and exports are
tenant scoped.

Still open within M4: contact and lead merge (account merge only); CSV
import/export covers accounts only; saved views and bulk-operation selection
UI exist on some entity lists (accounts, tasks) but not all; saved-view
column selection is stored but not yet applied by any list UI; and
custom-field-aware search/filter integration has not been started.

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
  Ordered actions now accept a bounded `delay_minutes`; the execution ledger
  stores the next due instant, survives restarts, and resumes at the correct
  action. Every in-app notification is also placed on a durable SMTP queue
  with a lease, bounded retries, and capped exponential backoff.

  **Not done.** Approvals (a rule that waits needs a durable pause and a way
  to address the approver), wall-clock triggers and recurrence, outbound
  webhooks with signing and retries (need an egress
  policy and a secret story), templated action text (needs a field-visibility
  model), custom fields in conditions (needs definition versioning), and
  cross-rule chaining (needs the causation-depth limit the loop-prevention
  section names). Delayed multi-step sequences inside one rule are complete.
  See [WORKFLOW_AUTOMATION.md](WORKFLOW_AUTOMATION.md).

Acceptance criteria: provider credentials never reach browser storage or logs;
sync is idempotent; automations are auditable, tenant-isolated, and safely
retryable.

## M7 — Privyr parity: speed-to-lead (80%)

Dependencies: none for the delivered items; the rest are listed with the
product-owner decision each waits on in `task_crm.md`.

`task_crm.md` is the engineering source of truth for this milestone. It asks a
narrower question than M0–M6: what does Privyr do that this codebase cannot do
at all? Most of Privyr's surface this product already exceeds, so the file — and
this milestone — covers only the genuine zeroes.

- [x] **Messaging consent ledger and its gate** (`task_crm.md` §2,
  `migrations/0028_messaging_consent.sql`). One function, `domain.MayMessage`,
  that every send path passes through, over an append-only ledger keyed on
  (channel, address) rather than on a contact — so a re-import cannot resurrect
  permission from somebody who said no. Messaging channels are derived from
  `Contact.Email`/`Contact.Phone` rather than stored, so no channel table
  exists. Verified over real HTTP with real tokens against RLS-bound
  PostgreSQL: the read/withdraw/re-read loop, the scope split, and that one
  tenant can neither read nor overturn another's withdrawal.

  Two defects found by its own tests and fixed: a same-instant grant/withdrawal
  tie resolved at random (it now resolves to withdrawn, since a tie means the
  order is unknown and the restrictive answer is the safe one), and a local
  phone number and its international form were different consent keys, so a
  withdrawal against one did not stop a message to the other. A number without
  a country code is now refused at both ends rather than having one guessed —
  guessing `+1` for a tenant elsewhere produces a reachable *different
  person's* number.

- [~] **Click-to-chat outreach** (`task_crm.md` §3). WhatsApp, SMS, phone and
  email actions on a contact, opening the user's own app with the message
  pre-filled. Nothing is sent from the CRM, and the timeline entry says
  "opened" rather than "sent" for exactly that reason. Covered by 12 component
  tests including an axe-core scan, plus unit tests for the two things that
  fail silently at the recipient's end: `wa.me` wants bare digits where `tel:`
  wants the `+`, and `mailto:` needs `%20` where `URLSearchParams` emits `+`.
  **Open:** a real-device check, which its own acceptance criteria require —
  deep-link behaviour differs by OS and the `sms:` body spelling is empirical.

- [x] **Message templates** (`task_crm.md` §5,
  `migrations/0030_message_templates.sql`). Tenant-shared wording with an
  allowlisted `{{merge}}` vocabulary, refused at save time if unknown. Merge
  fields shipped in this version rather than waiting on the field-visibility
  model, because rendering happens in the browser from the record already on
  screen — a user cannot leak to themselves what they are looking at. A
  server-side render, which an automated send needs, still inherits that
  blocker.

- [x] **Round-robin lead distribution** (`task_crm.md` §8,
  `migrations/0029_lead_distribution.sql`). An `assign_owner` workflow action
  rather than a second engine, so it inherits rule versioning, conditions, the
  execution ledger, retries and loop prevention. The cursor is locked
  `FOR UPDATE` and the owner change, the ledger row and the advance are one
  transaction. Verified against live PostgreSQL: nine concurrent assignments
  take nine distinct turns with an even split, and a failed assignment leaves
  the rotation exactly where it was rather than burning a turn.

- [x] **Inbound lead capture** (`task_crm.md` §1,
  `migrations/0031_lead_capture.sql`, `docs/LEAD_CAPTURE.md`). An external
  system POSTs a signed lead and an ordinary Lead appears in the right tenant,
  emitting the same `lead.created` event so existing automations — including
  the rotation above — fire on it unchanged.

  D1 is answered in the decision record rather than left open: unauthenticated
  because the caller is a third-party server, behind a required HMAC-SHA256
  signature with no unsigned mode, an unguessable token that also resolves the
  tenant on the drain pool, a body cap and a per-endpoint rate limit. The
  response is uniform by design — an unknown token and a bad signature answer
  identically, or the response is an oracle for whether a token is real.

  Verified against the running Compose stack, not only in tests: two identical
  signed deliveries produced exactly one lead linked to its delivery, an
  unsigned one was recorded as rejected and created nothing, and the captured
  lead emitted `lead.created`. That pass found two defects the suite could not
  — the new configuration key tripped Google's all-or-nothing check and
  crash-looped the service, and the delivery log recorded "created" without
  recording what it created.
- [~] Mobile speed-to-lead (`task_crm.md` §10.1–10.3). The app is installable
  — manifest, icons, and a service worker that caches the app shell and
  deliberately no data — and there is now a `mobile-chromium` Playwright
  project asserting the four things only a real browser knows: the drawer, no
  sideways scroll, main's actual width, and tap target size.

  Two verification gaps remain, both environmental rather than open work: the
  in-app browser's sandbox refuses to register a service worker at all, so
  installability needs a check in an ordinary browser; and the Playwright
  project has not been run here, because the suite signs in first and this
  machine has no bootstrapped organization.

  **Push on a new lead (§10.2) is not done** — it needs provider and key
  ownership. A focused Flutter application for iOS and Android is now an
  approved product direction; its native CRM-session handoff, capability-contract, security,
  offline boundary, test and release plan is documented in
  `docs/FLUTTER_MOBILE_ARCHITECTURE.md`. This supersedes the earlier PWA-only
  recommendation in §10.4.
- [x] **My Day, instant lead alerts and SLA escalation**
  (`migrations/0040_speed_to_lead.sql`). The signed-in rep gets one bounded
  daily queue for overdue, due-today and upcoming tasks plus assigned new and
  untouched leads. A five-second worker creates an owner notification for a
  new assigned Lead, falls back to workspace supervisors for an unassigned
  Lead, and escalates a still-new/no-activity Lead to its owner and supervisors
  after `LEAD_SLA`. Claims and notifications are transactional and idempotent;
  email uses the durable notification-delivery queue above. Existing rows are
  backfilled as already alerted so deploying the migration sends no historical
  mail burst, while My Day still exposes old untouched work.
- [x] **Automations draft messages** (`task_crm.md` §6,
  `migrations/0032_outbound_messages.sql`). A `draft_message` workflow action
  that checks consent, renders a template and leaves a personalised message in
  the record owner's queue, one tap from being sent through the click-to-chat
  links. It drafts and does not send: nothing here can deliver a message.

  Two blockers listed against this section turned out not to apply, and the
  reasons are recorded rather than assumed. D6 (quiet hours) governs automatic
  *sending* — a draft in a queue wakes nobody — and becomes required the
  moment anything drains it. And server-side template rendering is safe for
  this vocabulary because the merge allowlist is fixed-schema fields while
  field-level visibility covers custom fields only, and because type-level
  scopes mean a rule author can already read every record a rule could fire
  on; that second reason expires if record-level authorization ever lands.

  Consent is re-checked when the queue is read and again when a rep presses
  send, because a rule can draft on Monday and a rep can open the queue on
  Wednesday.

- [~] **Sending a drafted message through Gmail** (`task_crm.md` §4, the
  `gmail.send` rung). Built on the product owner's instruction. It sends one
  drafted message as the acting user's own connected mailbox, when a person
  presses the button; nothing drains the queue on a timer, which is what keeps
  quiet hours (D6) out of scope. See `docs/GOOGLE_INTEGRATION.md` § *Sending*.

  **Unverified against real Gmail.** The message bytes, the header-injection
  refusals, the provider-error redaction and the no-connection paths are
  asserted against a stub; no message has been sent, which needs a real OAuth
  client. Connect a mailbox and send one draft before trusting it.

- [ ] WhatsApp and SMS sending (§4), bulk campaigns (§7), trackable files
  (§9), push (§10.2) and ad-conversion feedback (§11) are each held for a
  stated reason in `task_crm.md` rather than merely unstarted — a provider
  contract, a legal review, object-storage ownership, a push provider, and an
  outbound PII decision respectively.

Acceptance criteria: an external lead source delivers into the correct tenant
and fires existing automations unchanged; a rep reaches a contact in one tap
and the timeline records the attempt honestly; consent is durable, auditable
and enforced by a single gate proven total by a test; new leads distribute
across a team exactly once each. **Three of the four are met**; the
remaining one — a rep reaching a contact in one tap — waits on a real-device
check that jsdom cannot give it.

## M6 — Production readiness and release (25%)

Dependencies: target hosting, domains/TLS, production secrets/KMS, SMTP, and
backup ownership supplied by the product owner.

- [~] The CRM container has local health endpoints, safe request IDs,
  structured access logs, Prometheus HTTP/Connect metrics, database
  pool/query-latency gauges, NATS state, and outbox/integration counters. The
  native OTP/Google/session paths still need dedicated bounded counters and
  production alert thresholds. CRM ingress continues W3C `traceparent`
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
- [ ] Run a final SBOM/license scan and confirm the repository's Apache-2.0
  grant before external distribution. AuthinCore provenance is historical
  because that source is no longer distributed.

Acceptance criteria: deployment and restore are rehearsed; no development
credentials are accepted in production; observability identifies tenant-safe
request failures; release sign-off includes security and license review.
