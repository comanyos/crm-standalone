# Savvy CRM product blueprint

**Internal reference — not customer-facing copy**
**Snapshot:** 10 September 2026

This document separates the complete product we intend to sell from the work that is still required to deliver it. The public website should describe the coherent end product. Status, gaps, implementation technology, provider accounts, and launch gates belong here and in the linked engineering notes.

## Product thesis

Savvy CRM is a mobile-first sales operating system for teams that receive leads from forms, ads, referrals, email, and messaging. Its primary loop is:

```text
Capture → Route → Respond → Follow up → Convert → Learn
```

The differentiator is not “more CRM fields.” It is a reliable next-action system that keeps the person, source, owner, conversation, promise, and revenue outcome connected across web and mobile.

## What we take from the market

Privyr was the initial example, not the sole blueprint. Savvy CRM should curate the strongest product ideas from focused lead tools, conventional CRMs, enterprise platforms, and modular open-source systems.

| Reference | Pattern worth adopting | Savvy interpretation | Do not copy |
|---|---|---|---|
| [Privyr](https://www.privyr.com/) | Direct lead capture, fast mobile alert, WhatsApp-first response, sequences, content tracking, Meta feedback | Make the first five minutes after an enquiry exceptionally clear | Its exact mobile flow, layouts, wording, or a narrow messaging-only ceiling |
| [Salesforce Sales Cloud](https://www.salesforce.com/sales/cloud/) | Governed automation, pipeline history, forecast discipline, metadata-driven records, integration health | Version workflows; expose failures; keep field/permission metadata consistent across web and mobile | Enterprise sprawl, edition/add-on maze, unrestricted code, or AI-first positioning |
| [HubSpot CRM](https://www.hubspot.com/products/crm) | Fast onboarding, understandable core objects, visible product UI, progressive complexity, broad ecosystem | Make the core useful before configuration; keep acquisition and customer context close | Marketing-suite breadth before the sales loop is reliable |
| [Zoho CRM](https://www.zoho.com/en-us/crm/) | Attract → pipeline → close narrative, mobile field actions, offline support, reminders, communication channels | Build an action-first phone experience with deliberate offline boundaries | Claiming full offline parity or adding unrelated suite products |
| [Pipedrive](https://www.pipedrive.com/en/products) | Activity-based selling, visual pipeline, Leads Inbox, deal ageing, next-action cues | “My Day,” unattended leads, stage ageing, and one explicit next action | Reducing the product to a kanban board without customer context |
| [Freshsales](https://www.freshworks.com/crm/features/) | Routing, sequences, omnichannel context, attribution, deduplication, scannable capability structure | One timeline and one follow-up engine across channels | Premature telephony/contact-centre scope |
| [Odoo CRM](https://www.odoo.com/app/crm-features) | Modular applications, daily next activities, assignment rules, UTM attribution, flexible analysis, self-hosting | Connector modules behind stable contracts; next-action and source-quality reports | Pulling accounting, HR, manufacturing, and inventory into CRM core |
| [ERPNext CRM](https://docs.frappe.io/erpnext/crm) and [Frappe Framework](https://docs.frappe.io/framework/user/en/basics/apps) | API-first document model, app extension hooks, explicit Lead → Opportunity → Quotation handoff, backup/self-host discipline | Versioned metadata/capability API and clean ERP/accounting handoff connectors | Copied source, unrestricted CRUD/SQL/scripts, or workflow changes that bypass policy |

### Product principles derived from the comparison

1. **Next action over passive storage.** Every lead, contact, task, and opportunity should answer “what should happen next, who owns it, and when?”
2. **Person first.** A name plus phone or email must be enough to create a lead. Company is useful, not mandatory.
3. **One provider-neutral intake pipeline.** Meta, forms, Google Ads, LinkedIn, imports, and partners should converge on the same verification, deduplication, attribution, routing, and audit machinery.
4. **Automation must be governed.** Draft, validate, dry-run, activate, version, pause, inspect runs, retry safely, and show fault reasons.
5. **Mobile is a focused product.** Lead inbox, My Day, record context, notes, tasks, pipeline movement, alerts, and one-tap communication—not a desktop admin screen copied to Flutter.
6. **Customization needs a contract.** Fields, layouts, permissions, validation, supported actions, and mobile renderers come from a versioned metadata API.
7. **ERP is an integration boundary.** Quotes, orders, inventory, and accounting should connect to Savvy CRM, not inflate its core.
8. **Analytics follow clean operations.** Measure response time, unattended leads, stage ageing, source quality, conversion, forecast, win/loss reasons, and sequence effectiveness before predictive AI.

## Customer-facing product scope

The public website may describe these as the intended Savvy CRM product. It should not show internal implementation status.

### Access and onboarding

- Login and signup use Google account sign-in or a passwordless six-digit email OTP; Savvy CRM does not ask users to create an application password.
- Transactional OTP mail is sent directly by the CRM API's minimal SMTP adapter as `Savvy CRM <support@savvyadvi.com>`, matching the existing company website. The selected provider must verify that sending identity before production use.
- Email OTPs are short-lived, single-use, purpose-bound, attempt-limited, and protected by shared rate limits and generic account-discovery-safe responses.
- Signup collects first name, last name, work email, and organization first; email verification happens before workspace creation.
- Onboarding then asks the user’s organization role, sales-team size, industry, primary goal, and optional lead-source mix so the initial workspace can be shaped appropriately.
- Google account authentication uses a separate Google client and consent path from later Gmail/Calendar CRM integrations.

### Customer and revenue data

- Person-first leads with name, E.164 phone, email, company, source, campaign, consent, owner, score, and next action.
- Contacts, accounts, relationships, notes, files, tags, custom fields, and custom objects.
- Opportunities, configurable pipelines and stages, value, probability, forecast category, expected close, stage age, loss reason, and history.
- Unified timeline across capture, ownership, communications, tasks, meetings, automation, and revenue changes.

### Acquisition and routing

- Website forms and signed webhooks.
- Facebook and Instagram Lead Ads.
- Google Ads lead forms.
- LinkedIn Lead Gen Forms.
- CSV import and partner/API ingestion.
- Source, campaign, ad, and form attribution.
- Deduplication and merge suggestions.
- Round-robin and rule-based assignment by source, geography, language, team, availability, capacity, and fallback.
- Instant owner alerts plus untouched-lead escalation.

### Conversations and follow-up

- One-tap call, email, SMS, and WhatsApp from lead and contact screens.
- Gmail sync and send.
- WhatsApp Business Platform conversations, templates, delivery/read/reply state, 24-hour window controls, and consent/STOP handling.
- Personalised templates and drafts.
- Scheduler-backed multi-step sequences using tasks, email, WhatsApp, and calls.
- Quiet hours, rate limits, pause/resume, and automatic stop on reply or conversion.
- My Day: new, overdue, due now, due next, and unattended leads.

### Mobile

- Dedicated iOS and Android apps.
- Action-first home, lead inbox, global search, lead/contact/account detail, timeline, notes, tasks, opportunities, and pipeline movement.
- Push notifications with deep links.
- One-tap channel actions.
- Encrypted local cache for selected recent records, visible last-sync state, and safe recovery.
- Responsive PWA as the full-coverage fallback for admin and unsupported mobile features.

### Automation, analytics, administration, and trust

- Typed event/condition/action workflow builder with versions and execution history.
- SLA, response-time, source-quality, funnel, stage-age, win/loss, owner, and forecast reporting.
- Saved filters, team views, exports, imports, and dashboards.
- Role/field/record permissions, tenant isolation, consent history, audit trail, privacy export, erasure, retention, backups, and restore.
- Integration connection health, token expiry/reconnect, delivery retry, and provider audit history.
- API, outgoing webhooks, and a bounded extension/module model.

## Internal gap register

| Priority | Gap in the current repository | Why it blocks the product loop | Required outcome |
|---|---|---|---|
| P0 external acceptance | Meta connector code is complete; app credentials/review and live provider evidence are not | Production traffic could still fail at provider authorization or asset access | Approve the Meta app, connect controlled Business/Page/Form assets, pass Facebook and Instagram test leads, and retain evidence |
| P0 external acceptance | Lead-level WhatsApp handoff is implemented and browser-tested; no physical-device record exists | URI handling can differ between iOS, Android, WhatsApp and WhatsApp Business | Run the documented captured-lead handoff on one supported iPhone and Android device |
| P1 | In-app/email alerts and untouched-lead SLA escalation are implemented; native device push is not | Owners receive durable email and fast in-app notice, but a closed browser has no push notification | Add device registration and FCM/APNs delivery on the existing notification ledger |
| P1 | No ready speed-to-lead preset | Existing workflow pieces require manual expert setup | Safe preset: assign → notify → task → personalised draft |
| P1 | Ordered workflow steps now support durable delays; explicit enrollment, recurrence and reply-aware stopping are not built | Basic follow-up cadence executes, but advanced sequence lifecycle controls remain | Add enrollment/pause/resume, recurrence and reply-aware stop conditions |
| P1 | Provider verification is mostly stubbed | Gmail/Calendar and mobile handoffs lack production evidence | Real-account and real-device acceptance matrix |
| P2 | No WhatsApp Business inbox | No inbound matching, templates, receipts, replies, or 24-hour policy | Cloud API/BSP connection, durable conversation model, compliance controls |
| P2 | Limited duplicate/import/merge workflow | Acquisition scale reduces data quality | Person-level duplicate rules, review queue, merge history, robust imports |
| P2 | Analytics lack response/source/stage operating metrics | Team cannot see where leads stall | SLA, attribution, unattended-lead, stage-age, and conversion dashboards |
| P3 | No trackable content or conversion feedback | Cannot close the acquisition learning loop | Content events, provider consent, Meta CAPI, campaign feedback |

Detailed Privyr-specific evidence remains in [PRIVYR_COMPARISON.md](PRIVYR_COMPARISON.md).

## Technology reference

This is the engineering stack, not website marketing copy.

### Current web and services

| Layer | Current repository technology | Direction |
|---|---|---|
| Web application | React 18, TypeScript strict mode, Vite, React Router, TanStack Query, React Hook Form, Zod, Tailwind | Keep; add server-driven metadata/capability rendering and mobile-specific acceptance coverage |
| API contracts | Protocol Buffers, Connect, generated Go and TypeScript clients | Keep as the versioned source of truth; add Dart generation for mobile |
| CRM API | Go, Connect-Go, pgx | Keep domain boundaries and optimistic concurrency; add integration adapters and scheduler/device APIs |
| Identity | Native Go package inside `crm-api`: email OTP, Google sign-in, opaque sessions, local users/organizations/memberships and four CRM roles | Keep deliberately small; add multi-membership selection, mobile one-time session handoff, shared abuse controls, recovery, and production acceptance tests |
| Primary data | PostgreSQL with tenant `org_id` and row-level isolation controls | Keep; use managed RDS/Aurora only after sizing and operations decisions |
| Cache/session | PostgreSQL stores hashed opaque sessions, OTP challenges, and Google state | Keep PostgreSQL authoritative; add shared Redis/Valkey only if horizontally consistent rate limiting or measured cache demand requires it |
| Events | NATS JetStream plus transactional outbox patterns | Keep; require recoverable delivery and explicit worker ownership |
| Observability | OpenTelemetry and Prometheus instrumentation | Export to the selected production metrics/tracing/logging stack with tenant-safe labels |
| Local/runtime packaging | Docker, Compose, nginx | Keep for development; use repeatable production images and managed orchestration |
| Testing | Go tests, Vitest/Testing Library, Playwright, live-stack integration suites | Add provider sandboxes, two-tenant tests, physical-device checks, contract generation checks, and CI launch gates |

### Marketing website

- Static semantic HTML, CSS, and small progressive-enhancement JavaScript.
- No dedicated runtime server and no credentials in the bundle.
- The static Login / Sign-up dialog is the real web account entry. Production Google and email-OTP actions use `crmapi.savvyadvi.com/auth/*`; the static origin never receives SMTP/Google secrets, OTP verifiers, or server-side session state.
- Private Amazon S3 origin, CloudFront Origin Access Control, ACM TLS, Route 53 alias, security headers, versioning, and cache-aware deployment are the primary path; Azure Static Web Apps is a supported alternative.
- Marketing/auth-dialog host: `crm.savvyadvi.com`.
- Authenticated app host: `crmapp.savvyadvi.com`.
- API/auth host: `crmapi.savvyadvi.com`.
- Full architecture: [Savvy CRM website AWS hosting architecture](../../savvy_crm_website/docs/AWS_HOSTING_ARCHITECTURE.md).

### Mobile

- Flutter with one repository and separate staging/production entrypoints.
- Generated Connect-Dart client from the existing protobuf contracts.
- Direct email-OTP JSON flow plus system-browser Google sign-in through the CRM API; no Google client secret in the app.
- Keychain/Keystore-backed storage for one opaque CRM session token; no access/refresh JWT pair.
- Feature-first modules with Views/ViewModels, repositories as source of truth, services for API/auth/database/push, and deliberate domain use cases.
- Encrypted, tenant-partitioned cached reads first; online-only writes until idempotency and conflict UX exist.
- Firebase Cloud Messaging as transport for Android and iOS, with no customer PII in push payloads.
- Full plan: [FLUTTER_MOBILE_ARCHITECTURE.md](FLUTTER_MOBILE_ARCHITECTURE.md).

## Product/application hosting shape

```text
Public marketing
  crm.savvyadvi.com
    → Route 53 → CloudFront → private S3

Authenticated clients
  crmapp.savvyadvi.com (React/PWA)
  iOS + Android (Flutter)
    → crmapi.savvyadvi.com
        → load balancer / API edge
        → CRM API + workers
        → PostgreSQL, optional shared limiter/cache, NATS JetStream
        → native `/auth/*` routes and CRM RPCs in the same service

Provider callbacks and webhooks
  crmapi.savvyadvi.com/integrations/{provider}/...
    → signature/OAuth verification
    → provider-neutral intake or conversation adapter
    → transactional storage + outbox
    → routing, notification, automation, analytics
```

Recommended production separation:

- Marketing and React application remain static deployments in separate S3 buckets/distributions because their cache, headers, routing, and release cadence differ.
- CRM API and background workers run as versioned containers behind private networking and a public HTTPS edge.
- Use managed PostgreSQL for CRM and native-auth records with least-privilege runtime/migration roles. Add managed Redis/Valkey only for a justified shared limiter/cache workload.
- Run a durable NATS JetStream service or managed equivalent; do not replace delivery guarantees with an in-process queue.
- Put database/provider credentials in Secrets Manager or an equivalent secret store, encrypted with controlled KMS keys.
- Use separate staging and production AWS accounts or at minimum isolated VPCs, secrets, databases, buckets, domains, OAuth clients, Meta apps, Firebase projects, and WhatsApp senders.
- Keep raw webhook bodies, message content, and exports under explicit retention and erasure policy.

## Delivery order

### Phase 1 — reliable CRM core

1. Rename product surfaces to Savvy CRM while preserving compatibility identifiers.
2. Implement person-first leads and repair/recover generic capture.
3. Add duplicate/merge flow, next-action/My Day view, unattended-lead report, stage age, and source attribution.
4. Make automation draft/test/activate/version/run-history behaviour explicit.

### Phase 2 — acquisition and first response

1. Native Meta connector for both Facebook and Instagram.
2. Instant push/email owner alerts and SLA escalation.
3. Lead-level call/email/SMS/WhatsApp actions and response-time measurement.
4. Real Google, Meta, browser, and mobile acceptance tests.

### Phase 3 — mobile MVP

1. Native mobile one-time session handoff and multi-membership workspace selection.
2. Flutter foundation, generated contracts, auth, routing, telemetry, and CI.
3. My Day, lead inbox/detail, contacts/search, tasks, one-tap actions, and push.
4. Opportunities, pipeline movement, notes, templates, cached reads, conflict handling, TestFlight, and Play closed testing.

### Phase 4 — conversations and cadence

1. WhatsApp Business Platform inbox, templates, receipts/replies, consent, and 24-hour-window policy.
2. Scheduler-backed tasks, reminders, activity plans, and reply-aware sequences.
3. Gmail/WhatsApp unified communication context and deeper automation conditions.

### Phase 5 — ecosystem and intelligence

1. Google Ads and LinkedIn native adapters using the proven connector contract.
2. Trackable content and conversion feedback such as Meta CAPI.
3. Safe report builder, cohort/source analysis, ERP/accounting connectors, and signed extension packages.
4. Predictive scoring only after data-volume and quality thresholds are defined and met.

## Explicit non-goals for initial releases

- Full ERP, inventory, accounting, payroll, manufacturing, or e-commerce inside the CRM core.
- Arbitrary tenant-authored SQL, JavaScript, Go, or unrestricted workflow code.
- Full desktop/custom-object parity in the first Flutter release.
- Generic offline mutation queues before idempotency, authorization re-check, and conflict resolution exist.
- AI lead scoring before clean won/lost and source/response history exists.
- A marketplace before connector packaging, permission review, upgrade compatibility, and uninstall behaviour are safe.
- Feature names, UI layouts, copy, source code, or licensed assets copied from benchmark products.

## Decisions still required

1. Confirm control of the `savvyadvi.com` hosted zone before creating the selected `crm`, `crmapp`, and `crmapi` records.
2. Choose product infrastructure owner and launch AWS account/region strategy.
3. Choose multi-tenant workspace discovery for mobile.
4. Approve data/message/raw-payload retention and erasure periods.
5. Choose KMS/secrets ownership and rotation process.
6. Choose WhatsApp Cloud API directly versus a BSP, including commercial/message-cost ownership.
7. Approve quiet hours, rate limits, consent evidence, and automatic-send policy.
8. Choose push, crash reporting, product analytics, data residency, and PII-scrubbing rules.
9. Define first launch segment and the minimum integrations required for that segment.

## Linked internal notes

- [Production application architecture](PRODUCTION_ARCHITECTURE.md)
- [Flutter mobile architecture](FLUTTER_MOBILE_ARCHITECTURE.md)
- [Third-party account and acceptance-test plan](THIRD_PARTY_ACCOUNT_TEST_PLAN.md)
- [Privyr comparison and gap evidence](PRIVYR_COMPARISON.md)
- [Product roadmap](ROADMAP.md)
- [Google integration](GOOGLE_INTEGRATION.md)
- [Lead capture](LEAD_CAPTURE.md)
- [Workflow automation](WORKFLOW_AUTOMATION.md)
