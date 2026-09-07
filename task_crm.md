# Privyr parity — gap closure plan (task_crm)

## Purpose

`task.md` answers "is the CRM we scoped finished?".
`task_v2.md` answers "what stands between this and Salesforce/HubSpot/Zoho?".

This document answers a third, narrower question: **"what does Privyr
(https://www.privyr.com/intro/crm/) do that this codebase cannot do at all?"**

Privyr is not a competitor on the same axis as the platforms in `task_v2.md`.
It is a mobile-first, chat-centric *speed-to-lead* tool for solo agents and
small sales teams. Its value is the seconds between an ad click and a
personalised WhatsApp message. This codebase is a multi-tenant B2B CRM whose
value is correctness, isolation, auditability, and team-scale record
management. **Most of Privyr's surface we already exceed.** This file covers
only the part where we are at zero, and it is deliberately opinionated about
which of those gaps are worth closing.

The same evidence rule as the other two files applies: a checkbox may be
marked complete only after its stated acceptance criteria and verification
have passed. The presence of a migration, screen, endpoint, or test file is
not completion.

---

## Non-negotiable boundaries (inherited, not restated in each task)

These come from `task.md` and hold for every task in this file. They are
listed once because messaging work is the most likely place in the product to
break them.

- **Tenancy.** Every new table carries `tenant_id`, has RLS `ENABLE`d and
  `FORCE`d, a tenant-aware primary key and indexes, and is reached through the
  scoped store helper. No request may name a tenant from browser-supplied
  body or query data. New tables must be added to the fixture behind
  `TestUnscopedQuerySeesNothingInAnyTenantTable` — a policy that exists but is
  never asserted to filter is the failure mode the metadata-layer tables are
  already in.
- **Migrations are append-only.** 0028–0030 are taken by the work below; the
  next free number is `0031_*.sql`. Never edit a shipped migration.
- **Deny by default.** Every new procedure needs an entry in
  `services/crm-api/internal/server/scopes.go`. `assertPolicyIsTotal` refuses
  to start a server whose policy does not decide every served procedure, so a
  handler added without a decision is unreachable rather than public.
- **The role matrix is pinned on both sides.** A new scope must land in
  `packages/contracts/rbac/crm-roles.json`, in `scopes.go`, and in AuthinCore's
  `cmd/authinctl/bootstrap.go`, with `role_matrix_test.go` and
  `bootstrap_roles_test.go` updated. Existing organizations need `make
  bootstrap` re-run to pick a new scope up — this bit us before on
  `crm:pipeline:read`.
- **Provider credentials.** Sealed with AES-256-GCM by the pattern in
  `internal/integration/googleoauth`, associated data binding the ciphertext to
  `(tenant_id, connection id)` so a row copied into another tenant fails to
  open. Never reach the browser, never reach a log, purged on disconnect. See
  `migrations/0019_google_connections.sql` for the table shape to copy,
  including the `CHECK` that a disconnected row holds no ciphertext.
- **Outbox, not dual-write.** Integration and workflow events go through the
  transactional outbox in the same transaction as their relational write.
  Consumers are idempotent and carry tenant context.
- **Bounded everything.** Cursor pagination with a maximum page size on every
  list. Bulk operations bounded (the existing bound is ≤200 ids) and
  per-item authorized, reporting per-item success or failure rather than
  all-or-nothing.

---

## Verified baseline — Privyr surface we already cover

Measured from the working tree, not estimated. This section exists so nobody
builds something twice.

| Privyr capability | Covered by | Evidence |
| --- | --- | --- |
| Lead management, notes, follow-up reminders | Leads + tasks + notes + activity timeline | `proto/crm/v1/crm.proto`, `internal/api/crm.go`, `apps/web/src/features/leads` |
| Client/contact records with timeline | Accounts + contacts with shared timeline | `apps/web/src/features/{accounts,contacts}` |
| Contact import | CSV import with preview/commit, field mapping, per-row errors | `docs/CSV_IMPORT_EXPORT.md`, `migrations/0027_import_export.sql` — **accounts only** |
| Team CRM, multi-user | Multi-tenant + 4-role RBAC + OIDC/PKCE identity service | `packages/contracts/rbac/crm-roles.json`, `services/authincore` |
| New-lead automations | Rule engine on `lead.created` / `opportunity.stage_changed` / `opportunity.closed_won` | `docs/WORKFLOW_AUTOMATION.md`, `internal/integration/workflow` |
| Auto follow-up reminders | `create_task` action with `record_owner`/`fixed_user` assignee selector | `internal/integration/workflow/actions.go` |
| Sales oversight / manager view | Dashboard: per-pipeline stage breakdown, task tiles, conversion funnel, weighted forecast | `internal/domain/reporting.go`, `GetDashboardReport` |
| Encryption in transit and at rest | TLS + AES-256-GCM sealed credentials + RLS-enforced isolation | `migrations/0019`, `internal/integration/googleoauth` |
| Mobile-usable layout | Off-canvas drawer nav below `lg`, stacking detail lists and form pairs, horizontally scrolling tables, axe-core static a11y floor | `apps/web/src/components/shell/{AppShell,Sidebar}.tsx`, `src/test/a11y.ts` |

**Correction to a common misreading of `docs/ROADMAP.md`:** M2 still describes
the shell as "desktop-only, not yet responsive". That text is stale. The
responsive drawer, stacking, and a11y work is done and documented in
`task.md` § *Web application*. What is missing on the mobile axis is not
layout — see §10.

Also worth stating plainly, because it changes what "parity" is worth: Privyr
has **no** configurable pipelines with weighted forecasting, no custom
fields/objects, no record merge with audit trail, no saved views, no
versioned workflow rules with an execution ledger, no RLS-enforced tenant
isolation proven at four layers, and no first-party identity service. Parity
is one-directional here. Do not trade any of the above away to buy items
below.

---

## The gap list

| § | Gap | Verdict | Status |
| --- | --- | --- | --- |
| 1 | Inbound lead capture webhooks (Facebook Lead Ads, Google Ads, WordPress, Zapier, web forms) | **Build.** Highest value per unit of work. | **Done** — D1 answered conservatively, see below |
| 2 | Contact channel model + consent/opt-out ledger | **Build first.** Prerequisite for §3–§7. | **Done** — see below |
| 3 | Click-to-chat outbound (WhatsApp/SMS/tel deep links) + logged activity | **Build.** Cheapest real capability in this file. | **Done, pending a real-device check** |
| 4 | Provider-sent messaging (WhatsApp BSP, SMS, `gmail.send`) | **Defer** until §3 shows demand. | Not started — D2, D3, D4 |
| 5 | Message templates + 1-touch quick replies | **Build** (literal first, merge fields after §5.3). | **Done, including merge fields** |
| 6 | Messaging actions in the workflow engine | **Build** after §3 and §5. | Not started — needs D6 (quiet hours) |
| 7 | Bulk / campaign sending | **Defer.** Highest legal risk, lowest fit for a B2B CRM. | Not started — D3, legal review |
| 8 | Automatic lead distribution (round-robin / rules) | **Build.** Small, self-contained, no external dependency. | **Done** |
| 9 | Trackable file sharing (PDF open tracking) | **Defer.** | Not started — D5 |
| 10 | Mobile speed-to-lead (PWA, push, tap-to-act) | **Build §10.1–10.3.** Native app: **do not build.** | Not started |
| 11 | Meta Conversions API / ad conversion feedback | **Defer.** | Not started — §1 first |

## What has been delivered

Five sections, in the order the sequencing at the bottom of this file gives.
Each was verified the way this repository requires — against live PostgreSQL
with row-level security, over real HTTP with real signed tokens where the
change reaches that far — not merely written.

**§2 — the consent ledger and its gate.** `migrations/0028_messaging_consent.sql`,
`internal/domain/messaging.go`. One gate, `MayMessage`, that every send path
passes through, and an append-only ledger behind it. Channels are *derived*
from `Contact.Email`/`Contact.Phone` rather than stored, so there is no
channel table and no data migration; the sketch above proposed one and it
turned out not to be needed. Three RPCs behind a new `crm:consent` scope pair,
split so a viewer can see that somebody opted out without being able to assert
that they opted in.

Two defects were found by its own tests and fixed, each with the test that
fails without the fix. A same-instant tie between a grant and a withdrawal
resolved at random, because stamps truncate to the microsecond and ids are
random — a tie now resolves to `withdrawn`, since we do not know which came
first and the restrictive answer is the safe one. And a local phone number and
its international form were different consent keys, so a withdrawal against
`+15550100000` did not stop a message to `555 010 0000`. Inferring the country
code would have been worse than the bug — guessing `+1` for a tenant elsewhere
yields a reachable *different person's* number — so a number without a country
code can now be neither messaged nor recorded against, and the UI says which
code is missing.

**§3 — click-to-chat.** `apps/web/src/lib/messagingLinks.ts`,
`components/ContactChannelsPanel.tsx`. `wa.me` / `sms:` / `tel:` / `mailto:`
links that open the user's own app with the message pre-filled. The panel
renders the server's verdict rather than computing one, so no second
implementation of `MayMessage` exists in TypeScript. The timeline entry says
**opened**, never *sent*: all we know is what was handed to the operating
system.

Still open, as this section's own acceptance criteria require: a real-device
check. Deep-link behaviour differs by OS and cannot be proven in jsdom, and the
`sms:` body spelling (`?&body=`) is empirical rather than specified.

**§5 — message templates.** `migrations/0030_message_templates.sql`,
`internal/domain/message_template.go`. Merge fields shipped in this version
rather than waiting for §5.3, because rendering happens in the browser from
the record already on screen — a user cannot leak to themselves what they are
looking at — and the vocabulary is an allowlist of fields any role that can
read the record can already read. The field-visibility model is still required
for a *server-side* render, which is what an automated send needs, so §6
inherits that blocker unchanged.

**§8 — lead distribution.** `migrations/0029_lead_distribution.sql`,
`internal/domain/lead_distribution.go`. An `assign_owner` workflow action
rather than an engine of its own, so it inherits rule versioning, conditions,
the execution ledger, retries and loop prevention. Nine concurrent assignments
through a real connection pool take nine distinct turns with an even split;
a failed assignment leaves the rotation exactly where it was rather than
burning a turn.

**§1 — inbound lead capture.** `migrations/0031_lead_capture.sql`,
`internal/integration/leadcapture`, `docs/LEAD_CAPTURE.md`. An external system
POSTs a signed lead and an *ordinary* Lead appears in the right tenant —
ordinary being the point, since it emits the same `lead.created` event and the
workflow engine and §8's rotation fire on it unchanged.

**D1 is answered rather than still open.** The decision record in
`docs/LEAD_CAPTURE.md` states it: unauthenticated because the caller is a
third-party server, behind a *required* HMAC-SHA256 signature with no unsigned
mode, an unguessable token that also resolves the tenant on the drain pool,
and a body cap plus a per-endpoint rate limit. A stricter answer — an IP
allowlist, a WAF — sits in front of this and replaces none of it.

The response deliberately says almost nothing: an unknown token and a bad
signature are answered identically, or the response becomes an oracle for
whether a token is real.

Two defects only the live pass could find, both fixed with tests: the service
crash-looped on boot because the new key was added to the group that checks
Google's all-or-nothing configuration, and the delivery log recorded "created"
without recording which lead it created.

### What that leaves

The remaining unblocked work is §10.1–10.3 (installability, push,
mobile-viewport tests) and §6 (messaging automation, which additionally wants
D6). Everything else is behind a decision listed below.

Two things are worth carrying forward rather than leaving in commit messages.
**§3 is not finished** until somebody sends a real message from a real phone:
deep-link behaviour differs by operating system and the `sms:` body spelling
is empirical, not specified. And **no browser test has ever rendered this app
at a phone width** — the responsive work is real but is currently protected by
review and jsdom, which has no layout.

---

## Decisions required from the product owner — BLOCKING

None of §4, §7, §9 or §11 may start before the matching decision lands. This
mirrors `docs/WORKFLOW_AUTOMATION.md` § *Decisions still needed before
integrations*, which already refuses to guess these.

- [x] **D1 — Ingress policy.** *Answered conservatively and implemented; see
  `docs/LEAD_CAPTURE.md`. Revisit only to add controls in front of it.* Are unauthenticated inbound webhook endpoints
  acceptable on the CRM API surface, and behind what (IP allowlist, WAF,
  provider signature verification, rate limit per endpoint)? Today the only
  unauthenticated route is the Google OAuth callback
  (`internal/server/google_callback.go`); §1 adds a second class of them.
- [ ] **D2 — Messaging provider and cost owner.** WhatsApp requires a Meta
  Business account, a WhatsApp Business Account, an approved sender number,
  and per-conversation pricing. SMS requires a provider (Twilio/Vonage/SNS)
  and, in the US, 10DLC brand/campaign registration. Both are recurring cost
  and a named commercial relationship, not an engineering choice.
- [ ] **D3 — Consent and retention policy.** What constitutes consent to
  message a contact, how is it recorded, how long are message bodies retained,
  and what does erasure remove? The synced-mail integration already answers
  the retention half structurally (no body column exists); outbound messaging
  cannot use that answer because it must store what was sent.
- [ ] **D4 — Production secret/KMS ownership.** Still open from `docs/ROADMAP.md`
  M6. Sealing-key rotation has no re-encryption path today; adding a second
  class of sealed provider credentials makes that gap wider, not new.
- [ ] **D5 — Object storage.** Provider, geography, retention, max size/type,
  malware scanning, download authorization, legal hold. Already blocking
  `task.md` §5.2. §9 inherits it verbatim.
- [ ] **D6 — Quiet hours and rate policy.** Per-tenant sending windows and a
  per-tenant/per-recipient rate ceiling. Required before §6 or §7 — an
  automation that can message is an automation that can message at 3am.

---

## §1 — Inbound lead capture

**What Privyr does.** Connect a website form, Facebook Lead Ad, TikTok,
WordPress, ClickFunnels, LinkedIn, Google Ads or Google Form, and new leads
land in the CRM within seconds with an alert.

**What we have.** Nothing inbound. `CreateLead` is an authenticated RPC
requiring `crm:lead:write` and a browser session. There is no way for an
external system to deliver a lead.

**Why this is first.** It is the only gap in this file that is pure
engineering — no provider contract, no recurring cost, no legal review — and
it is the head of Privyr's entire value chain. Every other item is downstream
of a lead arriving.

### 1.1 Capture endpoints (the generic mechanism)

- [ ] Resolve **D1** before writing code.
- [ ] `migrations/0028_lead_capture.sql` — `lead_capture_endpoints`
  (tenant-scoped; `id`, `name`, `source_kind`, `secret_ciphertext`,
  `field_mapping` JSONB, `default_owner_id`, `default_pipeline_id`, `status`,
  `created_by`, timestamps, `last_delivery_at`, `last_error` as a bounded
  reason code never a provider body) and `lead_capture_deliveries` (raw
  payload, dedup key, outcome, resulting `lead_id`, `received_at`). RLS
  enabled and forced on both.
- [ ] Tenant resolution follows the `google_oauth_states` precedent exactly: an
  **unguessable, globally unique** endpoint token carried in the URL path,
  with a `UNIQUE` index below RLS so two tenants cannot collide, resolved by
  the `crm_drain` role in one query returning **identifiers only**. Every
  write that follows runs on the application pool with that tenant bound.
  `crm_drain` is granted `SELECT` on the endpoint-token lookup and nothing
  else — never on a table holding a sealed secret.
- [ ] Signature verification per source kind (Meta's `X-Hub-Signature-256`,
  a shared-secret HMAC-SHA256 for generic/Zapier). A payload failing
  verification is recorded as a rejected delivery and answered with a
  non-committal status — never an error naming why.
- [ ] Idempotency: `UNIQUE (tenant_id, endpoint_id, dedup_key)` where
  `dedup_key` is the provider's own event/lead id, falling back to a hash of
  the normalised payload. Providers retry; a retry must not create a second
  lead. This is the same at-least-once discipline the workflow engine already
  turns on.
- [ ] Bounded ingestion: max body size, per-endpoint rate limit, and a payload
  retained only as long as **D3** allows.
- [ ] Route registration in `internal/server/server.go` alongside
  `googleCallbackPath`, wrapped in the same middleware chain. It is
  unauthenticated by design and must therefore never echo request content back.

### 1.2 Field mapping and lead creation

- [ ] Map provider fields to lead fields through the stored `field_mapping`,
  reusing the CSV import mapping vocabulary from `docs/CSV_IMPORT_EXPORT.md`
  rather than inventing a second one.
- [ ] Unmapped fields land in a bounded `raw_payload` on the delivery row, not
  silently dropped — "the lead came in but the phone number is missing" is the
  first support ticket this feature will generate.
- [ ] Created leads emit the existing `lead.created` outbox event, so the
  workflow engine fires with no change to it. **This is the whole point of the
  design:** capture must produce an ordinary lead, not a special one.
- [ ] Duplicate detection uses the existing lead dedup flags — a captured lead
  matching an existing contact is flagged, never merged automatically.

### 1.3 Administration and observability

- [ ] RPCs: `CreateLeadCaptureEndpoint`, `ListLeadCaptureEndpoints`,
  `UpdateLeadCaptureEndpoint`, `RotateLeadCaptureSecret`,
  `DisableLeadCaptureEndpoint`, `ListLeadCaptureDeliveries`. New scope pair
  `crm:capture:read` / `crm:capture:write`, admin/manager only — an endpoint
  is tenant-wide authority to create records.
- [ ] Web: a Settings → Lead capture page showing each endpoint's URL, secret
  rotation, mapping editor, and a delivery log with per-delivery outcome.
  The delivery log is the feature's own debugger; without it every failure is
  a support call.
- [ ] Metrics with **bounded** labels: `crm_lead_capture_deliveries_total`
  by `{source_kind, outcome}`. Never label by endpoint id or tenant —
  `docs/OBSERVABILITY.md` sets that rule.
- [ ] Provider recipes in `docs/LEAD_CAPTURE.md`: exact setup steps for Meta
  Lead Ads, a generic HTML form, Zapier, and Google Forms. Written **before**
  implementation, in the style of `docs/CSV_IMPORT_EXPORT.md`.

**Acceptance criteria.** An external POST with a valid signature creates a
tenant-correct lead and fires existing automations. An invalid signature
creates nothing and reveals nothing. A duplicate delivery creates exactly one
lead. A delivery for tenant A is invisible to tenant B at every layer. The
endpoint token is the only tenant selector and cannot be guessed or enumerated.

**Verification.** API-tier tests for signature accept/reject, replay, and
oversized body. A two-tenant isolation probe in the style of
`internal/api/tenant_isolation_test.go`. A live end-to-end delivery against
the Compose stack producing a lead visible in the UI. A concurrent
double-delivery test proving one lead.

**Lazy first rung.** Ship one `source_kind` — `generic` HMAC — plus the
delivery log and settings page. Meta/TikTok/LinkedIn recipes are then
configuration of an endpoint that already works, not new code. Do not build
per-provider adapters until a provider's payload actually refuses to fit.

---

## §2 — Contact channel model and consent ledger

**What Privyr does.** Implicitly: every contact has a phone number that is
also a WhatsApp identity, and messaging it is assumed permitted.

**What we have.** Contacts have contact fields, but there is no notion of a
*channel* (which address on which medium), no verification state, and no
consent or opt-out record anywhere in the schema.

**Why this is a prerequisite and not an afterthought.** Every item in §3–§7
sends something to a person. Consent, opt-out and quiet hours are legal
obligations under TCPA/GDPR/PECR and WhatsApp's own policy — not features to
be added once the sending works. Building the send path first and retrofitting
consent is how a product ends up with an un-auditable "did we have permission"
question. This section is small; it is listed separately because it must land
before anything can send.

- [ ] `migrations/0029_contact_channels.sql` — `contact_channels`
  (tenant-scoped; `contact_id`, `kind` in `email|sms|whatsapp|voice`,
  `address` normalised to E.164 for phone kinds, `is_primary`,
  `verified_at`, timestamps) with RLS and a
  `UNIQUE (tenant_id, contact_id, kind, address)`.
- [ ] `messaging_consent` — append-only per `(tenant, channel kind, address)`:
  `state` in `granted|withdrawn|unknown`, `source` (capture endpoint, import,
  manual, inbound STOP keyword), `recorded_at`, `recorded_by`, and the
  evidence reference. Append-only because "when did they opt out" must be
  answerable after the fact; a mutable flag cannot answer it.
- [ ] A single `domain.MayMessage(scope, channel) (bool, reason)` gate that
  **every** send path calls. One function, checked once, so a new send path
  cannot forget it. Consent state, suppression list (reuse the existing
  `ListSyncSuppressions` suppression concept rather than adding a second),
  and quiet hours (**D6**) all resolve here.
- [ ] E.164 normalisation on write, so `+1 (555) 010-0000` and `+15550100000`
  are one address for consent and dedup purposes.
- [ ] Extend the erasure path in `internal/integration/privacy/erasure.go` to
  cover channels and message bodies, so the existing data-subject erasure
  story stays whole rather than acquiring an exception.

**Acceptance criteria.** No send path can reach a provider without passing
`MayMessage`. A withdrawn consent is durable, timestamped, attributable, and
survives contact edits. Erasure removes readable message content while leaving
dedup and consent history intact, exactly as the synced-content tombstones do.

**Verification.** A test that enumerates every send call site and asserts each
routes through the gate — the same shape as `assertPolicyIsTotal`, and for the
same reason: totality checked, not trusted.

---

## §3 — Click-to-chat outbound (the lazy rung that delivers most of the value)

**What Privyr does.** One tap opens WhatsApp/SMS/email with a personalised
message pre-filled, then logs the interaction against the contact.

**What we have.** Nothing. Gmail sync is read-only (`format=metadata`,
deliberately no body column), and `gmail.send` is requested in the OAuth scope
set but unused.

**The insight that makes this cheap.** Privyr's core loop does not require
being a messaging provider. A `https://wa.me/<e164>?text=<encoded>` link, an
`sms:` link, a `tel:` link, or a `mailto:` link opens the user's own installed
app with the message pre-filled. The user presses send. No BSP contract, no
per-message cost, no template pre-approval, no 24-hour window, no 10DLC
registration — and the message comes from the rep's own number, which is what
the recipient expects anyway.

This is one rung on the ladder below §4 and delivers most of the observable
value. Build it first; climb to §4 only if measured usage demands it.

- [ ] Channel actions on contact, lead, and opportunity detail pages: Call,
  SMS, WhatsApp, Email — rendered only for channels the contact actually has
  and `MayMessage` permits.
- [ ] Message body composed from a template (§5) with merge fields resolved
  **client-side from data already on the page**, so no new field-visibility
  question arises: the user cannot leak to themselves a field they are already
  looking at.
- [ ] On activation, write an `Activity` of the matching type through the
  existing immutable Activity API, recording channel, address, and the text
  handed off. Mark it `intent` — we know what we handed to the OS, not that it
  was sent. **Do not label it "sent".** A CRM that claims a message was
  delivered when it only opened an app is lying to its user.
- [ ] A follow-up prompt: after a click-to-chat, offer to create the follow-up
  task in one step, reusing `CreateTask`.
- [ ] No new scope. This is `crm:activity:write` plus a link.

**Acceptance criteria.** A rep on a phone reaches WhatsApp with a personalised
message in one tap from a lead record, and the timeline shows the attempt with
its channel and text. A contact with withdrawn consent shows no messaging
action at all.

**Verification.** Component tests for link construction, including correct
percent-encoding of the message text and E.164 formatting of the address
(these are the two things that break silently). Playwright coverage at a
mobile viewport (see §10.3). A live test on a real device before this is
marked done — deep-link behaviour differs by OS and cannot be proven in jsdom.

**What this deliberately does not do.** No delivery receipts, no inbound
replies, no automation-triggered sending, no bulk. Those are §4, §6, §7 —
each of which needs **D2**.

---

## §4 — Provider-sent messaging

**What Privyr does.** Sends from its own infrastructure: WhatsApp
auto-responder, WhatsApp campaigns, chat monitoring.

**Verdict: defer** until §3 is live and its usage proves the demand. This is
the most expensive item in the file — a commercial relationship, a recurring
per-message cost, an approval process, and an on-call surface — and §3 covers
the same user intent for a fraction of it.

When it is taken up, the shape is already determined by existing patterns:

- [ ] **D2, D3, D4 must all be resolved first.** No exceptions.
- [ ] A `messaging_connections` table copying `google_connections` verbatim in
  structure and constraints, including the CHECK that a disconnected row holds
  no ciphertext and the CHECK that a connected row holds tokens.
- [ ] `outbound_messages`: tenant-scoped, with provider message id, status
  (`queued|sent|delivered|read|failed`), a bounded failure reason code, and the
  body — the first table in this codebase to store message content, which is
  exactly why **D3** gates it.
- [ ] Sending goes through the outbox and a poller, in the shape of
  `internal/integration/{gmail,calendar}/poller.go`, so at-least-once delivery
  meets an idempotency key rather than duplicate customer messages. The
  claimed-row-wins pattern from the workflow engine applies unchanged.
- [ ] Inbound webhooks for delivery receipts and replies reuse §1's endpoint,
  signature, and dedup mechanism. Do not build a second webhook framework.
- [ ] Inbound `STOP`/`UNSUBSCRIBE` keyword handling writes a `withdrawn`
  consent row through §2 automatically. This is not optional in any
  jurisdiction that matters.
- [ ] WhatsApp specifics that are non-negotiable and non-obvious: business-
  initiated messages outside the 24-hour customer service window require a
  **pre-approved template**, approval is asynchronous and can be rejected, and
  template state must therefore be modelled as a first-class thing that can be
  `pending`/`approved`/`rejected` — not a string in a text box.
- [ ] `gmail.send` is the cheapest sub-rung here: the OAuth scope is already
  requested and the connection lifecycle already exists. Sending mail as the
  connected user needs no new provider relationship at all. If any part of §4
  ships first, it should be this one.

**Acceptance criteria.** No message is sent twice on a retry. Every send is
consent-gated, rate-limited, quiet-hours-respecting, tenant-isolated, and
audited. Provider credentials never reach the browser or a log and are purged
on disconnect. A failed send surfaces a bounded reason code, never a provider
response body.

---

## §5 — Message templates and quick replies

**What Privyr does.** Pre-written messages with the contact's details merged
in, sent in one tap.

**What we have.** Nothing. `docs/WORKFLOW_AUTOMATION.md` states the reason
explicitly: *"Action text is literal — there is no template expansion, because
a template that can read the record can read fields the rule author is not
allowed to see."* That reasoning is correct and this section must respect it,
not route around it.

- [ ] `migrations/00XX_message_templates.sql` — tenant-scoped templates:
  `name`, `channel_kind`, `body`, `is_shared`, `created_by`, timestamps, RLS.
  Owner-scoped by default the way saved views are (`migrations/0025`), since
  sharing needs record-level authorization that does not exist yet.
- [ ] CRUD RPCs behind a new `crm:template:read` / `crm:template:write` scope
  pair. Read for all roles, write for admin/manager.
- [ ] **§5.1 — literal templates first.** A template is fixed text. No merge
  fields. Ship this; it is most of the value and raises no new question.
- [ ] **§5.2 — a fixed, allowlisted merge vocabulary.** `{{contact.first_name}}`,
  `{{contact.full_name}}`, `{{sender.name}}`, `{{account.name}}`,
  `{{opportunity.name}}` — an explicit list, mirroring how the workflow engine
  allowlists comparable condition fields. An allowlist of fields every role
  can already read raises no visibility question, so this rung needs no new
  model.
- [ ] **§5.3 — arbitrary field interpolation is blocked** on the
  field-visibility model named in `docs/WORKFLOW_AUTOMATION.md` § *Named open
  items* and in `internal/api/field_security.go`. Do not ship it before that
  model exists. Custom fields specifically stay out, for the same
  definition-versioning reason conditions exclude them: an archived or retyped
  field turns a working template into one that silently renders wrong.
- [ ] Web: a template picker in the §3 composer and a Settings → Templates
  management page. Unresolved merge fields render as an explicit visible
  placeholder, never as an empty string — a message reading "Hi ," is worse
  than one that refuses to send.

**Acceptance criteria.** A template renders identically in preview and in the
handed-off message. An unknown or unresolvable merge field is refused at save
time, not discovered at send time. No template can render a field its renderer
is not authorized to read.

---

## §6 — Messaging actions in the workflow engine

**What Privyr does.** Auto-send a personalised introduction when a lead
arrives; auto follow-up on a schedule.

**What we have.** The engine, its ledger, its retry policy, and its loop
prevention are all built and documented. Actions are `create_task`,
`add_activity`, `notify`. There is no send action.

**Depends on §2, §3, §5.** Do not start earlier.

- [ ] New action type `send_message` in `internal/domain/workflow.go`, added to
  the allowlisted catalog so `DescribeWorkflowCatalog` surfaces it and the
  Automation UI cannot compose a rule the server would refuse — the existing
  invariant.
- [ ] The action re-reads its subject under the execution's own tenant scope
  before acting, like every other action. `internal/integration/workflow/actions.go`
  documents why in a comment worth re-reading before touching it: an execution
  can run minutes after its event, and the record may have been reassigned or
  deleted.
- [ ] The action calls `MayMessage` (§2) and fails **terminally** — not with a
  retry — when consent is absent. A retry cannot make consent appear, and
  `ReasonActionFailed` already exists as a bounded code; add
  `ReasonNotPermittedToMessage` alongside it.
- [ ] `AssertTriggersCannotLoop` must still pass: no trigger may be an event
  that a `send_message` action produces. Check it, do not assume it — that
  assertion runs at startup for exactly this reason.
- [ ] Quiet hours (**D6**) are enforced by deferring the execution, not by
  dropping it. A follow-up that vanishes because it was due at 2am is a bug
  the ledger will be blamed for.
- [ ] Time-based triggers ("follow up 3 days after no reply") remain out of
  scope — they need the scheduler already named as an open item. `create_task`
  with a due date is the existing answer and is good enough.

**Acceptance criteria.** A rule that sends is versioned, ledgered, retryable,
loop-free, consent-gated and tenant-isolated exactly as the existing actions
are. Replaying an outbox event does not send a second message.

---

## §7 — Bulk and campaign sending

**Verdict: defer, and reconsider whether to build at all.**

Privyr's bulk WhatsApp campaigns are the feature most specific to its market
(solo agents blasting a contact list) and the least appropriate for a
multi-tenant B2B CRM: it is the highest-risk surface for platform bans, spam
complaints, and regulatory exposure, and one tenant's behaviour damages a
shared sender reputation for all of them.

If it is taken up anyway:

- [ ] Requires §2, §4, **D3**, **D6**, and a documented legal review.
- [ ] Per-tenant and per-recipient rate ceilings enforced server-side, plus a
  global provider-level ceiling. A tenant must not be able to exhaust a shared
  quota.
- [ ] Every recipient individually consent-checked at send time, not at
  campaign-composition time. The gap between the two is where the violation
  lives.
- [ ] Per-recipient outcome reporting in the existing bulk-operation shape
  (bounded, per-item authorized, per-item success/failure).
- [ ] A campaign is cancellable mid-flight, and cancellation is honoured by
  the poller before each send rather than only at enqueue.

---

## §8 — Automatic lead distribution

**What Privyr does.** New leads are automatically assigned across team members.

**What we have.** `BulkReassignLeadOwner` and `BulkReassignAccountOwner` —
manual, after the fact. The workflow engine can create a task assigned to
`record_owner` or a `fixed_user`, but cannot *set* an owner, and has no notion
of a rotation.

**Verdict: build.** It is small, entirely internal, needs no provider, no
consent, no external decision — and it is the one Privyr feature that is
genuinely a team-CRM capability rather than a solo-agent one.

- [ ] `migrations/00XX_assignment_rules.sql` — tenant-scoped rules: an ordered
  member list, a strategy (`round_robin` initially), optional conditions reusing
  the workflow condition vocabulary rather than a second one, and a durable
  rotation cursor.
- [ ] A new `assign_owner` workflow action, so distribution is a rule on
  `lead.created` rather than a parallel engine. **This is the whole design
  decision:** we already have a versioned, ledgered, idempotent, loop-checked
  rule engine — a second assignment mechanism beside it would need all of that
  again.
- [ ] The rotation cursor advances inside the same transaction that claims the
  execution row, so at-least-once delivery cannot hand two leads to the same
  member or skip one.
- [ ] Skip members who are inactive or lack `crm:lead:write`. Assigning work to
  someone who cannot open it is a silent failure.
- [ ] Web: an Automation-screen rule editor, reusing the existing catalog-driven
  form so an author cannot compose a rule the server would refuse.

**Acceptance criteria.** Ten leads created concurrently across a three-member
rotation distribute 4/3/3 with no duplicates and no gaps. A redelivered event
does not re-assign. A rotation containing an ineligible member skips them and
records why in the ledger.

**Verification.** A concurrent-creation test asserting the distribution, in the
style of the existing contiguous-stage-history-under-concurrent-advances test.

---

## §9 — Trackable file sharing

**What Privyr does.** Share a PDF as a tracked link and see when the recipient
opens it.

**Verdict: defer.** Blocked on **D5**, which is the same object-storage
decision already blocking `task.md` §5.2 attachments. Building tracked sharing
before ordinary attachments exist is out of order.

When unblocked:

- [ ] Attachments first (`task.md` §5.2), then tracking on top. Do not build a
  second storage path for tracked files.
- [ ] A tracked link is an unguessable token resolving to `(tenant, file,
  recipient)` — the same shape as §1's endpoint token, resolved the same way.
- [ ] Opens are recorded as activities on the recipient's timeline. Record
  first-open and open-count; do not attempt read-duration or scroll depth.
- [ ] **A privacy notice is mandatory, not optional.** The recipient is being
  tracked. Several jurisdictions require disclosure, and the tenant is the
  data controller. This needs the same explicit treatment the synced-mail
  privacy boundary got.
- [ ] Revocation must be immediate and must not depend on a CDN cache.

---

## §10 — Mobile speed-to-lead

**What Privyr does.** A native mobile app with push notifications for new
leads, and one-tap actions on every record.

**What we actually have** (correcting the stale ROADMAP text): a responsive
web app. The shell is an off-canvas drawer below `lg`, detail lists and paired
form fields stack below `sm`, tables scroll horizontally, the viewport meta is
present, and axe-core runs as a static a11y floor over every page, panel and
fallback state.

**What is genuinely missing:**

### 10.1 — Installability and offline shell
- [ ] Web app manifest and icons (`apps/web/public/` does not exist yet).
- [ ] A service worker caching the app shell only. **Do not build offline data
  sync.** Offline mutation of a multi-tenant CRM with optimistic concurrency is
  a distributed-systems project, and the existing conflict handling assumes an
  online round-trip.
- [ ] Verified installable on iOS Safari and Android Chrome.

### 10.2 — Push notification for a new lead
- [ ] Requires a push provider decision (Web Push/VAPID is the no-vendor
  option and works everywhere except older iOS).
- [ ] Reuses the existing notification entity and the `notify` workflow action;
  push is a **delivery channel** for a notification we already create, not a
  new notification system. The bell already polls — this makes it arrive when
  the app is closed, which is the entire point for speed-to-lead.
- [ ] Subscription storage is tenant- and owner-scoped, per-device, revocable,
  and purged on sign-out.

### 10.3 — Mobile verification, not mobile assumption
- [ ] A `Mobile Chrome` project in `apps/web/playwright.config.ts` — today the
  only browser project is `chromium` on `devices["Desktop Chrome"]` (beside the
  `setup` auth project), so no test has ever rendered this app at a phone
  width. The responsive work is currently protected by review and jsdom, and
  jsdom has no layout.
- [ ] The §3 click-to-chat journey covered at a mobile viewport end to end.
- [ ] Close the browser-tier gap `task.md` already names as open: axe against
  the running app for contrast and target size, which cannot be judged in
  jsdom. Tap targets are exactly what a phone-first workflow gets wrong.

### 10.4 — Native app
- [ ] **Do not build.** A native iOS/Android client means two more codebases,
  two store review pipelines, a mobile release train, and a second auth
  implementation — against a PWA that reaches the same tap-to-call,
  tap-to-WhatsApp and push capabilities. Revisit only if a specific capability
  the web cannot reach (contact-book sync, call-log capture) is shown to be
  required by a real customer, not anticipated.

---

## §11 — Ad-platform conversion feedback (Meta CAPI)

**What Privyr does.** Reports lead quality and conversions back to Meta so ad
delivery optimizes toward leads that actually close.

**Verdict: defer.** Meaningless before §1 exists — there is nothing to attribute
a conversion to without capture. It is also the only item in this file that
sends customer PII *outbound to an ad platform*, which makes it a **D3**
question with sharper edges than the rest.

When taken up:

- [ ] Depends on §1 storing the provider's click/lead identifier on the lead at
  capture time. Without it there is nothing to match on, and it cannot be
  backfilled.
- [ ] Fires on `opportunity.closed_won` — an outbox event that already exists.
- [ ] PII is SHA-256 hashed before transmission, per Meta's requirement.
- [ ] Per-tenant opt-in, off by default, with an explicit disclosure of what
  leaves the system. A tenant must never discover after the fact that their
  customer data was sent to an ad platform.

---

## Sequencing

Strictly ordered by dependency and by value per unit of risk:

1. **§1 Lead capture** — no external dependency, heads the value chain.
2. **§2 Channels and consent** — small, and gates everything after it.
3. **§3 Click-to-chat** + **§5.1/5.2 templates** — most of Privyr's felt value
   at a fraction of §4's cost.
4. **§8 Lead distribution** — self-contained, real team-CRM capability.
5. **§10.1–10.3 PWA, push, mobile verification** — makes §1 and §3 actually fast.
6. **§6 Messaging automation** — once §2, §3, §5 are proven.
7. Everything else stays behind its decision: **§4** (D2/D3/D4), **§7**
   (legal), **§9** (D5), **§11** (D3), **§10.4** (do not build).

Steps 1–5 close the gap that matters and require **one** product-owner decision
(**D1**). Steps 6–7 require the rest.

## Definition of done for this file

- [ ] An external lead source delivers a lead into the correct tenant, and the
  existing automation fires on it unchanged.
- [ ] A rep opens a lead on a phone, taps once, and is in WhatsApp with a
  personalised message; the timeline records the attempt honestly.
- [ ] Consent is recorded, withdrawable, durable, auditable, and enforced by a
  single gate every send path must pass — proven by a totality test, not by
  code review.
- [ ] New leads distribute across a team automatically, exactly once each.
- [ ] The app is installable, notifies on a new lead when closed, and its
  mobile journey is covered by a mobile-viewport browser test.
- [ ] Nothing above weakened tenant isolation, the deny-by-default scope
  policy, the append-only migration rule, or the credential-sealing boundary.

---

*Skipped deliberately: WhatsApp BSP integration (§4), bulk campaigns (§7),
file open-tracking (§9), Meta CAPI (§11), and a native mobile app (§10.4). Add
§4 when §3's measured usage justifies the provider contract; §7 only after
legal review; §9 when object storage has an owner; §11 after §1 ships; §10.4
only for a capability the web genuinely cannot reach.*
