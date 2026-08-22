# Standalone CRM — Competitive Parity Plan (task_v2)

## Purpose

`task.md` answers "is the CRM we scoped finished?". This document answers a
different question: **"what stands between this codebase and the products a
buyer will actually compare it against?"** — Salesforce, HubSpot, Zoho CRM,
Microsoft Dynamics 365, and SAP.

It is additive. `task.md` remains the execution checklist for the originally
scoped product; `docs/ROADMAP.md` remains the stakeholder view. This file is
the engineering source of truth for **parity work**, and it is deliberately
opinionated about what parity does and does not mean.

The same evidence rule applies: a checkbox may be marked complete only after
its stated acceptance criteria and verification have passed. Presence of a
migration, screen, endpoint, or test file is not completion.

## Verified baseline (2026-08-22)

Measured from the working tree, not estimated:

| Measure | Value |
| --- | --- |
| `services/crm-api` Go (non-test) | 20,113 lines |
| `services/crm-api` Go tests | 9,666 lines |
| `services/authincore` Go (non-test) | 20,067 lines |
| `apps/web` + `packages/*` TS/TSX | 7,353 lines |
| CRM tables | 12 |
| CRM RPCs implemented | 47 (48 procedures under total scope policy) |
| Web routes with live CRUD | accounts, contacts, leads, opportunities, tasks, pipelines |

What is genuinely strong today and must not regress while parity work
proceeds:

- Tenant isolation enforced in PostgreSQL by row-level security against a
  non-`BYPASSRLS` `crm_app` role, proven at four layers.
- Deny-by-default authorization: `assertPolicyIsTotal` refuses to start a
  server whose scope policy does not decide every served procedure.
- A transactional outbox with W3C trace context persisted across the
  asynchronous hand-off to NATS.
- An observability posture with bounded metric labels and allowlist-based
  logging.
- A first-party identity service (OIDC, SAML, SCIM, FGA, MFA) rather than a
  dependency on someone else's.

## Strategic position — read before planning any phase

Full feature parity with all five competitors is not a goal, and pursuing it
would be a mistake. Those products differ from this one in kind, not only in
size:

- **Salesforce / Dynamics / SAP** are *metadata-driven platforms*. Their
  defining property is that an administrator changes the data model at
  runtime. This repository compiles its schema; adding a field is a migration
  plus a deploy.
- **SAP** competes on ERP adjacency — CRM as one node in an order-to-cash and
  finance graph.
- **HubSpot** competes on communications and marketing depth.
- **Zoho** competes on breadth at low price.

This product's defensible position is the one none of the five can occupy:
**a self-hostable, security-first, multi-tenant CRM with verifiable data
ownership.** Every phase below is ordered to strengthen that position first
and close generic feature gaps second.

Accordingly, parity is defined in three tiers:

- **Tier 1 — Must reach.** Without these the product is not credible against
  *any* of the five. Phases 1–6.
- **Tier 2 — Must reach to compete with HubSpot/Zoho** on their own ground.
  Phases 7–9.
- **Tier 3 — Deliberately declined or deferred.** Documented at the end, so
  the omission is a decision on record rather than an oversight.

## Product definition of parity-done

Parity is reached when all of the following are true:

1. An administrator can extend the data model — new fields on shipped objects
   and new objects — without a code change, a migration, or a deploy, and
   tenant isolation still holds for everything they create.
2. A business user can automate a multi-step process (trigger, condition,
   action, approval, escalation) without engineering involvement.
3. A sales manager can build, save, share, schedule, and export a report and
   a dashboard over live tenant-scoped data.
4. A representative can send and receive email and schedule meetings inside
   the product, with the full thread on the record timeline.
5. A third party can build an integration against a documented, versioned,
   rate-limited public API with signed webhooks and OAuth application
   credentials, in a sandbox, without our involvement.
6. Record-level and field-level permissions are enforced server-side, proven
   by tests, for owners, teams, hierarchies, and explicit shares.
7. The product runs in a chosen production environment with rehearsed
   backup/restore, monitoring, alerting, on-call ownership, and a release
   checklist.
8. Data-subject obligations (export, deletion, retention, legal hold, audit
   trail) are implementable by an operator without engineering escalation.

---

# Phase 0 — Foundation debt (blocking)

Nothing below may start until this phase is green. These are audited,
reproduced defects in work already marked complete; building parity features
on top of them multiplies the cost of each one.

- [ ] **0.1 — Repair CI.** Two of four jobs fail on a clean checkout.
  - `authincore` job: `//go:embed all:dist` in `hosted-ui/embed.go` cannot
    resolve because `**/dist/` is gitignored and no build step precedes
    `go build ./cmd/...`. Add a hosted-UI build step to the job.
  - `compose-config` job: `docker compose config --quiet` exits 1 because
    `CRM_WEB_AUTH_ORG_ID` uses required-variable syntax and `.env.example`
    ships it empty. Provide a CI-only placeholder env or relax the
    interpolation for validation.
  - Acceptance: a fresh clone with no local state passes every CI job.

- [ ] **0.2 — Fix the untrusted metric label.** `internal/api/http/server.go`
  passes a request-body-supplied `method` string into
  `metrics.LoginTotal.WithLabelValues(...)` on the failure path. Unknown
  methods reach it, so an attacker chooses Prometheus label values.
  Validate against the registered strategy set before incrementing; emit
  `"unknown"` otherwise.
  - Acceptance: a test asserts an unregistered method produces the
    `"unknown"` label and creates no new series.

- [ ] **0.3 — Repair the backup and restore scripts.** Both use `-U crm`
  against a database whose roles are `postgres`, `crm_app`, and `crm_drain`;
  neither has ever run successfully against the CRM database.
  - Correct the role.
  - Decrypt and verify each archive *before* `DROP SCHEMA public CASCADE`,
    not after — a wrong age identity currently destroys the database with
    nothing to restore.
  - Reconsider `--no-privileges`: it strips the `crm_app`/`crm_drain` grants
    that `0003_rls_enforce.sql` creates, leaving a restored database the
    application cannot read.
  - Acceptance: a scripted round trip — seed, back up, drop, restore — passes
    in CI against the `--profile test` databases, and the application serves
    traffic afterwards without a manual grant.

- [ ] **0.4 — Close the remaining redaction violations.** `org_id` is logged
  in `internal/webhook/dispatcher.go`; the `Host` header is logged in
  `internal/tenancy/tenancy.go`. Both contradict the stated policy.

- [ ] **0.5 — Gate AuthinCore `/metrics` on an administrator.**
  `requireAdminAuth` falls back to `Authn.ResolveHTTP`, which accepts any
  valid session — any end user in any organization can scrape it. Require an
  administrative permission, use a constant-time comparison for
  `ADMIN_TOKEN`, and make the token mandatory in staging/production the way
  `CRM_METRICS_TOKEN` already is for the CRM.

- [ ] **0.6 — Restore `ResponseController` support.** `loggingMW`'s
  `statusWriter` has no `Unwrap`, which breaks flush, hijack, and deadline
  control through the chain (reproduced: `Flush ... feature not supported`).
  Latent while every RPC is unary; a hard blocker for Phase 7 streaming.

- [ ] **0.7 — Correct stale documentation.** `task.md` claims "no CRM entity
  can yet be viewed, created, or edited through the web app" and a 20% total;
  five entities have full CRUD. Comments in `event_outbox.go`, `events.go`,
  and `cmd/crm/main.go` claim nothing enqueues events; `opportunity_events.go`
  does. `docs/ROADMAP.md`'s M3 acceptance line contradicts its own body.

**Phase 0 acceptance:** CI green from a clean clone; a rehearsed
backup/restore round trip; no untrusted value reaching a metric label; no
tenant identifier or header value in any log line; documentation matching the
code.

---

# Phase 1 — The metadata layer (the decisive gap)

This is the single largest architectural distance to Salesforce, Dynamics,
and SAP, and the hardest to retrofit. It reshapes the schema, the API
contract, the query layer, and the UI simultaneously. **Do it before the
feature phases, not after** — every object shipped in the meantime is another
migration to reconcile.

- [ ] **1.1 — Custom fields on shipped objects.** Typed, tenant-scoped field
  definitions (text, number, currency, date, datetime, boolean, picklist,
  multi-picklist, lookup, formula, rollup) with per-tenant storage.
  - Decide the storage strategy explicitly and record the reasoning: `JSONB`
    column with GIN indexing, sparse typed columns, or an EAV side table.
    `JSONB` is the recommended default — it preserves the existing RLS model
    unchanged, which sparse columns and EAV both complicate.
  - Every custom value must live on a row already carrying `tenant_id` and
    already covered by RLS. A custom field must never open a cross-tenant
    read path.
  - Acceptance: two organizations define a field of the same name and
    different type; neither can observe the other's definition or values;
    proven at the RPC, store, and database layers as §1.3 of `task.md` does.

- [ ] **1.2 — Custom objects.** Tenant-defined objects with relationships to
  shipped objects, participating in search, reports, automation, permissions,
  audit, and the public API on the same footing as built-in objects.
  - Acceptance: a custom object is created, related to `accounts`, populated,
    reported on, automated against, and permission-checked — with no deploy.

- [ ] **1.3 — Validation rules and formula evaluation.** A sandboxed,
  non-Turing-complete expression evaluator with a bounded execution budget.
  It parses tenant-authored input and must be treated as a security boundary:
  fuzz it, cap recursion, cap evaluation time, and forbid I/O.
  - Acceptance: a fuzz suite finds no panic, no unbounded loop, and no
    cross-tenant read; a malicious formula cannot exhaust a server goroutine.

- [ ] **1.4 — Page layouts and field-level security.** Per-role visibility and
  editability enforced **server-side** first, then reflected in the UI.
  A hidden field must be absent from the API response, not merely hidden by
  CSS.
  - Acceptance: a lower-privileged principal receives a response with the
    restricted field absent, proven live rather than only at the component
    tier.

- [ ] **1.5 — Contract strategy for a dynamic schema.** The generated
  Connect/protobuf contract assumes a fixed schema. Decide and document how
  custom fields cross the wire (`google.protobuf.Struct`, a typed
  `map<string, Value>`, or a parallel dynamic endpoint) without abandoning
  type safety for shipped fields.

**Phase 1 acceptance:** an administrator adds a field and an object through
the UI; both appear in search, reports, automations, permissions, audit, and
the public API; tenant isolation is re-proven at all four layers afterwards.

---

# Phase 2 — Record-level and organizational permissions

Today authorization is scope-based (`crm:*`) and tenant-based. Every
competitor enforces *who inside the tenant* may see *which record*. AuthinCore
already ships an FGA engine — this phase wires it to CRM records rather than
building a second system.

- [ ] **2.1 — Ownership.** An owner on every record, transferable, with
  history. `account_ownership_history` exists as a precedent to follow.
- [ ] **2.2 — Role hierarchy.** Managers see subordinates' records by
  configuration, not by hard-coded rule.
- [ ] **2.3 — Teams and explicit shares.** Per-record grants with an
  expiry and an audit entry.
- [ ] **2.4 — Territories.** Assignment by geography, segment, or product
  line, with rule-driven reassignment.
- [ ] **2.5 — Sharing-rule evaluation in the query path.** Predicates must be
  applied in SQL, never by filtering a fetched result set — a filtered-in-Go
  approach leaks through counts, pagination, and aggregates.
  - Acceptance: list, count, search, report, and export all return identical
    record sets for a restricted principal; no endpoint discloses the
    existence of an unshared record through a total, a page token, or an
    aggregate.

**Phase 2 acceptance:** two users in one organization with different
ownership, hierarchy, team, and territory positions provably see different
records through every read path, including reports and exports.

---

# Phase 3 — Workflow and automation engine

Table stakes for all five competitors. Business users must automate without
engineering.

- [ ] **3.1 — Trigger model.** Record created/updated/deleted, field changed,
  time-based, inbound webhook, manual invocation.
- [ ] **3.2 — Condition builder.** Reusing the Phase 1.3 evaluator; one
  expression language across validation, automation, and reports.
- [ ] **3.3 — Action library.** Field update, record create, task create,
  email send, webhook call, notification, assignment, stage change.
- [ ] **3.4 — Approvals.** Multi-step, delegation, recall, timeout escalation,
  full decision history.
- [ ] **3.5 — Scheduled and recurring jobs.** Tenant-scoped, with a durable
  queue. Build on the existing outbox pattern rather than a parallel
  mechanism.
- [ ] **3.6 — Assignment and escalation rules.** Round-robin, load-balanced,
  and territory-based, with SLA timers.
- [ ] **3.7 — Execution safety.** Loop detection, per-tenant execution
  quotas, an idempotency key per action, retry with backoff, a dead-letter
  queue, and a per-run audit trail. An automation that silently loops is
  worse than no automation.
  - Acceptance: a deliberately recursive rule is detected and halted; a
    failing action lands in the dead-letter queue with its context intact and
    can be replayed exactly once.

**Phase 3 acceptance:** a business user builds a lead-assignment and
escalation process end to end in the UI; it runs, is observable per-run, is
tenant-isolated, and cannot exhaust shared capacity.

---

# Phase 4 — Reporting, dashboards, and analytics

Currently one `GetSummary` RPC. This is the most visible day-one gap in an
evaluation.

- [ ] **4.1 — Query layer.** Tenant-scoped aggregation with grouping,
  filtering, joins across shipped and custom objects, honouring Phase 2
  sharing rules.
- [ ] **4.2 — Report builder.** Tabular, summary, matrix, and joined reports;
  saved, named, shared, and permission-aware.
- [ ] **4.3 — Dashboards.** Composable widgets, per-role visibility,
  scheduled refresh.
- [ ] **4.4 — Charting.** Server-computed aggregates; the browser renders,
  it does not aggregate.
- [ ] **4.5 — Scheduled delivery.** Report subscriptions by email on a
  schedule.
- [ ] **4.6 — Forecasting.** Quotas, fiscal calendar, weighted pipeline,
  forecast categories, and historical trend. `forecast.go` is a starting
  point, not an implementation.
- [ ] **4.7 — Performance discipline.** Every report is bounded: a row cap,
  a statement timeout, and a cost estimate before execution. Long-running
  reports run asynchronously and are delivered, never held open on a request.
  - Acceptance: a report over a million-row tenant returns within the stated
    budget or degrades to an async job; it can never hold a connection open
    indefinitely or read across a tenant boundary.

**Phase 4 acceptance:** a manager builds, saves, shares, schedules, and
exports a report and a dashboard over live data with sharing rules applied.

---

# Phase 5 — Data productivity

- [ ] **5.1 — Global search.** Cross-object, ranked, permission-aware,
  incremental. Decide PostgreSQL full-text versus an external index and
  record the reasoning; an external index becomes a second place tenant
  isolation must be proven.
- [ ] **5.2 — Saved views, filters, and tags.** Personal and shared.
- [ ] **5.3 — Bulk operations.** Update, delete, assign, and transfer — atomic
  or reporting per-record failure, never partially silent.
- [ ] **5.4 — Duplicate detection and merge.** Configurable matching rules,
  merge with full field-level provenance and an undo window.
- [ ] **5.5 — Import.** CSV and XLSX with mapping, dry-run validation, an
  error report, and a resumable job.
- [ ] **5.6 — Export.** Tenant-scoped, permission-aware, audited, rate-limited,
  and asynchronous for large sets.
- [ ] **5.7 — Attachments.** Object storage with per-tenant prefixes,
  signed time-bounded URLs, virus scanning, and a retention policy. Requires
  the storage-ownership decision below.

**Phase 5 acceptance:** a 100k-row import dry-runs, reports errors, imports
resumably, is searchable, deduplicated, bulk-editable, and exportable — with
every step audited and tenant-scoped.

---

# Phase 6 — Public API, extensibility, and ecosystem

Without this the product cannot be integrated, and no partner can build on
it. This is what converts a CRM application into a platform.

- [ ] **6.1 — Versioned public API.** A documented, stable REST surface
  alongside Connect, with OpenAPI generation and an explicit deprecation
  policy.
- [ ] **6.2 — API authentication for machines.** OAuth client credentials and
  scoped, revocable, rotatable API keys. AuthinCore's service-account support
  is the foundation.
- [ ] **6.3 — Rate limiting and quotas.** Per-tenant and per-credential, with
  `429` plus `Retry-After` and headers exposing remaining budget.
- [ ] **6.4 — Outbound webhooks.** HMAC signing, timestamped replay
  protection, retries with backoff, a dead-letter queue, tenant-scoped
  routing, and a delivery log the customer can inspect and replay.
- [ ] **6.5 — Event subscriptions.** Expose the existing outbox as a customer-
  facing event stream. Today it emits exactly one event type
  (`opportunity.closed_won`) with no consumer; extend coverage to every
  significant state transition.
- [ ] **6.6 — Sandbox environments.** A per-tenant non-production copy for
  integration development. This is a common procurement requirement and a
  frequent reason a self-hosted product is rejected.
- [ ] **6.7 — Developer experience.** Reference client libraries, a CLI, and a
  documentation site with runnable examples.

**Phase 6 acceptance:** a third-party developer builds and ships an
integration against a sandbox using only public documentation, with signed
webhooks and rate-limit handling, without contacting the team.

---

# Phase 7 — Communications (HubSpot parity)

The widest functional gap against the most similar competitor.

- [ ] **7.1 — Email connection framework.** Gmail and Microsoft 365 OAuth,
  with credentials encrypted at rest under a KMS-held key and never reaching
  browser storage or logs.
- [ ] **7.2 — Bidirectional sync.** Idempotent, cursor-based, resumable, with
  per-connection health surfaced to the user.
- [ ] **7.3 — Calendar integration.** Availability, meeting scheduling, and a
  booking link.
- [ ] **7.4 — Timeline unification.** Email, calls, meetings, notes, and
  automation events on one record timeline. The `ActivityTimeline` component
  and the `activities` table are the seam.
- [ ] **7.5 — Templates and sequences.** Merge fields, multi-step cadences,
  and enrolment rules.
- [ ] **7.6 — Engagement tracking.** Open and click tracking with an explicit
  per-tenant consent setting and a documented privacy posture. Ship it off by
  default; tracking pixels are a regulatory exposure, not a free feature.
- [ ] **7.7 — Shared inbox and conversation routing.**
- [ ] **7.8 — Telephony hooks.** Click-to-dial, call logging, recording
  metadata, with the recording-consent policy documented per jurisdiction.

**Phase 7 acceptance:** a representative sends, receives, schedules, and
enrols a contact in a sequence entirely inside the product, with the complete
thread on the timeline and provider credentials never leaving the server.

---

# Phase 8 — Intelligence layer

All five competitors now ship AI assistance. Absence is increasingly read as
a product gap in evaluations.

- [ ] **8.1 — Predictive lead and opportunity scoring**, with an explanation
  of each score. An unexplained score is not actionable.
- [ ] **8.2 — Summarization** of records, threads, and timelines.
- [ ] **8.3 — Drafting assistance** for email and notes, always
  human-approved before send.
- [ ] **8.4 — Data hygiene suggestions** — duplicates, staleness, missing
  fields.
- [ ] **8.5 — Deployment posture.** Because self-hosting is this product's
  differentiator, AI must be optional and pluggable: a bring-your-own-key
  provider, a self-hosted model option, and a documented no-AI configuration
  that loses no non-AI functionality. Tenant data must never be sent to a
  third-party model without an explicit, per-tenant, opt-in setting.
  - Acceptance: with AI disabled, every non-AI test still passes; with AI
    enabled, an audit entry records what left the tenant boundary and where
    it went.

---

# Phase 9 — Enterprise readiness

- [ ] **9.1 — Audit trail.** Immutable, queryable, exportable, covering data
  changes, permission changes, logins, exports, and automation runs.
  AuthinCore's `internal/audit` and `eventstore` are the precedent; the CRM
  has no equivalent.
- [ ] **9.2 — Data governance.** Retention policies, legal hold, defensible
  deletion, and GDPR/CCPA subject access and erasure — operable by an
  administrator without engineering escalation.
- [ ] **9.3 — Internationalization.** UI translation, locale formatting,
  time zones, multi-currency with dated exchange rates, and a fiscal
  calendar. Multi-currency is invasive and should land before, not after,
  Phase 4 forecasting.
- [ ] **9.4 — Accessibility.** WCAG 2.2 AA, audited, not merely linted. The
  `jsx-a11y` plugin is a floor, not a certification.
- [ ] **9.5 — Responsive and mobile.** The app shell is documented as
  desktop-only. Responsive web first; a native application is a separate
  decision.
- [ ] **9.6 — Compliance evidence.** SOC 2 and ISO 27001 control mapping,
  penetration-test cadence, and a subprocessor register. Self-hosted
  customers still ask for these.

---

# Phase 10 — Production operations

Carried forward from `task.md` Phase 10 and `docs/ROADMAP.md` M6, unchanged
in substance and still the gate on everything customer-facing.

- [ ] **10.1** Cloud provider, regions, managed services, DNS/TLS, registry,
  object storage, KMS/secrets manager.
- [ ] **10.2** Infrastructure as code, deployment manifests, CI/CD,
  least-privilege service accounts, network policy.
- [ ] **10.3** Managed backups and PITR, retention, cross-region policy,
  measured RPO/RTO, and rehearsed restore and upgrade drills.
- [ ] **10.4** Metrics collection, log shipping, tracing collector,
  dashboards, alerts, SLOs, runbooks, on-call ownership.
- [ ] **10.5** Rate limits, security-header and CORS review, dependency and
  container scanning, release checklist.
- [ ] **10.6** Reliability drills: load, soak, restart, dependency outage,
  key rotation, database failover.
- [ ] **10.7** Resolve license provenance before any external distribution —
  still open in `docs/ROADMAP.md` M0 and `docs/THIRD_PARTY_NOTICES.md`.

---

# Tier 3 — Deliberately declined or deferred

Recorded so each omission is a decision, not an oversight. Revisit only with
a named owner and a customer reason.

| Not doing | Why |
| --- | --- |
| Full ERP suite (SAP adjacency) | Different product category. Integrate through Phase 6 events instead of rebuilding. |
| App marketplace with billing and revenue share | Requires ecosystem scale that does not exist yet. Phase 6 makes it possible later. |
| A proprietary programming language (Apex-equivalent) | Phase 1.3's sandboxed expression evaluator covers the real use cases at a fraction of the security surface. |
| Marketing automation suite (campaigns, landing pages, ads) | Adjacent product. Phase 7 covers sales communications only. |
| CPQ, field service, commerce clouds | Separate products in every competitor too. |
| Native mobile applications | Responsive web first (9.5); revisit on customer demand. |
| Multi-region active-active | Premature before a single production region exists. |

---

# Sequencing and dependencies

```
Phase 0 (blocking) ──> Phase 1 (metadata) ──> Phase 2 (permissions)
                                │                     │
                                ├──> Phase 3 (automation)
                                ├──> Phase 4 (reporting) <── 9.3 multi-currency
                                └──> Phase 5 (data productivity)
                                              │
                              Phase 6 (public API) ──> Phase 7 (communications)
                                              │
                                       Phase 8 (AI)
                                              │
                    Phase 9 (enterprise) ──> Phase 10 (production ops)
```

Three ordering constraints are non-negotiable:

1. **Phase 1 precedes Phases 3–6.** Automation, reports, search, and the
   public API each need to address custom fields. Building them against a
   fixed schema means rewriting all four.
2. **Phase 2 precedes Phase 4.** A report that ignores sharing rules is a
   data breach, not a missing feature.
3. **9.3 multi-currency precedes 4.6 forecasting.** Retrofitting currency
   into computed pipeline values is materially harder than designing for it.

# Effort framing

Deliberately expressed as relative weight, not dates — this repository has no
staffing model, and a schedule invented here would be fiction.

| Phase | Relative weight | Risk |
| --- | --- | --- |
| 0 — Foundation debt | XS | Low |
| 1 — Metadata layer | XL | **High** — architectural, hard to reverse |
| 2 — Record permissions | L | **High** — a mistake is a data breach |
| 3 — Automation | L | Medium |
| 4 — Reporting | L | Medium — performance risk |
| 5 — Data productivity | M | Low |
| 6 — Public API | M | Medium — compatibility commitment |
| 7 — Communications | L | Medium — third-party dependency |
| 8 — Intelligence | M | Low — optional by design |
| 9 — Enterprise readiness | L | Medium |
| 10 — Production operations | L | Medium |

Phases 1 and 2 together carry most of the architectural risk in this plan.
They deserve a written design document and a review before implementation
starts, on the same standard as `docs/ARCHITECTURE.md`.

# Decisions required from owners

Additional to those already listed in `task.md`. Engineering must not invent
these.

| Decision | Blocks | Owner |
| --- | --- | --- |
| Custom-field storage strategy (`JSONB` / sparse columns / EAV) | 1.1 | Architecture owner |
| Dynamic-schema wire contract | 1.5 | Architecture owner |
| Sharing model semantics (hierarchy, teams, territories) | Phase 2 | Product owner |
| Search backend (PostgreSQL FTS vs external index) | 5.1 | Architecture owner |
| Object-storage provider, region, retention, scanning | 5.7 | Product/security owner |
| Public API surface and deprecation policy | 6.1 | Product owner |
| Sandbox isolation model | 6.6 | Architecture owner |
| Email/calendar providers and OAuth applications | Phase 7 | Integration owner |
| Engagement-tracking privacy posture | 7.6 | Product/legal owner |
| AI provider posture and data-boundary policy | Phase 8 | Product/security owner |
| Supported locales, currencies, fiscal calendar | 9.3 | Product owner |
| Compliance targets and audit cadence | 9.6 | Security owner |

# Progress update rule

Unchanged from `task.md`, and it applies to this file with one addition:

1. Run proportionate unit, integration, browser, migration, authorization,
   and build checks.
2. Record exact commands, results, and any environment limitation.
3. Update this file and `docs/ROADMAP.md` with completed criteria only.
4. Link user-facing documentation and runbooks.
5. Do not increase completion merely because a screen, migration, endpoint,
   or test file exists.
6. **After any phase touching the data model, permissions, search, reporting,
   export, or automation, re-run the two-organization isolation suite
   (`task.md` §1.3) at all four layers.** Every phase in this plan adds a new
   path by which tenant data could escape. Isolation is proven per change,
   never assumed from a previous proof.
