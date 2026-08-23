# Standalone CRM — Completion Task Plan

## Purpose

Deliver a standalone, production-ready multi-tenant CRM from this repository.
The finished product must not depend on CompanyOS source, runtime services,
container mounts, module replacements, or credentials.

This document is the detailed execution checklist. A checkbox may be marked
complete only after its stated acceptance criteria and verification have passed.
`docs/ROADMAP.md` remains the concise stakeholder progress view; this file is
the engineering source of truth for work remaining.

## Non-negotiable boundaries

- Work only in `/Users/santoshguru/Desktop/crm`.
- Do not copy further CompanyOS files without explaining the exact need and
  obtaining approval.
- Preserve the current service boundary: `services/authincore` owns identity,
  organizations, authentication, authorization, and FGA; `services/crm-api`
  owns CRM data and workflows; `apps/web` owns the browser application.
- AuthinCore `org_id` is the CRM `tenant_id`. No request may choose a tenant
  from browser-supplied body data or query data.
- Every new CRM table must include `tenant_id`, use a tenant-aware key/index,
  have RLS enabled and forced, and be accessed with the scoped database helper.
- Browser authentication uses OIDC Authorization Code with PKCE. Never place a
  client secret in the web application.
- Do not commit passwords, OAuth secrets, private keys, tokens, certificates,
  database dumps, provider exports, or generated build output.
- Existing user changes are preserved. Do not use destructive Git commands.

## Product definition of done

The CRM is 100% complete only when all of the following are true:

1. A new operator can deploy from documented artifacts, create an organization
   and first administrator without source edits, and securely log in.
2. At least two organizations are proven unable to read, mutate, search,
   export, automate, or receive each other's data.
3. A responsive web CRM supports the agreed core workflows using live backend
   contracts—not mocks—and communicates loading, error, empty, and forbidden
   states clearly.
4. CRM entities, communications, automation, imports/exports, reports, and
   administration are complete, authorized, observable, and tested.
5. Backups/restores, upgrades, monitoring, security hardening, delivery
   automation, documentation, legal/license review, and release sign-off have
   been rehearsed for the chosen production environment.

## Current verified baseline

Latest verified update (2026-08-22): two-organization isolation (§1.3) is now
proven by an automated suite at the RPC, application, database and
asynchronous-worker layers, run against live PostgreSQL with row-level
security applied to the non-bypassing `crm_app` role. Earlier the same day:
the standalone CRM web application completes its local PKCE callback on
`http://localhost:3002` when the default port is occupied, and AuthinCore
filters browser-requested resource wildcards from human tokens, so CRM
authority can only come from server-side role grants.

| Area | Status | Evidence / limitation |
| --- | --- | --- |
| Standalone extraction | Complete | Architecture and source live entirely in this repository. |
| Compose stack | Running locally | PostgreSQL ×2, Redis, NATS, AuthinCore, Auth Admin, CRM API. |
| Auth bootstrap | Implemented | `make bootstrap` reconciles an org, admin, roles, scopes, and public SPA client. |
| OIDC/JWT integration | Live-verified via an automated browser test | `apps/web/tests-e2e/auth.spec.ts` (Playwright) exercises PKCE login, consent, live dashboard, reload, and logout end to end against the real stack. JWKS refresh/rotation and negative-path cases still need coverage. |
| Tenant isolation | Proven by automated tests at four layers | All 36 CRM RPCs, the store against live PostgreSQL, a catalog-driven RLS check covering every table carrying `tenant_id`, and the outbox dispatcher's `crm_app`/`crm_drain` split. Audit-event and metric tenant labelling stays open because neither surface exists yet (§10.2). A token already issued outlives organization deactivation until it expires — see §1.3. |
| Backend domain | Partially implemented | Accounts, contacts, leads, conversion, opportunities, activities, RLS, and tests exist. Production/contract coverage is incomplete. |
| Web app | Partially implemented | `apps/web` has a working design system, app shell, OIDC session boundary, live dashboard, and live CRUD workflows for accounts, contacts, leads, opportunities, and tasks, plus pipeline administration, an opportunity Kanban board, and an activity timeline with notes on four detail views. Search/automation/reporting screens and the remaining product workflows are still pending. |
| Production operations | Not started | Local keys/credentials and Compose are development-only. |

Approximate total product completion: **55%** of the scoped product, and
**~40%** including production operations. This is deliberately not an average
of files present. The core record layer is live end to end — 47 RPCs and
browser CRUD for accounts, contacts, leads, opportunities, and tasks — while
communications, automation, reporting, bulk import/export, record-level
permissions, and audit storage are not built, and nothing is deployed.

Measured against the products a buyer compares this with, completion is far
lower — roughly 15%, because the metadata, automation, reporting, and
extensibility layers that define those products do not exist here. See
`task_v2.md`, which plans that work; this file continues to track the
originally scoped product.

## Shared engineering standards

### API and data

- [ ] Define versioned API contracts before a UI feature begins. Regenerate
  Connect/TypeScript clients as part of the same change.
- [ ] Validate all untrusted input at the HTTP/RPC boundary; return stable,
  user-safe error codes/messages.
- [ ] Use cursor pagination for every potentially unbounded list. Define a
  maximum page size and stable ordering.
- [ ] Create append-only migrations. Never edit a shipped migration.
- [ ] Add database indexes based on actual filter/order query shapes.
- [ ] Add RLS, authorization, migration, and cross-tenant tests for each new
  tenant-owned entity.
- [ ] Use transactional outbox events for integrations/workflows. Consumers
  must be idempotent and include tenant context.
- [ ] Maintain audit events for security-sensitive and user-visible actions.

### Web application

- [ ] Use TypeScript with strict compiler settings and a tested build tool.
- [ ] Create a small design system: tokens, typography, colors, spacing,
  buttons, form controls, tables, dialogs, toasts, badges, skeletons, and
  status/empty/error/forbidden patterns.
- [~] Responsive tablet and mobile layouts. The app shell was a permanently
  visible 14rem sidebar with no responsive behaviour, so on a 375px phone the
  page got 151px and a two-column form field measured **44px** — the figures
  are from Chromium against the built stylesheet, not an estimate. Below `lg`
  the sidebar is now an off-canvas drawer opened from a Topbar button, and
  main takes the full width (375px on a phone, 768 on a tablet); from `lg` up
  it is the permanent column it always was.

  One `<nav>` serves both, shown or hidden, rather than a duplicate for small
  screens — two would put every destination in the accessibility tree twice.
  Closed, it is `display: none` rather than translated off-screen, so it
  leaves the tab order instead of becoming an invisible focus trap; opening it
  moves focus into the panel, and Escape, the Close button, the scrim and any
  navigation all dismiss it.

  Detail-page definition lists and paired form fields now stack below `sm`,
  and PageHeader stacks its title and actions. The opportunity board and the
  shared Table already scrolled horizontally; PipelinesPage was already
  responsive.

  Static accessibility is now asserted, not assumed. `src/test/a11y.ts` runs
  axe-core over a rendered container and fails with the violations named; the
  shell (both drawer states), a list page, a form page and the board use it.
  The layout-dependent rules are switched off rather than left to report
  "incomplete" forever — jsdom has no stylesheet and no layout, so contrast
  and target-size cannot be judged here. What is left is the markup half:
  roles, names, label association, landmarks, table headers.

  It found two real defects. Five list pages rendered an empty `<th>` for
  their badge and action columns, and a screen reader announcing a cell names
  its column — so those cells were announced with no context at all.
  `Table`'s `Column` now takes `headerHidden`, which renders the header for
  screen readers only: the text is required, hiding it is the design choice.

  The second is why the primitive counts as accessible now rather than
  before. Activatable rows carried `role="button"` and `tabIndex`, which took
  the `<tr>` out of the table — its cells stopped being cells, so the
  row/column relationship was lost — and made every real control inside a row
  a control nested inside a control (`nested-interactive`, serious, on the
  tasks and contacts tables). The row is a row again; the activating control
  is a button in the row's header cell, so the keyboard path is a real named
  control rather than a row pretending to be one. `Column.rowHeader` marks
  which column names the row, because the first column is not always it — the
  tasks table leads with a completion checkbox, and wrapping that in the
  activating button nested one control inside another all over again.
  Keyboard activation and the row header are both pinned by tests.

  Coverage now includes the settings and pipelines screens, which had no
  tests at all. Those are the dialog-heavy ones, so each dialog is opened and
  scanned — axe only sees what is rendered, and a closed dialog is not
  rendered. It found a sixth empty `<th>`, on the custom objects table: the
  earlier sweep's search missed it because that file writes `header:""` with
  no space. Both settings pages and the pipelines page are also scanned as a
  reader without write scope, since hiding controls changes the markup.

  Still open: the browser half. No Lighthouse run, no axe against the running
  app (contrast and target size are only checkable there), and no
  visual-regression snapshots — so a layout regression is still caught by
  review or not at all. The viewport numbers above came from a one-off
  Chromium run, not a checked-in test.

  Noticed while covering these, not fixed: archiving asks for confirmation
  two different ways on the same screen. Pipeline and custom field/object
  archival still go through `window.confirm`, while stage archival uses the
  app's own `ConfirmDialog`.

- [ ] Meet WCAG 2.2 AA for keyboard navigation, focus management, contrast,
  labels, validation, and screen-reader feedback.
- [ ] Make desktop workflows efficient and mobile/tablet layouts usable.
- [ ] Use typed live API clients. Mock service workers may be used only for
  isolated component tests, never as the sole feature implementation.
- [ ] Do not persist access tokens in localStorage. Prefer in-memory tokens
  with refresh strategy appropriate to the final auth architecture.

### Quality gates

- [ ] Unit tests cover domain invariants, authorization decisions, parsing,
  validation, and error conditions.
- [ ] Integration tests run against real PostgreSQL and exercise migrations,
  RLS, OIDC, and Connect endpoints.
- [ ] Browser tests cover each critical user journey.
- [ ] CI runs formatting, linting, type checks, unit tests, integration tests,
  browser tests, migration checks, dependency/security scans, and image builds.
- [ ] Every milestone has an operator runbook and acceptance record.

---

## Phase 0 — Engineering baseline and governance

### 0.1 Repository hygiene

- [x] Add a root contribution/development guide with prerequisites, local
  commands, test tiers, generated-code policy, and migration policy.
  ([CONTRIBUTING.md](CONTRIBUTING.md))
- [x] Add `.editorconfig`, Go formatting/lint policy, web lint/format policy,
  and pre-commit or CI equivalents. ([.editorconfig](.editorconfig),
  `services/crm-api/.golangci.yml` added, `services/authincore/.golangci.yml`
  fixed — its `issues.exclude-rules` key was silently ignored under
  `version: "2"`, moved to `linters.exclusions.rules` — and
  [.github/workflows/ci.yml](.github/workflows/ci.yml) runs fmt/vet/lint/
  build/test for both services plus `docker compose config --quiet`. Web
  lint/format policy is documented in CONTRIBUTING.md; enforcement is added
  to CI once `apps/web` has source to lint. AuthinCore's lint step runs
  informationally (`continue-on-error`) — see limitation below.)
- [x] Verify every module/package uses an independent project identifier;
  replace accidental CompanyOS source/import/provenance references only after
  determining compatibility impact. `services/crm-api`'s module path
  (`github.com/comanyos/crm` → `github.com/santoshguru/crm`) and NATS/event
  subject convention (`companyos.crm.<entity>.<tenant>` →
  `crm.<entity>.<tenant>`) are renamed and independent; `go build`,
  `golangci-lint run ./...` (0 issues), and `go test ./...` pass in
  `services/crm-api` after the rename. **Still open**:
  `services/authincore/third_party/platform-contract` remains an unlicensed
  vendored CompanyOS snapshot (larger decision — its content, not just its
  name, originates from CompanyOS) — see
  [docs/THIRD_PARTY_NOTICES.md](docs/THIRD_PARTY_NOTICES.md).
- [ ] Inventory all licenses and third-party notices. Resolve the AuthinCore
  licensing provenance before public distribution. Inventory done:
  [docs/THIRD_PARTY_NOTICES.md](docs/THIRD_PARTY_NOTICES.md). At the user's
  direction, the commercial license/tier-gating subsystems in both services
  (`internal/license/` in each, plus AuthinCore's `/api/v1/license` endpoints,
  admin-ui License page, and `migrations/018_license.sql`) have been removed
  — both `go build ./...` and `go build -tags enterprise ./...` pass for
  AuthinCore afterward, and crm-api lints at 0 issues. Provenance is **still
  not fully resolved**: `services/crm-api/LICENSE` is actually the Connect
  project's Apache-2.0 notice (not this project's own license), AuthinCore
  still has no `LICENSE` file for its claimed MIT terms, the
  `platform-contract` snapshot above is still unlicensed, and a newly found
  `services/authincore/migrations/025_dbcore_role_scopes.sql` seeds role/scope
  data for an unrelated "DBCore" product by default — see
  [docs/THIRD_PARTY_NOTICES.md](docs/THIRD_PARTY_NOTICES.md) for all open
  items.
- [x] Add a security reporting policy and supported-version policy.
  ([SECURITY.md](SECURITY.md))

Acceptance criteria:

- A new contributor can clone, configure local development values, build, and
  run all checks from documented commands.
- No runtime/build path refers to the original CompanyOS workspace.

Verification:

- `docker compose config --quiet`
- `make verify`
- Repository search for CompanyOS paths/module replacements/symlinks.

### 0.2 Environments and configuration

- [x] Define `development`, `test`, `staging`, and `production` configuration
  matrices and required environment variables.
  ([docs/CONFIGURATION.md](docs/CONFIGURATION.md))
- [x] Add startup validation that rejects development passwords, missing TLS
  URLs, ephemeral signing keys, and weak CORS in staging/production.
  `services/crm-api/internal/config` and `services/authincore/internal/config`
  (new) run `APP_ENV`-gated checks before any database connection opens;
  `development`/`test` are unaffected (verified live via `make up` — logs
  `"startup","environment":"development"` and boots normally). Unit tests:
  `go test ./internal/config/...` in each service. **Cookies**: AuthinCore
  already computes the `Secure` flag per-request from `requestIsHTTPS`
  (`r.TLS != nil || X-Forwarded-Proto: https`) rather than a static flag, and
  the new `ISSUER` https requirement in staging/production reinforces that a
  TLS-terminating deployment is expected — not separately re-validated here.
  **Related finding, not fixed**: `requestIsHTTPS` trusts `X-Forwarded-Proto`
  from any client, not just a known reverse-proxy hop; that header-spoofing
  gap is a Phase 10 hardening item, not a startup-config one.
- [x] Document secret ownership, rotation, and injection for database
  passwords, AuthinCore signing keys, and `ADMIN_TOKEN`/bootstrap admin
  password. ([docs/CONFIGURATION.md](docs/CONFIGURATION.md)) SMTP, OAuth
  providers, webhooks, and object storage are marked not-yet-applicable —
  blocked on Phase 8 provider decisions, per the "Required external
  decisions" table.
- [x] Create separate disposable integration-test database/Compose profiles.
  `auth-postgres-test` / `crm-postgres-test` behind the `--profile test`
  Compose profile: separate ports, `tmpfs` storage (no named volume), never
  started by `make up`. `make test-db-up` / `make test-db-down`. Verified:
  brought up, confirmed healthy, confirmed crm-api's migrations auto-applied
  (8 tables), torn down cleanly.

Acceptance criteria:

- Production cannot start with local default passwords or an ephemeral signer.
  Verified by `internal/config` unit tests in both services.
- Test tooling never requires or writes production credentials. The test-DB
  profile uses its own fixed local-only passwords, unrelated to any
  staging/production secret.

---

## Phase 1 — Complete authentication, tenancy, RBAC, and FGA

### 1.1 Bootstrap lifecycle

- [x] Add idempotent `authinctl bootstrap` and Compose tools profile.
- [x] Bootstrap a first organization, administrator, `owner` and `crm_admin`
  grants, CRM role model, and public `crm-web` client.
- [x] Add structured bootstrap integration tests against a clean database.
  `services/authincore/cmd/authinctl/bootstrap_integration_test.go` runs
  against a real migrated AuthinCore database (compose `auth-postgres-test`,
  migrations applied by the suite itself from the embedded FS). Every test
  owns a unique organization domain rather than assuming an empty database,
  so none of them only passes when it runs first.
- [x] Test a second bootstrap invocation produces no duplicate org, identity,
  role, resource, permission, project, or OAuth client.
  `TestSecondBootstrapCreatesNoDuplicates` snapshots all nine row counts,
  re-runs bootstrap twice, and compares the whole struct — plus asserts the
  reported ids are stable, the `created_*` flags flip to false, and no
  duplicate `org.created` audit event appears.
- [x] Add safe admin-password reset/recovery process with explicit operator
  confirmation and audit event; never silently reset on normal bootstrap.
  New `authinctl reset-admin-password` (`make reset-admin-password`,
  documented in `docs/BOOTSTRAP.md`). It is a separate command rather than a
  flag on bootstrap for a specific reason: bootstrap is re-run routinely and
  reads `BOOTSTRAP_ADMIN_PASSWORD` from the environment each time, so if it
  also reset the password, an operator whose shell still had the original
  value exported would reset it invisibly and one whose shell did not would
  lock the administrator out — neither failure announces itself.
  `RESET_ADMIN_CONFIRM` must equal the account's own address, not a bare
  "yes", so pasting a runbook command unedited cannot reset the wrong
  account. The command creates nothing, replaces rather than adds a
  credential, writes an `identity.password.reset` audit event naming the
  account but never the password or hash, and leaves roles, grants and the
  OAuth client untouched. `TestBootstrapNeverResetsAnExistingAdministratorPassword`
  proves the "never silently" half against a real database.
- [x] Document organization domain rules, localhost development setup, and
  tenant-domain DNS/TLS prerequisites for staging/production —
  `docs/BOOTSTRAP.md`, linked from the README.

Acceptance criteria:

- A blank AuthinCore database can be initialized using environment-injected
  values only. **Met**, and now proven rather than asserted:
  `TestBootstrapOnACleanDatabaseCreatesTheWholeModel` checks the organization
  is active, the administrator is resolvable the way a login resolves them,
  and both creations left an audit event.
- Bootstrap output contains IDs and client configuration but never a password,
  private key, or client secret. **Met** —
  `TestBootstrapOutputAndClientCarryNoSecrets` scans the encoded result and
  also asserts the `crm-web` client is a public PKCE client
  (`client_secret` empty, `auth_method_type: none`, `application_type: spa`),
  because a browser cannot keep a secret.

Found while doing the above — **needs a product decision:**

`services/authincore/migrations/025_dbcore_role_scopes.sql` installs an
`AFTER INSERT` trigger on `orgs` that seeds three roles for **DBCore** — a
different product, whose source is not in this repository — into every
organization ever created, plus a `dbcore` resource, and grants the `owner`
role `dbcore:*` on it. It also backfills organizations that already exist. So
a customer organization provisioned by this CRM carries "DBCore Admin",
"DBCore Operator" and "DBCore Read-only" roles it has no use for.

Blast radius is bounded and that is now tested: none of those roles carries
any CRM scope, so they grant no access to this product
(`TestOrganizationsCarryRolesForAnUnrelatedProduct`, which also pins the CRM's
own roles to the shared matrix). What they do is pollute any administration UI
that lists roles, and any audit of who can do what.

Removing them means deleting authority rows from organizations that already
exist, and migrations here are append-only — so this is a decision, not a
cleanup. It belongs with the other DBCore provenance items already open in
`docs/THIRD_PARTY_NOTICES.md` and Phase 0.

### 1.2 OIDC end-to-end behavior

- [x] Verify local Authorization Code + PKCE, consent, token exchange, JWT
  validation through JWKS, refresh-token rotation, and revocation.
- [x] Add an automated browser test for login redirect, callback validation,
  CSRF state validation, PKCE verifier, session restoration, and logout.
  `apps/web/tests-e2e/auth.spec.ts` covers login redirect, consent, the live
  dashboard, session restoration across a reload, and logout against the real
  stack. Callback validation, CSRF state and the PKCE verifier are covered as
  unit tests in `packages/auth-client/test/client.test.ts` rather than through
  the browser — driving a forged `state` or a mismatched verifier through a
  real redirect means making the identity provider misbehave, which the
  browser tier cannot arrange.
  New: `apps/web/tests-e2e/session-policy.spec.ts` covers cross-tab sign-out
  across two real documents, that a session ended locally does not come back
  on reload while a live one does, and that no JWT is ever written to browser
  storage (`localStorage` holds nothing at all). 18/18 e2e pass, twice in a
  row, in parallel.
- [x] Define the web token/session model: memory-only access token, refresh
  protocol, refresh failure handling, idle timeout, and multi-tab behavior.
  Written up in `docs/ARCHITECTURE.md` — what is stored and where, what ends a
  session and what each trigger does to the AuthinCore session and the refresh
  token, and why storing a refresh token at all is forced (AuthinCore sends
  `X-Frame-Options: DENY`, which rules out hidden-iframe silent renew).
  Already in place: memory-only access token, refresh token in `sessionStorage`
  rather than `localStorage`, renewal 60s before expiry, concurrent renewals
  de-duplicated because rotation makes the second one lose, and a failed
  renewal treated as "sign in again" with a toast.
  Added here: an **idle timeout** (30 minutes, `apps/web/src/auth/session-policy.ts`)
  and **cross-tab sign-out**. Both end only the local session and revoke this
  tab's refresh token — deliberately not the AuthinCore session, because an
  idle background tab must not sign the user out of the tab they are working
  in. `sessionStorage` is per-tab, so revoking is what stops a reload from
  restoring a session the user was just told had ended.
- [x] Implement front-channel logout and AuthinCore session logout from the
  web app; clear local app state and show a signed-out confirmation. Already
  implemented: `logout` drops the local session, revokes the refresh token,
  calls `DELETE /api/v1/sessions/me` to clear AuthinCore's own session cookie,
  and waits up to 3s for both before showing the signed-out screen — showing
  it earlier would claim something the system had not yet made true, since
  until that call lands a fresh `/authorize` silently re-authenticates the
  same user. Extended here to tell this app's other tabs first.
- [x] Add OIDC negative-path tests: invalid issuer/audience/signature/key ID,
  expired token, incorrect PKCE verifier, reused code, invalid redirect URI,
  missing state, revoked refresh token, and unavailable JWKS. All covered. On the CRM side:
  `internal/auth/tenant_resolution_test.go` (issuer, audience, signature,
  expiry, and a token carrying no `exp` at all) and
  `internal/auth/jwks_rotation_test.go` (unknown key id, a real key id signed
  by another key, unavailable JWKS). On the AuthinCore side, the new
  `internal/protocol/oidc/negative_paths_test.go` covers authorization-code
  replay, redirect-URI substitution, a wrong and a missing PKCE verifier
  (with the correct one asserted to still work, so the first two cannot pass
  for the wrong reason), refresh-token replay, and a refresh token redeemed
  by a different client. All pass — these are regression guards, not fixes.
  **State validation is the web app's job** (it is the CSRF defence for the
  browser redirect, and AuthinCore only echoes the value) and is covered in
  `packages/auth-client/test/client.test.ts`: a state that was never issued,
  an expired pending request, and — added here — a callback carrying no state
  at all, each rejected without touching the network. An earlier revision of
  this section said state validation was uncovered; that was wrong.
- [x] Add key rotation test: CRM validates active and previous AuthinCore keys
  and refreshes its JWKS cache without a restart.
  `TestKeyRotationTakesEffectWithoutARestart` publishes a second key mid-test
  and shows tokens signed by either verify during the overlap window.
- [x] Add readiness checks so CRM does not report healthy until it can retrieve
  or has a valid cached JWKS according to the deployment policy. New
  `GET /readyz` (`503` with no signing keys, `200` with them), kept separate
  from `/healthz`, which stays a pure liveness signal — see
  `docs/ARCHITECTURE.md`. `auth.Authenticator.Ready` does more than read the
  cache: an empty cache triggers a fetch, so an instance whose start-up fetch
  failed recovers on its own rather than sitting out of the load balancer
  until the next scheduled refresh.

Fixed while doing the above:

- **The e2e suite could not run on any machine that moved the web dev server
  off port 3000.** `playwright.config.ts` hardcoded `http://localhost:3000`
  for both `baseURL` and the `webServer` readiness URL, while `vite.config.ts`
  honours `VITE_WEB_PORT` — so `npm run dev` bound one origin and Playwright
  waited on another, and the run never started. `auth.spec.ts` and
  `helpers.ts` each hardcoded the same port in a URL assertion. The origin is
  now derived once, from the same env vite reads, and handed to the tests.
  (Port 3000 is a common collision; this repository's own `.env.example`
  documents moving it.)
- **A tab whose session ended elsewhere hung on "Redirecting to sign in…"
  forever.** `RequireAuth` only calls `login()` for `unauthenticated`, and the
  signed-out confirmation is a route the signing-out tab navigates to itself —
  so a tab ended by the idle timeout or by another tab's sign-out rendered a
  promise of a redirect that was never going to happen. It now goes to
  `/signed-out`, the same confirmation the signing-out tab shows. Found by the
  browser test; the unit tests could not see it, because they assert on
  `status` rather than on what a route renders.
- The cross-tab sign-out broadcast originally opened a fresh `BroadcastChannel`
  per call. A channel does not deliver back to the instance that posted, but it
  does deliver to every *other* instance — including another one in the same
  document — so the signing-out tab received its own broadcast and raced its
  own `logout`, clobbering the "signing-out" state the user was looking at.
  One shared channel per tab fixes it, and the test that caught it is kept.
- A request presenting a token whose key id was not cached could **block a
  server goroutine for up to a minute** before failing. The JWKS library's
  `RateLimitWaitMax` defaults to a minute, and the key id is attacker-chosen,
  so a handful of junk tokens was a cheap way to exhaust the server. Capped at
  one second, and `TestUnknownKeyIdRefreshIsRateLimited` now asserts both that
  the refresh is rate-limited *and* that losing the limiter fails fast rather
  than blocking. (Found because the first version of that test hung.)
- The JWKS background refresh used the library's one-hour default, which made
  the revocation window an hour and meant an instance that failed its start-up
  fetch could stay unready for an hour after the provider recovered. Now 15
  minutes, with the unknown-key-id refresh at 10 seconds — that shorter one is
  what bounds both rotation pickup and readiness recovery.

Documented rather than fixed, because both are consequences of offline
verification and closing them is a §10.1 decision:

- Withdrawing a signing key does not take effect until the next scheduled
  refresh (up to 15 minutes), because a cached key id never triggers the
  out-of-band refresh. `TestRevokedKeyKeepsWorkingUntilTheNextRefresh` pins
  this, and fails loudly if the behaviour ever changes.
- An access token already issued outlives deactivation until it expires
  (§1.3).

Acceptance criteria:

- The web app never receives a client secret.
- Login, renewal, logout, and expected failure states are deterministic and
  user-safe.

### 1.3 Tenant isolation

- [x] Create two test organizations, principals, and OAuth clients in an
  isolated test fixture. Two organizations with their own principals and full
  record sets are seeded by `seedTenant` (`internal/domain/
  pgstore_tenant_isolation_test.go`) and `seedViaAPI` (`internal/api/
  tenant_isolation_test.go`). The OAuth-client half is covered by §1.1's
  `TestTwoOrganizationsBootstrapIndependently`, which provisions two real
  organizations through the real bootstrap path — deliberately sharing an
  administrator email address and a client id, the two values most likely to
  have been given a global unique constraint by accident — and asserts no
  role, resource, permission, project or application is visible from both.
- [x] Prove org B cannot retrieve, update, delete, list, search, or reference
  an org A account/contact/lead/opportunity/activity by ID.
  `TestCrossOrganizationRPCAccessIsDenied` probes all 36 CRM RPCs across the
  boundary; `TestCrossTenantEntityAccessIsDenied` and
  `TestCrossTenantReferenceIsRejected` do the same through the store against
  live PostgreSQL. Both assert the answer is NotFound and never
  PermissionDenied, since refusing a foreign id confirms it exists.
  `TestEveryTenantSensitiveRPCHasAnIsolationProbe` fails when an RPC is added
  without a probe. There is no export surface yet, so nothing to prove there;
  the by-value lookups that would back one (`FindContactByEmail`,
  `FindAccountByDomain`) are covered.
- [x] Prove RLS prevents a deliberately unscoped application query from seeing
  another tenant's rows. `TestEveryTenantTableEnforcesRLS` reads the
  PostgreSQL catalog rather than a hand-written table list, so a table added
  later without ENABLE + FORCE + USING/WITH CHECK fails immediately;
  `TestUnscopedQuerySeesNothingInAnyTenantTable` seeds both tenants and shows
  an unbound connection reads zero rows from all ten tenant tables.
- [x] Prove background workers/outbox queries use the drain role only where
  cross-tenant dispatch is intended and never publish data to a wrong tenant.
  `TestOutboxDrainRoleIsTheOnlyCrossTenantReader` runs the same discovery
  query on both pools: as `crm_app` it finds nothing (the silent
  drains-nothing misconfiguration `config.Validate` rejects), as `crm_drain`
  it finds both tenants — and every read of tenant *data* after that goes
  back through the scoped pool. It also proves `crm_drain` cannot write.
  `internal/dispatch/tenant_isolation_test.go` proves the drain narrows to one
  real scope per tenant, that every published envelope's subject names its own
  tenant, and that one tenant's bus failure neither drops nor reattributes
  another's events.
- [~] Add tenant context to every audit event, metric label strategy, job, and
  integration callback while preventing high-cardinality/sensitive telemetry.
  §10.2 now adds CRM/AuthinCore metrics, bounded operation labels, safe
  request logs, and CRM outbox counters; tenant IDs are deliberately excluded
  from all labels and worker/access logs. Audit-event storage and integration
  callbacks still do not exist, so this remains open for those future surfaces.
- [x] Add automated tests for host-domain tenant resolution, token-org/host
  mismatch, and safe handling of unknown/deactivated organizations, in
  `internal/auth/tenant_resolution_test.go`. This service has no host-domain
  resolver by design, so the tests are written as negative proofs:
  `TestHostAndHeadersCannotChangeTheResolvedTenant` shows `Host`,
  `X-Forwarded-Host`, `Origin`, `X-Org-Id` and `X-Tenant-Id` cannot move a
  request between organizations, and `TestTokenWithoutAnOrganizationYieldsNoTenant`
  shows a token with no `org_id` produces no tenant rather than a shared
  default. An unknown organization gets its own empty tenant, never a
  fallback.

Acceptance criteria:

- The test suite proves tenant isolation at API, application, database, and
  asynchronous-worker layers. **Met** for those four layers; the audit and
  telemetry bullet above remains open on its own dependencies.

Fixed while proving the above:

- `internal/auth/jwks.go` accepted a token carrying no `exp` claim, because
  `WithExpirationRequired` is not the golang-jwt default. Since the CRM does
  not call AuthinCore per request, expiry is the only thing that ends a
  session after an organization or user is deactivated — a token without one
  would have made that window unbounded. Now required, covered by
  `TestDeactivatedOrganizationIsBoundedByTokenExpiry`. AuthinCore always sets
  `exp` (`internal/token/token.go`), so no issued token is affected.
- The default test DSN in `internal/domain/pgstore_test.go` named a port
  belonging to an unrelated local project, where it could only fail to
  connect — and a failed connection there is `t.Skip`. The entire PostgreSQL
  tier, row-level security included, silently did not run for anyone who had
  not set `TEST_DATABASE_URL`. It now defaults to this repository's own
  `make test-db-up` instance.

Residual limitation, disclosed rather than hidden: an access token already
issued stays valid until it expires even after the organization or user is
deactivated, because the CRM verifies tokens offline against JWKS and does not
call AuthinCore per request. The exposure window is exactly the access-token
lifetime, and ending a session sooner would need either token introspection or
a revocation list — a §10.1 decision, not a §1.3 one.

### 1.4 RBAC and record-level authorization

- [~] Finalize role matrix with product owner: `crm_admin`, `crm_manager`,
  `crm_member`, `crm_viewer`, and any required custom roles. The four roles
  are now specified with a written rationale each in
  `packages/contracts/rbac/crm-roles.json`, which both services pin
  themselves to. Two things keep this partial: no product owner has signed
  off on the matrix, and custom (organization-defined) roles are not
  supported — AuthinCore can hold them, but the CRM has no way to describe
  what a custom role may do.
- [x] Define a permission catalog for every current RPC/action; forbid public
  RPCs by default. `internal/server/scopes.go` carries the catalog, and
  `auth.ScopePolicy` now **denies any procedure it has no entry for**. The
  previous `map[string]string` failed open: a procedure absent from the map
  was enforced as public, so adding a handler and forgetting its entry
  silently published it to every authenticated principal in every
  organization. `assertPolicyIsTotal` walks the generated service descriptors
  and fails server construction — not just the test suite — on a procedure
  with no decision, a procedure decided twice, or an entry naming a procedure
  that no longer exists. ("Planned" actions are deliberately not catalogued:
  an entry for an RPC that does not exist is exactly the dead entry the
  totality check rejects.)
- [x] Add tests that each role has exactly its intended scopes and that
  changes take effect after the expected token/session refresh boundary.
  `internal/server/role_matrix_test.go` asserts behaviour rather than
  restating the file: for each role it computes which procedures the real
  policy would let it reach, and compares that against separately written
  intent. `TestRoleChangesTakeEffectOnlyAtTheNextToken` pins the session
  boundary — authority travels in the token and this service verifies
  offline, so a revoked role keeps working until the client presents a newly
  minted token. `TestViewerHoldsNoWriteAuthority` and
  `TestOnlyTheAdminRoleUsesAWildcard` state the two properties that would
  otherwise be inferred from a list.
- [ ] Design AuthinCore FGA relations for account/contact/lead/opportunity
  owner, editor, viewer, team, and organization access.
- [ ] Implement FGA check calls in CRM before record reads/writes where a
  tenant-wide scope alone is insufficient.
- [ ] Add FGA tuple lifecycle rules for creation, ownership transfer, deletion,
  user deactivation, and tenant removal.
  **The three FGA bullets are blocked on a product decision, not on
  engineering.** Record-level authorization means answering "may this user
  edit an account they do not own, and who can grant that" — a sharing policy
  the matrix above cannot express, because a scope in a token describes a
  *type* of record, never an instance. Implementing relations before the
  policy exists would bake in a guess that later has to be migrated out of
  every tuple. The needed input is already listed under "Required external
  decisions": *Final role matrix and record-sharing policy*.
- [x] Surface permission-denied states in the API and web UI without leaking
  record existence across tenants. `TestScopeDenialIsNotAnExistenceOracle`
  proves the scope check runs before the handler, so the refusal is byte-identical
  whether or not the id names a real record — a caller lacking the scope cannot
  use the error to enumerate ids. Cross-tenant reads answer NotFound rather than
  PermissionDenied (§1.3). On the web, `ForbiddenState`
  (`apps/web/src/components/ui/StatePanel.tsx`) renders the denial without
  naming the record.

Acceptance criteria:

- Every CRM RPC has explicit required scope(s). **Met**, and enforced: the
  server refuses to start otherwise.
- Record sharing and ownership cannot bypass tenant RLS or tenant-level RBAC.
  **Not yet applicable** — there is no record sharing to bypass anything with
  until the FGA bullets above are unblocked. Tenant RLS and tenant-level RBAC
  are themselves proven (§1.3 and this section).

Fixed while doing the above:

- `crm_member` and `crm_viewer` were granted no `crm:pipeline:read`, but
  `apps/web/src/features/opportunities/OpportunityFormPage.tsx` and
  `OpportunityBoardPage.tsx` call `ListPipelines`/`ListPipelineStages`
  unconditionally. A member therefore could not create or edit an opportunity
  and a viewer could not open the board — both got a permission error on a
  screen their role is meant to include. Pipeline definitions are reference
  data an opportunity cannot be rendered without (stage names, ordering,
  close-reason rules), so both roles now read them; writing pipelines stays
  with manager and admin.

  **Operator action required:** an organization bootstrapped before this
  change still has the old scope set. Re-run `make bootstrap` against it —
  `ensureCRMRoleModel` upserts permissions, so the new scope is applied to the
  existing roles without touching identities or credentials.

Testing note: `internal/server/policy_enforcement_test.go` runs against a real
HTTP server, real RSA-signed tokens verified through a real JWKS endpoint, the
real interceptor and the real handlers. The data-level tests above would all
still pass if the interceptor were registered on one handler and not the
other; this is the file that would not.

---

## Phase 2 — Build the CRM web application foundation

### 2.1 Application setup

- [x] Choose and document the web framework, router, data-fetching/cache
  library, form validation, test runner, browser test runner, and component
  primitives. React 18 + TypeScript (strict) + Vite; React Router; TanStack
  Query; React Hook Form + Zod; Vitest + Testing Library; Playwright;
  hand-built Tailwind-based primitives (no external component library).
  Documented in [apps/web/README.md](apps/web/README.md).
- [x] Initialize `apps/web` with strict TypeScript, environment validation,
  production build, dev server on `http://localhost:3000`, and test scripts.
  `src/env.ts` (zod) validates `VITE_*` at load, enforcing `https://` in a
  production build; `vite.config.ts` sets `strictPort: true` since the OIDC
  redirect_uri and both backends' CORS allowlists are pinned to `:3000`.
  Verified: `npm run build`, `npm run test`, `npm run lint` all pass.
- [x] Typed Connect client package: `packages/contracts`, generated via
  `@bufbuild/protoc-gen-es` v2 (`buf generate`) from `services/crm-api/proto`
  — no hand-duplicated request/response shapes in UI code.
- [x] CORS configured (no dev proxy — see apps/web/README.md for why):
  added `CORSOrigins`/`CORS_ALLOWED_ORIGINS` + production-safety validation
  to `services/crm-api` (previously had none at all), reused AuthinCore's
  existing support. Production API/issuer URLs are explicit env vars,
  validated `https://` in a production build.
- [x] Error boundary (`ErrorBoundary.tsx`), route-level loading boundary
  (`RequireAuth`'s loading state), not-found screen (`NotFoundPage`), and
  unavailable-service state (`UnavailableState`, shown on
  `Code.Unavailable`/`DeadlineExceeded` from a live RPC) all implemented and
  exercised by `DashboardPage`.

### 2.2 Design system and shell

- [x] Design tokens (Tailwind theme: brand/danger/success/warning scales) and
  reusable primitives: `Button`, `Input`, `Table`, `Dialog`/`ConfirmDialog`
  (native `<dialog>`), `Badge`, `Skeleton`, `Pagination`,
  `EmptyState`/`ErrorState`/`ForbiddenState`/`UnavailableState`.
- [x] App shell: sidebar navigation, user menu, page headers with
  breadcrumbs, a command palette (`⌘K` — navigation only until Phase 6 adds
  live search), and a global toast system (ARIA `status`/`alert` live
  regions). **Not yet responsive**: the shell is desktop-only today — no
  mobile/tablet layout or sidebar collapse — so "responsive app shell" is
  only partially delivered; still open.
- [ ] Accessible data table and pagination primitives exist and are used
  live (Dashboard); filters, detail panels, and composed form layouts do not
  exist yet — there is no real data/entity screen to filter or edit until
  Phase 3. Confirmation dialogs and status chips (`Badge`) are built but
  have no live caller yet either.
- [ ] Visual regression / screenshot coverage — not implemented.

### 2.3 Web authentication boundary

- [x] Login route (`LoginPage` + `beginLogin`) creates `state` and a PKCE
  verifier, stored in `sessionStorage` with a 10-minute expiry
  (`packages/auth-client/src/pending.ts`). No `nonce`/ID-token handling —
  the client only consumes the access token, so the parenthetical does not
  apply.
- [x] Callback route (`CallbackPage` + `completeCallback`) validates `state`
  against the stored pending request (rejecting forged/replayed/expired
  callbacks), exchanges the code, and renders a "Sign-in failed" screen for
  a denied or malformed callback — verified live, including the denied path.
- [x] `RequireAuth` route guard + `AuthProvider` (principal via decoded JWT
  claims, `hasScope`/`hasRole`).
- [x] Token renewal (~60s before `exp`, via `grant_type=refresh_token` —
  see below), a toast warning + forced sign-out on renewal failure, and
  logout (revokes the refresh token, ends AuthinCore's session, clears all
  local state) — all verified live.
- [ ] `hasScope`/`hasRole` exist but nothing calls them yet to hide
  navigation/actions — there are no permission-differentiated actions until
  Phase 3 has real screens. Not yet done.

**Session/renewal strategy differs from a typical SPA and from what was
originally attempted**: AuthinCore's `/oauth2/authorize` (and `/login`) send
`X-Frame-Options: DENY` — correct clickjacking hardening on an identity
provider — which makes the common hidden-iframe "silent renew" pattern
impossible. Renewal instead uses the standard `grant_type=refresh_token`
exchange, with the (rotating, single-use) refresh token held in
`sessionStorage` — never `localStorage`, and the access token stays
in-memory only. See `packages/auth-client/README` references above and
`apps/web/README.md`'s "Session model" section.

**Backend bugs found and fixed while building and live-testing this flow**
(none of these existed until this work — building AND testing against the
real backend, not mocks, is what surfaced them):

1. AuthinCore's `/oauth2/consent` had no user-facing UI at all — `GET` just
   returned raw JSON with a comment saying a frontend was expected to render
   it, but `hosted-ui` had no route for it. No public OAuth client (any
   client without a stored secret) could ever complete login. Fixed:
   content-negotiated `services/authincore/internal/api/http/server.go`
   (`consentScreenOrData`) serves the SPA shell for a real browser
   navigation and JSON for the SPA's own fetch; added a real Allow/Deny
   `ConsentPage` to `hosted-ui`.
2. `SubmitConsent` only accepted a JSON body, but completing consent ends in
   a same-origin redirect and then a cross-origin redirect to the client's
   `redirect_uri` — only a real browser navigation (a plain `<form
   method="POST">`) follows both hops correctly; a JS-driven `fetch()`
   cannot safely continue across that origin change. Fixed: `SubmitConsent`
   now also accepts `application/x-www-form-urlencoded`.
3. `BrowserCSRF` rejected genuine same-origin `<form>` POST navigations: per
   the Fetch spec, `Origin` is sent as the literal string `"null"` for such
   requests once `Referrer-Policy: no-referrer` is in effect (set globally
   by `SecurityHeaders`), which the Origin-matching check could never
   satisfy. Fixed: trust `Sec-Fetch-Site` (Fetch Metadata) as the primary,
   browser-guaranteed same-origin signal, falling back to Origin-matching
   only when a browser omits Fetch Metadata entirely. Regression tests
   added in `internal/api/middleware/csrf_test.go`.
4. (apps/web-side, not AuthinCore) React 18 StrictMode's intentional
   double-invocation of effects raced two concurrent
   `renewFromStoredRefreshToken` calls against AuthinCore's single-use
   rotating refresh token — the second call presented a token the first had
   already consumed and got rejected. Fixed with an in-flight-promise guard
   in `packages/auth-client/src/client.ts`; this is a real robustness fix,
   not a dev-only workaround, since nothing guarantees exactly one caller in
   production either.

All four are covered by new unit tests (`csrf_test.go`;
`packages/auth-client/test/client.test.ts`'s de-dupe test) and were verified
against the live Compose stack — see `apps/web/tests-e2e/auth.spec.ts`,
which passed end-to-end (sign in with the bootstrap administrator, consent,
land on the live dashboard, reload while the session is valid, sign out, and
confirm the session actually ended) — run via
`npm run test:e2e --workspace=@crm/web`.

Acceptance criteria:

- [x] A user can sign in at localhost, land on a protected CRM page, reload
  while the session is valid, log out, and receive clear errors for failed
  auth. Verified live and by the automated `auth.spec.ts` Playwright test
  (2/2 passing) — see above.
- [ ] Lighthouse/accessibility baseline and keyboard tests — **not
  verified**. `eslint-plugin-jsx-a11y` runs in CI as static analysis and the
  shell uses semantic roles/ARIA landmarks and focus-visible states
  throughout, but no Lighthouse audit or automated axe/keyboard-navigation
  test has actually been run. Do not treat this as done.

---

## Phase 3 — Accounts and contacts

### 3.1 Backend completion

- [x] Review existing account/contact contracts and migrations for validation,
  field limits, nullability, identifier semantics, optimistic concurrency, and
  audit requirements. Added field-limit validation (name/industry/tier/website/
  email/phone/title lengths, non-negative revenue, RFC-5321-bounded/shape-checked
  email) and optimistic concurrency (`UpdatedAt` mismatch → `ErrConflict` →
  `connect.CodeAborted`) to both `MemStore` and `PgStore`
  ([types.go](services/crm-api/internal/domain/types.go),
  [memstore.go](services/crm-api/internal/domain/memstore.go),
  [pgstore.go](services/crm-api/internal/domain/pgstore.go)).
- [x] Add server-side filters, stable cursor pagination, sort allowlists, and
  query indexes for account/contact lists. New `AccountFilter`/`ContactFilter`
  and validated `AccountSort`/`ContactSort` allowlists
  ([query.go](services/crm-api/internal/domain/query.go)), keyset pagination
  (SQL row-value comparison in Postgres, `sort.Search`-based boundary reusing
  the sort comparator in MemStore), and a new migration adding `website_domain`
  (indexed, write-time-maintained mirror for `FindAccountByDomain`) plus
  name/tier/owner/email/account_id indexes
  ([0010_account_contact_query_indexes.sql](services/crm-api/migrations/0010_account_contact_query_indexes.sql)).
  Wired through new `ListAccountsRequest`/`ListContactsRequest` proto fields
  (`name_contains`, `tier`, `owner_id`, `sort`, `account_id`, `email_contains`).
- [x] Add account hierarchy traversal safeguards and tested ownership history.
  (Pre-existing: `account_hierarchy.go`, `account_hierarchy_test.go`,
  `pgstore_account_hierarchy_test.go` — verified still passing.)
- [x] Add contact/account referential behavior for deletion/reassignment.
  (Pre-existing: `requireContactAccount` rejects a dangling/cross-tenant
  `account_id`; verified via new RPC-layer test
  `TestCreateContact_RejectsDanglingAccountID`.)
- [x] Add duplicate detection review flow; never silently merge records.
  (Pre-existing: `applyContactDuplicateFlag` sets, never blocks or merges,
  `DuplicateOfContactID`; verified via new RPC-layer test
  `TestCreateContact_FlagsDuplicateEmailWithoutBlocking`.)
- [x] Add database-backed integration tests for all CRUD, RLS, invalid foreign
  keys, duplicate detection, pagination, and authorization paths. Added
  `internal/domain/query_test.go` (MemStore: filters, sort, keyset pagination,
  optimistic concurrency, field-limit validation),
  `internal/domain/pgstore_query_test.go` (same, against live Postgres — filter
  SQL, keyset SQL, indexed `FindAccountByDomain`, DB-level optimistic
  concurrency), and `internal/api/crm_test.go` (new — the RPC layer had zero
  test files before: auth/tenant-scope checks, CRUD round-trips, tenant
  isolation, filter/sort/pagination, invalid-sort rejection, optimistic
  concurrency conflict, dangling-FK rejection, duplicate-email flagging,
  tenant-scoped summary counts). All passing: `go test ./...` (unit),
  `TEST_DATABASE_URL=... go test ./internal/domain/... -run TestPgStore` (live
  Postgres via `make test-db-up`), `go vet`, `gofmt -l .`, and
  `golangci-lint run ./...` (0 issues) all clean.

### 3.2 Web workflows

- [x] Accounts list with search/filter/sort/pagination and clear empty states
  (`apps/web/src/features/accounts/AccountsListPage.tsx`). Name search and
  tier filter are debounced and pushed to the server; the sort dropdown
  offers exactly `domain.ParseAccountSort`'s allowlist; Previous/Next work
  off a visited-token stack, since keyset pagination yields only a forward
  cursor. "No accounts yet" and "No matching accounts" are distinct states.
  Verified live against seeded data: 25 rows page 20/5 correctly, tier +
  sort compose, and the id-descending tie-break is visible on identical
  `created_at` values.
- [x] Account detail with hierarchy, ownership, contacts, activity timeline,
  and permission-aware actions
  (`apps/web/src/features/accounts/AccountDetailPage.tsx`). Child accounts
  come from the server-side `parent_id` filter added in 3.1, not a
  client-side scan. Edit/Delete are gated on `crm:account:write`, delete is
  confirmed, and a duplicate-website account is flagged with a link — never
  merged. Verified live with a seeded parent/child/activity fixture.
  **Related opportunities/leads is deliberately not built here**:
  `ListOpportunitiesRequest`/`ListLeadsRequest` have no `account_id` filter
  yet, and adding filter/sort/pagination to those is Phase 4's own scope.
  Building it now would mean a client-side full-tenant scan.
- [x] Account create/edit forms with validation and unsaved-change protection
  (`apps/web/src/features/accounts/AccountFormPage.tsx`,
  `src/lib/useUnsavedChangesGuard.ts`). Zod validation mirrors
  `internal/domain/types.go`'s limits; `updated_at` is round-tripped so a
  concurrent write is rejected rather than silently clobbering. `parent_id`
  and `duplicate_of_account_id` are preserved rather than cleared (there is
  no parent-assignment UI yet — viewing only).
- [x] Contacts list/detail/create/edit with account selection and duplicate
  warning (`apps/web/src/features/contacts/`, `src/components/AccountPicker.tsx`).
  The account picker is a live-search combobox with no free-text option,
  since `account_id` must name a real Account. A duplicate email warns after
  save and never blocks or merges, matching backend behaviour. The
  duplicate *resolve* flow is not built — resolution semantics (merge, link,
  dismiss) are undecided and merging is explicitly out of scope here.
- [x] Component and browser tests for create, validation error, edit, access
  denial, and tenant-safe not-found behaviour. Component tier: 44 tests
  across `AccountsListPage/AccountDetailPage/AccountFormPage` and the three
  contact equivalents, mocking the Connect clients and rendering on a memory
  **data** router (required — the forms use `useBlocker`). Browser tier:
  `apps/web/tests-e2e/accounts.spec.ts` and `contacts.spec.ts` — 12 journeys
  against the live stack, covering CRUD, server-side search, validation, the
  unsaved-changes prompt, the stale-edit conflict across two independent
  sessions, both duplicate flags, and not-found.

Verification: `npm run typecheck` and `npm run build` clean, `npm run lint`
0 errors (4 pre-existing warnings in untouched files), Prettier clean,
`vitest run` 66/66, and `playwright test` 14/14 against the live Compose
stack across five consecutive runs.

Three real defects were found and fixed while verifying this:

1. **Saving prompted to discard the changes it had just saved.** The forms
   navigate to the detail page in the mutation's `onSuccess` while the form
   is still dirty, so `useBlocker` intercepted the success navigation. Fixed
   by giving the guard an explicit `disarm()`; regression-tested (removing
   the fix fails two tests).
2. **Two account-detail sections swallowed errors.** Child accounts and
   Activity rendered "No child accounts." / "No activity recorded." when
   their query had actually *failed* — asserting something the app did not
   know. Both now render the shared error state.
3. **The browser suite's sign-in was racy and rate-limited.** A non-retrying
   `isVisible()` check skipped the password-tab click whenever the login
   page had not painted yet, and logging in once per test tripped
   AuthinCore's per-IP login rate limit (`rl:login:*` in Redis — brute-force
   protection working as intended) so whole runs failed at sign-in. The
   suite now signs in once in a setup project and reuses that session.

4. **The browser suite cancelled its own writes.** This is what kept the
   suite pinned to one worker, and it was never contention between tests.
   After clicking Save, the specs asserted `getByRole("heading", { name })`
   — which Playwright matches as a *substring*, so it resolved instantly
   against the form page's own `Edit <name>` header instead of waiting for
   the detail page. The test walked on while `UpdateAccount` was still in
   flight, and its next `page.goto` cancelled the request. The duplicate-
   website case made this visible: account A never got its website, so
   account B had nothing to match and came back correctly unflagged. Traced
   under `--fully-parallel` (`POST UpdateAccount -> -1`, aborted 4ms after
   the heading assertion resolved) and confirmed in the database: A's
   `updated_at` still equalled its `created_at`. `deleteFromDetailPage` had
   the same shape and was losing cleanup deletes, which is why fixtures
   accumulated. Serial runs simply won the race every time. Every post-save
   assertion now waits for the detail page (`expectOnDetailPage` in
   `tests-e2e/helpers.ts`), deletes wait for the list navigation, and the
   sign-out spec waits for its two best-effort server calls. The suite is
   fully parallel again, verified across 24 consecutive runs.

   Follow-up on the sign-out half: waiting in the spec was papering over the
   app. `AuthProvider.logout` fired `POST /oauth2/revoke` and `DELETE
   /api/v1/sessions/me` without awaiting them and flipped to the signed-out
   state synchronously, so a navigation or tab close in that window left
   AuthinCore holding a live session that silently re-authorized the next
   `/authorize` — the flake was a real security defect, not a test-timing
   quirk. Sign-out now holds on "Signing out…" until both calls settle
   (capped by `SIGN_OUT_GRACE_MS`), and both fetches carry `keepalive` so an
   unload cannot cancel them; the spec's `waitForResponse` scaffolding is
   gone.

   The backend was ruled out first, not assumed innocent:
   `TestPgStoreAccountDuplicateFlagUnderConcurrentWrites` runs the same
   create/update/create sequence concurrently in one tenant against live
   Postgres, with background churn on a shared domain, and never misses the
   match. `applyAccountDuplicateFlag`'s read and the `website_domain` write
   do sit in separate transactions, but the writer has committed before the
   next request begins, so READ COMMITTED always sees it.

Acceptance criteria:

- All screens use CRM API data and retain correct states during loading, empty,
  error, retry, unauthorized, and permission-denied conditions. **Met**, with
  one caveat: permission-denied is proven at the component tier (a principal
  without `crm:account:write`/`crm:contact:write` sees no write actions, and a
  denied RPC renders the forbidden state) but not live — that needs a
  second, lower-scoped test principal, which does not exist yet.

---

## Phase 4 — Leads, opportunities, configurable pipelines, and forecasting

### 4.1 Leads

- [x] Verify existing lead lifecycle and conversion against Postgres/RLS with
  retries, transactions, duplicate contacts, and cross-tenant references.
  `internal/domain/pgstore_revenue_workflow_test.go` walks the whole status
  machine against live Postgres (default status, every legal step, both
  terminal branches, and the four ways a caller can try to cheat it —
  skipping, moving backward, creating into a terminal status, and setting
  `converted` through `UpdateLead` instead of `ConvertLead`), and proves each
  refusal writes nothing. Conversion is proven **atomic** by engineering a
  failure at its last step — a default pipeline with no open stage, so the
  conversion Opportunity is refused after the Contact and Account inserts have
  already run — and showing that neither survives, and that the lead is still
  `qualified` with no conversion links. `TestRevenueWorkflowsUnderRowLevelSecurity`
  re-runs the load-bearing half as `crm_app` (the role RLS binds, not the
  table owner) with two organizations: every cross-organization read and
  mutation, `ConvertLead` included, answers NotFound, and a deal cannot be
  pointed at another tenant's account.
- [ ] Define lead fields, assignment, source, score, disqualification reasons,
  conversion options, and audit history.
- [x] Build live lead list, detail, create/edit, lifecycle progression,
  disqualification, duplicate-contact warning, and conversion confirmation UX
  in `apps/web/src/features/leads`. This uses the generated Connect client;
  workflow coverage is assigned to the QA workstream.
- [x] Ensure conversion outcomes are idempotent and linked records are visible.
  `TestPgStoreConcurrentLeadConversionConvertsExactlyOnce` runs four
  simultaneous conversions of one lead — a double-clicked button, or a client
  retrying a request whose answer it never saw — and asserts all four return
  the same three ids and the tenant holds exactly one contact, one account and
  one opportunity. `PgStore` gets that from `SELECT ... FOR UPDATE` on the
  lead; `MemStore` had no equivalent and duplicated everything, which
  `revenue_store_parity_test.go` now catches and a mutex over its compound
  write paths now prevents. `TestPgStoreLeadConversionLinksAreReal` checks
  linkage rather than non-emptiness: each id resolves, in this tenant, and
  points back — the contact is attached to the linked account, the
  opportunity is against it, and it opened on the tenant's default pipeline at
  its first open stage, exactly like a directly created deal.

### 4.2 Pipelines and opportunities

- [~] Add tenant-configurable pipeline and stage models with stage ordering,
  probability, terminal type, required fields, and deletion/archive rules.
  Migration `0012_pipeline_definitions.sql` now provides the tenant-scoped,
  RLS-protected data model. Typed pipeline/stage create/list/update endpoints
  and the live `/settings/pipelines` administration screen are implemented.
  Administrators can edit pipeline/stage settings, atomically choose one
  default pipeline, and archive a pipeline without deleting its history.

  Individual stage archival is now implemented, behind the guard it needed.

  **Archiving used to be a data trap.** Nothing checked whether a pipeline or
  stage still held deals, and `activePipelineStage` resolves an Opportunity's
  current stage before any other rule and refuses an archived one — so every
  deal left behind became permanently unwritable: it could not advance, could
  not be closed, and could not even be renamed. It stayed readable and kept
  its amount, so it went on counting toward anything summed later, and the
  only exit was SQL. Pipeline archival was reachable from a button on
  `/settings/pipelines` whose confirmation promised the records were merely
  retained for reporting.

  Both paths now refuse with `ErrConflict` while **open** deals stand on the
  thing being archived, naming the deals and the step that unblocks it
  (`pipeline_archival.go`, enforced in both stores). Only open deals block: a
  closed deal cannot be moved off, since every transition out of a terminal
  stage is refused, so counting it would make any stage that has ever closed a
  deal permanently unarchivable and the remedy impossible to follow.
  `/settings/pipelines` now offers per-stage archival and shows the server's
  refusal verbatim — `refusalMessage`, not the generic "reload the page" line,
  which would send an administrator round a loop that never ends.

  Still open here: **moving deals between pipelines**. There is no such
  operation, and `prepareOpportunityStage` refuses one
  ("an opportunity cannot move to another pipeline"), so a pipeline holding
  open deals can only be emptied by closing them. That is the remaining half
  of "deletion/archive rules".

  The residual that came with the guard is now fixed too. Archiving a closing
  stage is permitted precisely because closed deals do not block it — and
  every deal standing on it then became unwritable: the close reason could
  not be corrected and the deal could not be renamed, while it stayed
  readable and kept its amount. `activePipelineStage` is the right check for
  a *destination* and the wrong one for where a deal already is, so
  `currentPipelineStage` now resolves the latter and archival stops arrivals
  without freezing the history already there. The state machine is untouched:
  an archived stage is still not a destination, and an archived terminal
  stage still cannot be reopened. Both boundaries have tests.

- [x] Migrate existing opportunity stage behavior to pipeline/stage IDs without
  data loss and retain immutable stage history. Migration `0013` binds prior
  records to a compatible, tenant-local Sales pipeline; new opportunities use
  a default or explicitly selected configured pipeline and retain the existing
  append-only stage-history rows.
- [~] Add opportunity validation: account ownership, amount/currency, expected
  close date, loss/win reason, stage transitions, and concurrency handling.
  Stage transitions, the terminal-stage close fields and concurrency are now
  verified against Postgres and over real HTTP
  (`pgstore_revenue_workflow_test.go`, `internal/server/revenue_workflow_pg_test.go`):
  a deal walks the configured pipeline one stage at a time, skipping and
  unknown stages are refused without writing a history entry, closing requires
  a reason and refuses a client-supplied close date, both terminal stages are
  terminal in every direction, only a won transition publishes
  `opportunity.closed_won`, and the history forms an unbroken chain ending
  where the deal actually is.

  Three defects fixed here. `UpdateOpportunity` had **no optimistic-concurrency
  check** even though `OpportunityFormPage` had been sending `updated_at` all
  along, so a second editor silently overwrote the first. It also ran its
  read, write, history append and outbox enqueue as **separate transactions
  with no lock**, so concurrent advances each decided their transition from
  the same stale stage and each appended an entry leaving it — the audit log
  branched. And it acquired a **second pool connection inside its own
  transaction** to read custom-field definitions, which deadlocks the pool
  once there are as many concurrent writers as connections (`ConvertLead` had
  this one already). It is now one RLS-bound transaction, row-locked, reading
  everything through `txReader`. Each fix has a test that fails without it;
  see `docs/ARCHITECTURE.md` "Write concurrency".

  Still open: amount/currency (there is no currency field yet) and an
  explicit expected-close-date model distinct from the stamped `close_date`.
- [x] Build live opportunity list, detail, create/edit/delete, account
  association, and permitted stage-change UX in
  `apps/web/src/features/opportunities`. The current UI uses the generated
  Connect client, respects `crm:opportunity:*` permissions, uses live
  pipeline/stage IDs, and displays stage-specific close-reason requirements.
- [x] Build Kanban/pipeline and immutable stage-history views after the
  tenant-configurable pipeline model is available. A live, tenant-scoped
  `/opportunities/board` is implemented with pipeline selection, active-stage
  columns, and loading/empty/error/permission-aware states. Each opportunity
  detail screen now renders its append-only stage history through a typed,
  tenant-scoped endpoint.

  Drag/drop transitions are now validated. `transitions.ts` mirrors the
  server's four transition rules (`checkConfiguredStageTransition`) so an
  ineligible column greys out during the drag and an illegal drop is refused
  without a request; the server still decides every move, so a divergence
  between the two shows up as a rejected drop rather than an illegal write.
  Dragging is only the pointer affordance — a drop and a card's **Move** menu
  both call one `moveTo`, which is what gives keyboard and touch users the
  same operation, since HTML5 drag events reach neither. Dropping onto a
  terminal stage opens the close-reason dialog the stage requires; the write
  omits `close_date` so the server stamps it. Moves round-trip `updated_at`,
  so a board left open while someone else edits the deal reports a conflict
  instead of overwriting them, and `custom_fields`, which `UpdateOpportunity`
  replaces rather than merges.

  A card renders in its destination column while its write is in flight and
  returns if the write fails, so the board feels immediate without the query
  cache being edited behind the server's back.

  `OpportunityFormPage` now takes its stage choices from the same
  `legalTargets` the board uses, rather than a second copy of the rules. Its
  copy was stricter than the server in one place — a closing stage was offered
  only from the last open stage, so a deal could not be marked lost at
  Prospecting without first being walked through three stages it never
  reached. The board allowed that move all along, so the two screens
  disagreed about the same record.
- [ ] Define quota, forecast category, weighted pipeline, and forecast snapshot
  calculations; test them with deterministic dates/currency rules.

Acceptance criteria:

- An administrator can configure a pipeline safely; users see only permitted
  stages/actions; forecast calculations are explainable and reproducible.

---

## Phase 5 — Activities, tasks, notes, attachments, and timeline

### 5.1 Core activity model

- [ ] Validate current activity timeline behavior with PostgreSQL/RLS and
  pagination where timeline volume can be unbounded.
- [~] Define activity kinds, immutable fields, correction policy, mentions,
  participants, due dates, reminders, completion, and recurrence rules.
  Defined and implemented: Activity stays immutable and a correction is a new
  Activity; Task carries due dates, priority, assignee and a reversible
  completion; Note is editable with `edited_at` recording that it was.
  **Not implemented, and each for a stated reason:** *reminders* and
  *recurrence* need a scheduler and a per-user timezone policy that does not
  exist yet — they belong with §9 workflow automation, not here, and a
  half-built reminder that silently does not fire is worse than none.
  *Mentions* and *participants* need a user directory the CRM does not have
  (AuthinCore owns identities and the CRM currently sees only opaque subject
  ids), so they wait on the same user-administration work as §1.4's UI.
- [x] Add first-class task and note entities rather than overloading generic
  activity data where reporting/permissions need distinct models.
  `internal/domain/task.go`, `note.go`, `migrations/0014_tasks_and_notes.sql`,
  both stores, 12 RPCs, and their own `crm:task:*` / `crm:note:*` scopes.

  The separation is not taxonomy. An Activity is immutable *because* it
  records what happened, and that is what makes it worth having for audit. A
  Task is assigned, rescheduled and eventually completed; a Note is corrected
  as understanding improves. Sharing the table would force either making
  Activity mutable — destroying its value — or storing tasks that can never be
  completed. Scopes are separate for the matching reason: a role that may read
  the immutable activity log is not thereby a role that may reassign somebody's
  work.

  Rules worth knowing, each pinned by a test that runs against **both** stores:
  - Completion is reversible, unlike an Opportunity's terminal stages — a task
    that turns out not to be finished is reopened, not replaced, because
    replacing it loses what was originally asked and when.
  - `CompleteTask`/`ReopenTask` are separate RPCs from `UpdateTask`: ticking a
    checkbox should not be a read-modify-write of the whole record, which
    under optimistic concurrency either fails against a concurrent edit or
    clobbers one. Both are idempotent, so a double-tap cannot rewrite who
    finished the work.
  - `completed_at`/`completed_by` and `Note.author_id` are server-owned. A
    client that could assert them could backdate work or put words in someone
    else's mouth.
  - A Task's subject is optional ("call the vendor back" attaches to nothing);
    a Note's is required. Half a subject is rejected by both the domain and a
    CHECK constraint — it is a dangling reference no query can resolve.
  - An undated task is never overdue. The default order puts undated tasks
    last rather than treating a zero time as "long ago", and the SQL, the
    in-memory sort and the keyset cursor all encode that same rule.

  Two defects found while building the UI, both pre-existing:
  - `components/ui/Dialog.tsx` hardcoded `id="dialog-title"`, so on any screen
    with more than one dialog mounted — any list offering both "add" and
    "edit" — every dialog took the *first* one's accessible name via
    `aria-labelledby`. A screen reader announced "Add note" while the heading
    read "Edit note". Now a per-instance `useId`.
  - The account page's own **Edit**/**Delete** and a note's were both labelled
    just that, which is ambiguous to assistive technology and broke the
    existing `deleteFromDetailPage` e2e helper the moment a notes panel
    appeared on the page. The note actions are now "Edit note"/"Delete note".
- [~] Build a unified chronological timeline for account/contact/lead/
  opportunity with filtering and lazy loading. A shared API-backed timeline
  now renders on every record detail page and lets authorized users append
  immutable calls, emails and meetings. Every record page also carries an
  **Open tasks** panel and a **Notes** panel, ordered outstanding work →
  what is known → what happened. "Note" was removed from the activity kinds
  at the same time: with notes first-class, offering both would give one word
  two meanings on one screen — an immutable log line and an editable note.
  Activities already recorded with kind `note` still render.
  Cross-record aggregation, timeline filters, lazy loading and a dedicated
  activity workspace remain pending.

  Web surface added with this: `/tasks` (a work list opening on **My open
  tasks**, with Overdue and All views, and completion from the list),
  `/tasks/new` and `/tasks/:id/edit`. Completion from any surface goes through
  `CompleteTask`/`ReopenTask` rather than a whole-record update, so ticking a
  box cannot lose a concurrent edit. Overdue styling is derived client-side
  from the same rule the server filters by — an undated or completed task is
  never overdue — so the badge and the Overdue view cannot disagree.

### 5.2 Attachments

- [ ] Obtain product-owner decision on object storage provider, geography,
  retention, max sizes/types, malware scanning, download authorization, and
  deletion/legal-hold policy.
- [ ] Implement private object storage, pre-signed upload/download flows,
  metadata records, scanning state, quotas, and cleanup jobs.
- [ ] Prove objects cannot be read with a URL after tenant/user authorization
  is lost.

Acceptance criteria:

- Tasks/reminders are reliable across time zones; attachment access is fully
  tenant- and record-authorized; all actions appear on the timeline/audit log.

---

## Phase 6 — Search, filters, saved views, bulk operations, and data quality

- [ ] Define search technology and privacy model (Postgres FTS initially or a
  dedicated index later). Search indexes must be tenant-filtered.
- [ ] Implement global and entity search with ranking, highlights, permissions,
  debounce/cancellation, pagination, and no cross-tenant leakage.
- [ ] Implement reusable structured filters, sort controls, column selection,
  saved views, sharing rules, and defaults per entity.
- [ ] Implement bulk select/action APIs with authorization per affected record,
  bounded batches, idempotency keys, progress/failure reporting, and audit.
- [ ] Add tags, custom fields, validation rules, and controlled schema changes.
- [ ] Implement duplicate review/merge with field-by-field resolution,
  relationship migration, immutable merge audit, undo policy, and conflict
  constraints.

Acceptance criteria:

- Search and bulk actions remain fast and correct for large tenant datasets;
  unauthorized records never affect counts, results, or suggested actions.

---

## Phase 7 — Imports, exports, dashboards, reporting, and administration

### 7.1 Import/export

- [ ] Define supported formats, field maps, limits, encoding, validation,
  duplicate policy, error report format, dry-run behavior, rollback policy,
  and data retention.
- [ ] Implement asynchronous import jobs with uploaded-file scanning, tenant
  scope, field mapping UI, preview, per-row errors, and audit events.
- [ ] Implement asynchronous exports with authorization snapshot, generated
  file expiry, watermark/audit options, and secure download.

### 7.2 Dashboards and reports

- [ ] Define canonical metrics and date/currency/time-zone semantics.
- [ ] Implement dashboard/query APIs with bounded filters and tenant-aware
  aggregation; never aggregate through bypass-RLS connections without an
  explicit tenant filter.
- [ ] Build personal/team dashboards, pipeline reports, activity reports,
  conversion funnels, win/loss, forecast, and exportable reports.
- [ ] Add reporting permission model and cache invalidation/refresh policy.

### 7.3 Administration

- [ ] Build organization settings, user/role administration, pipeline/stage
  administration, custom fields, tags, imports/exports, integrations,
  workflows, audit log, and security/session views.
- [ ] Ensure all administration operations require explicit permissions and
  produce audit entries.

Acceptance criteria:

- Imports are previewable and reversible within the documented policy;
  exports/reports/dashboards are tenant-safe and permission-aware.

---

## Phase 8 — Email, calendar, and communication integrations

Blocked until the product owner supplies provider choices, OAuth applications,
redirect URLs, privacy/retention policy, and deployment secret ownership.

- [ ] Define provider abstraction, connection status model, scope minimization,
  OAuth state/PKCE, encrypted credential storage, refresh/rotation, revocation,
  reauthorization, and audit events.
- [ ] Implement email connection, sync cursor, deduplication, threading,
  contact/account matching, consent visibility, and timeline rendering.
- [ ] Implement calendar connection, incremental sync, event linking,
  attendee matching, time-zone handling, conflict policy, and timeline
  rendering.
- [ ] Add provider webhooks with signature validation, replay protection,
  idempotency, dead-letter/retry handling, and tenant routing.
- [ ] Add clear user/admin controls for disconnect, purge, and sync health.

Acceptance criteria:

- Provider credentials are encrypted, never logged or sent to the browser,
  and deleted/revoked when a connection is removed. Sync is idempotent.

---

## Phase 9 — Workflow automation and notifications

- [ ] Define event catalog, trigger conditions, filter language, action types,
  approval states, scheduling, concurrency, retries, rate limits, and loop
  prevention.
- [ ] Implement tenant-isolated workflow definitions, versioning, dry runs,
  execution logs, failure handling, pause/resume, and audit history.
- [ ] Implement notifications (in-app first; email/SMS only after provider
  decisions) with preferences, read state, batching, and accessibility.
- [ ] Implement assignment rules, escalation, due-date reminders, approvals,
  and signed outbound webhooks.
- [ ] Add reliability tests for duplicate events, failed actions, retries,
  long-running jobs, cross-tenant routing, and webhook replay attacks.

Acceptance criteria:

- Automations are explainable, auditable, bounded, idempotent, tenant-safe,
  and cannot trigger unbounded loops.

---

## Phase 10 — Security, observability, backups, deployment, and release

### 10.1 Security hardening

- [ ] Threat-model browser auth, multi-tenancy, FGA, imports, exports,
  attachments, integrations, webhooks, and administrative operations.
- [ ] Perform dependency/SBOM scans, secret scans, SAST, container scans, and
  periodic dynamic testing; remediate findings by severity policy.
- [ ] Enforce production TLS/HSTS, secure cookies, CSP, CORS allowlists, CSRF,
  rate limits, request-body limits, safe error handling, and security headers.
- [ ] Implement account lockout/MFA policy and administrator session controls.
- [ ] Establish data retention, deletion, privacy request, audit retention,
  incident response, and access review procedures.

### 10.2 Observability and reliability

- [~] Add service metrics for HTTP/RPC, DB pools/query latency, NATS/outbox,
  AuthinCore/OIDC, jobs, integrations, and user-visible errors. CRM and
  AuthinCore now export Prometheus HTTP/Connect request counters and latency,
  pool statistics, CRM outbox success/retry/failure, JWT/OIDC validation
  failures, NATS connection state, and bounded database query latency.
  Integration-specific metrics remain open because those integrations do not
  yet exist. See `docs/OBSERVABILITY.md`.
- [~] Add distributed tracing and request/correlation IDs across web, CRM,
  AuthinCore, workers, and webhooks. Safe request IDs and W3C `traceparent`
  now enter CRM and AuthinCore HTTP/Connect ingress; optional OTLP export is
  wired for both services. The CRM web client creates opaque trace context and
  the implemented CRM path persists/restores it through outbox delivery to
  NATS. AuthinCore outbound flows plus future webhooks/integrations still need
  explicit propagation when those features exist, so the cross-product item
  remains open.
- [~] Add structured logs with secret/PII redaction and retention controls.
  CRM and AuthinCore use allowlist-based JSON access logs that omit headers,
  bodies, query values, cookies, tokens, passwords, redirect locations, and
  PII. Retention, shipping, and access controls remain deployment work.
- [ ] Define SLOs, alert thresholds, dashboards, on-call ownership, runbooks,
  and synthetic checks for login and core CRM workflows.
- [ ] Run load, soak, failure, restart, key rotation, database failover, and
  dependency-outage tests.

### 10.3 Backups and deployment

- [ ] Select production platform, regions, DNS/TLS ownership, container
  registry, database service, cache/NATS service, object storage, SMTP, and
  secret manager/KMS.
- [ ] Create immutable deployment manifests/IaC, environment-specific config,
  least-privilege service accounts, network policies, and deployment pipeline.
- [~] Define migration forward/rollback/expand-contract process and test it on
  realistic data volumes. The forward-only expand/contract and application
  rollback policy is now documented in `docs/BACKUP_RESTORE.md`; realistic
  volume/upgrade validation is still open.
- [~] Implement encrypted backups, point-in-time recovery as needed, retention,
  cross-region policy, restoration runbook, and documented RPO/RTO. Encrypted
  local Compose logical backup/restore tools and a destructive-restore runbook
  now exist (`scripts/backup-compose.sh`, `scripts/restore-compose.sh`,
  `docs/BACKUP_RESTORE.md`). Provider-managed PITR, retention, cross-region
  policy, and approved RPO/RTO remain unselected and therefore open.
- [ ] Perform at least one restore drill and one production-like upgrade drill.

### 10.4 Release gate

- [ ] All prior phase acceptance criteria are satisfied and recorded.
- [ ] CI is green from a clean checkout; release images are reproducible and
  signed/attested as required by deployment policy.
- [ ] Security, privacy, license, accessibility, performance, and operations
  reviews are signed off by their owners.
- [ ] Production secrets, provider credentials, domains, support process, and
  incident contacts are in place.
- [ ] Release notes, administrator guide, end-user help, API reference,
  deployment guide, backup/restore runbook, and rollback plan are published.
- [ ] Staging smoke tests and production canary/rollback criteria pass.

Acceptance criteria:

- A production deployment can be created, monitored, upgraded, backed up, and
  restored by an operator who did not write the application.

---

## Required external decisions and credentials

Implementation must pause only for these product-defining or externally owned
inputs. All other work should continue autonomously.

| Needed input | Needed by | Owner |
| --- | --- | --- |
| Production domain/DNS/TLS and hosting platform | Phase 10 | Product/deployment owner |
| Production secret manager/KMS and database/backup ownership | Phase 10 | Infrastructure owner |
| SMTP provider and sender domain | Email notifications/recovery | Product owner |
| Email/calendar provider choice and OAuth app credentials | Phase 8 | Integration owner |
| Object-storage provider, region, retention, scanning policy | Attachments | Product/security owner |
| Privacy, retention, deletion, legal hold, export policy | Phases 5, 7, 10 | Product/legal owner |
| Final role matrix and record-sharing policy | Phase 1.4 | Product owner |
| Currency, fiscal calendar, quota, forecast rules | Phase 4/7 | Sales operations owner |
| Branding assets, product name, support/contact process | Web/release | Product owner |

## Progress update rule

After each delivered increment:

1. Run proportionate unit, integration, browser, migration, authorization, and
   build checks.
2. Record exact commands/results and any environment limitation.
3. Update this file and `docs/ROADMAP.md` with completed criteria only.
4. Link user-facing documentation and runbooks.
5. Do not increase completion percentage merely because a screen, migration,
   endpoint, or test file exists.
