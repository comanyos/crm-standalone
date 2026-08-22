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
  Still browser-side: the Playwright flow itself, CSRF state validation, and
  the web token/session model. See `docs/ARCHITECTURE.md` for the two windows
  that offline verification leaves open by design.
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

## M3 — Revenue workflow (50%)

Dependencies: M2 core records and M1 scope enforcement.

- [x] Backend domain includes tenant-scoped leads, conversion, opportunities,
  stage history, activities, and database migrations.
- [ ] Verify each backend workflow against Postgres/RLS, not only in-memory
  stores, including authorization and race/retry behavior.
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
tested; the UI exposes it with permission-aware controls. **Lead and basic
opportunity UI implementation are complete; configurable pipelines, Kanban,
stage-history UI, and live workflow/tenant/RBAC verification remain in the QA
and product workstreams.**

## M4 — Data productivity (0%)

Dependencies: M2/M3 stable record schemas and indexes.

- [ ] Search, filtering, cursor pagination, saved views, tags, and bulk
  operations with server-side authorization.
- [ ] Custom fields, deduplication/merge policy, CSV import validation, and
  export jobs with audit trails.
- [ ] Dashboard cards and basic reporting backed by query endpoints.

Acceptance criteria: large result sets remain paginated; bulk actions are
atomic or report per-record failures; imports are dry-runnable and exports are
tenant scoped.

## M5 — Communications and automation (0%)

Dependencies: provider ownership, OAuth credentials, retention policy, and
webhook security decisions from the product owner.

- [ ] Email and calendar connection framework, encrypted credential storage,
  sync cursors, and communication timeline.
- [ ] Workflow engine, notifications, assignments, approvals, scheduled work,
  webhook signing/retries, and administration controls.

Acceptance criteria: provider credentials never reach browser storage or logs;
sync is idempotent; automations are auditable, tenant-isolated, and safely
retryable.

## M6 — Production readiness and release (5%)

Dependencies: target hosting, domains/TLS, production secrets/KMS, SMTP, and
backup ownership supplied by the product owner.

- [x] Containers use a persistent signing-key secret boundary and local health
  endpoints; CRM includes request IDs and structured HTTP logs.
- [ ] Add metrics, tracing correlation, alertable health checks, audit log
  export, rate limits, security headers/CORS review, and dependency scanning.
- [ ] Define encrypted backups, restore drills, migration rollback policy,
  deployment manifests, CI/CD, release checklist, and load/security tests.
- [ ] Confirm AuthinCore license provenance before external distribution.

Acceptance criteria: deployment and restore are rehearsed; no development
credentials are accepted in production; observability identifies tenant-safe
request failures; release sign-off includes security and license review.
