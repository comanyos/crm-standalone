# Privyr comparison and prioritized gap list

Reviewed: 2026-09-10

This is the current comparison note for Privyr-style speed-to-lead parity. It
uses the implemented code and focused verification as the source of truth, not
the older unchecked implementation sketches in `task_crm.md`.

Official Privyr references reviewed:

- https://help.privyr.com/knowledge-base/integrations/
- https://help.privyr.com/knowledge-base/facebook-integrations/
- https://help.privyr.com/knowledge-base/using-whatsapp-business-automations-with-privyr/
- https://help.privyr.com/knowledge-base/privyr-sequences/
- https://www.privyr.com/pricing/

## Executive conclusion

This CRM is stronger than Privyr as a conventional multi-tenant B2B CRM: it
has Accounts, Contacts, Leads, Opportunities, configurable pipelines, atomic
lead conversion, custom fields and objects, tasks, activities, Gmail and
Calendar synchronization, workflows, RBAC, tenant isolation, and audit-focused
data handling.

The material gap is Privyr's acquisition-and-engagement loop:

> native lead source -> instant alert and assignment -> WhatsApp response ->
> timed follow-up -> engagement tracking -> conversion feedback

## Comparison snapshot

| Capability | Current CRM status |
| --- | --- |
| Accounts, contacts, leads, opportunities and pipelines | Implemented |
| Custom fields, custom objects, tags and role-based access | Implemented |
| Gmail and Google Calendar synchronization | Implemented; real-provider verification remains |
| Generic signed inbound lead endpoint | Implemented; default person-first mapping and atomic retry path verified |
| Native Facebook and Instagram Lead Ads | Product code implemented; Meta app approval/credentials and live test-lead evidence remain external gates |
| Native Google Ads, TikTok, LinkedIn and WordPress lead connectors | Not implemented; a bridge may call the generic endpoint |
| Person-first lead with name and phone | Implemented across contract, persistence, capture, dedupe, conversion, templates and UI |
| Instant app-push and email lead alerts | Not implemented; in-app notifications poll every 60 seconds |
| Round-robin assignment | Implemented |
| Weighted/custom assignment and external lead forwarding | Partial/not implemented |
| One-tap WhatsApp, SMS, call and email | Implemented for Contacts and Leads with consent gating; physical iOS/Android verification remains |
| WhatsApp contact import and inbound conversations | Not implemented |
| WhatsApp auto-responder, replies and delivery/read receipts | Not implemented |
| Scheduler-backed follow-up sequences | Not implemented |
| Text message templates | Implemented |
| File/page templates, branded links and view tracking | Not implemented |
| Bulk calling, bulk messaging and campaigns | Not implemented |
| Timeline, notes, tasks and activities | Implemented |
| Timeline attachments and geolocation | Not implemented |
| Phonebook and lead/contact CSV import | Not implemented; Accounts CSV only |
| Automatic lead/contact merging | Not implemented; duplicate flags and Account merge exist |
| Mobile application | Responsive PWA shell is partial; no push or native app |
| Response-time, source, representative and campaign analytics | Not implemented; basic CRM reporting exists |
| Meta Conversions API | Not implemented |

## Curated backlog

### P0 - required before paid-ad traffic

Repository implementation status as of 10 September 2026:

- **Complete in code and deployed to Azure beta release
  `beta-20260911-009`:** items 1–3; item 4 through the provider-ready connector
  and automated contract/webhook tests; item 5 through responsive web/PWA
  handoff.
- **External acceptance still required:** approved Meta production app and
  permissions, a controlled Facebook test lead, a controlled Instagram test
  lead, and physical iPhone/Android WhatsApp handoff evidence. Paid traffic is
  not approved until those checks are recorded.

1. Make Lead person-first. Add first name, last name and E.164 phone/WhatsApp
   number; make email and company optional. Update conversion, deduplication,
   forms, capture mappings, templates, filters and API contracts together.
2. Repair generic capture's advertised default path. The UI initially maps
   only `email -> contact_email`, while Lead validation requires company; the
   documented example also omits company. A default endpoint therefore cannot
   create its example Lead without extra configuration.
3. Make capture delivery claiming, Lead creation, delivery-to-Lead linking and
   `lead.created` publication atomic or recoverable. A process crash after the
   delivery claim can otherwise leave a delivery marked created without a Lead.
4. Build one native Meta connector for both Facebook and Instagram Lead Ads:
   OAuth and encrypted token lifecycle, Business/Page/Form selection, webhook
   verification and subscription, Meta signature validation, Graph lead
   retrieval, historical import, attribution, reconnection/health UI and live
   test-lead verification.
5. Put the consent-gated WhatsApp/SMS/call/email actions on Lead detail and run
   the captured-Lead-to-WhatsApp journey on real iOS and Android devices.

### P1 - speed-to-lead experience

1. Add immediate push and email alerts plus configurable uncontacted reminders.
2. Ship a safe default automation preset:
   `capture -> assign owner -> notify -> create task -> prepare response`.
3. Add scheduler-backed manual follow-up sequences with delays, due/overdue
   queues, enrollment, pause/resume and escalation.
4. Extend routing with weighted rules, source/Page/Form criteria, an unassigned
   claim pool and optional forwarding to non-CRM recipients.
5. Complete real Google OAuth, Gmail-send and mobile/PWA smoke verification.

### P2 - official WhatsApp integration

1. Connect a WhatsApp Business/Cloud API provider or BSP with per-user or
   per-team number ownership and sealed credentials.
2. Add inbound conversation/contact import, chat-to-Lead creation, replies,
   delivery/read receipts and timeline synchronization.
3. Add the approved-template lifecycle, customer-service-window handling,
   automatic opt-out processing, quiet hours and rate ceilings.
4. Add new-lead auto-response and scheduler-backed WhatsApp sequences that stop
   on a reply or representative takeover.

This phase is blocked until provider/cost ownership, consent and retention,
production KMS ownership, quiet hours, rate policy and erasure behaviour are
documented.

### P3 - engagement and optimization

1. File/page templates, attachment storage, branded links and view tracking.
2. Lead/contact import, phonebook import, richer filters/segments and controlled
   lead/contact merge.
3. Response-time, lead-source, representative, channel and campaign analytics.
4. Meta Conversions API after native Meta capture and attribution are reliable.
5. Additional native lead sources in demand order: Google Ads Forms, then
   TikTok, LinkedIn and WordPress/form providers.

Native mobile applications and bulk WhatsApp campaigns remain deferred until
the P0/P1 loop is proven and the corresponding commercial/legal owners exist.

## Recommended delivery order

1. Person-first Lead model.
2. Reliable generic capture.
3. Native Facebook/Instagram connector.
4. Instant alerts and Lead-level WhatsApp handoff.
5. Scheduled follow-up sequences.
6. Official WhatsApp conversations and automation.
7. Content tracking, advanced analytics and Meta CAPI.

## Product name

The selected product name is **Savvy CRM**. It is applied to the customer-facing
web UI, PWA manifest and icons, platform manifest, OAuth display name, calendar
exports, and primary repository documentation. Compatibility identifiers such
as `crm-web`, `crm:*`, protobuf service names, event subjects, and environment
variables deliberately remain unchanged.
