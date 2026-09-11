# Savvy CRM Flutter mobile architecture

**Internal engineering reference**
**Targets:** iOS and Android
**Status:** approved product direction; implementation not started

## Outcome

Build one focused Savvy CRM mobile product in Flutter. It should share API contracts and business truth with the existing web application, while using native-quality authentication, secure storage, notifications, deep links, and release processes on each platform.

The mobile app is not intended to reproduce every desktop administration screen. Version one should make the daily sales loop exceptionally fast:

```text
Alert → open lead → understand context → call/message → record outcome → set next action
```

The responsive PWA remains the full-coverage fallback for administration, uncommon customisations, and features an older mobile version cannot safely render.

## Current backend fit

- The authoritative service contract is `services/crm-api/proto/crm/v1/crm.proto`.
- The web client already uses generated Connect clients; mobile should generate Dart clients from the same protobuf contract.
- `services/crm-api` now owns first-party authentication: passwordless email OTP, Google account sign-in, opaque revocable sessions, and local user/organization/membership records.
- CRM authorization is server-side through scopes and tenant isolation; mobile must not reproduce authorization policy as its security boundary.
- Optimistic concurrency uses server record versions/timestamps. Mobile conflicts must refetch and make the difference visible.
- The notifications list in the CRM is canonical. Native push is only a transport that opens an authorized record.

### Backend gaps that must precede the app

1. Add dedicated native session issuance for both email OTP and Google sign-in. Current web completion is cookie-only; mobile needs a one-time exchange that returns an opaque Bearer token without ever putting it in a URL.
2. Define organization selection when a user belongs to more than one active organization; the current minimal flow selects the first membership.
3. Add `RegisterDevice` and `UnregisterDevice` APIs and a durable push dispatcher.
4. Add person name and phone to Lead so the mobile speed-to-lead flow is real.
5. Add idempotency keys before any generic offline write queue is allowed.
6. Provide a versioned mobile capability/metadata manifest for dynamic/custom fields and supported actions.

## Proposed repository structure

```text
apps/mobile/
  android/
  ios/
  lib/
    main_staging.dart
    main_production.dart
    src/
      app/
        bootstrap.dart
        router.dart
        theme.dart
      core/
        config/
        auth/
        api/
          gen/
          interceptors/
          errors/
        database/
        notifications/
        telemetry/
      features/
        my_day/
        leads/
        contacts/
        tasks/
        opportunities/
        inbox/
        settings/
      shared/
        models/
        widgets/
  test/
  integration_test/
  tool/
  buf.gen.yaml
  pubspec.yaml
  analysis_options.yaml
```

Follow Flutter’s [architecture guide](https://docs.flutter.dev/app-architecture/guide) and [architecture recommendations](https://docs.flutter.dev/app-architecture/recommendations):

- Views render state and forward intent.
- `ChangeNotifier` ViewModels own presentation state and commands.
- Repositories are the source of truth for each feature.
- Services wrap Connect, authentication, database, push, links, and other platform APIs.
- Use-case classes exist only for compound cross-repository operations such as lead conversion.
- Use `provider` for dependency injection and `go_router` for navigation/deep links unless a spike proves a concrete limitation.

## Generated API client

Add Dart generation without hand-written duplicate DTOs:

```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/dart
    out: lib/src/core/api/gen
  - remote: buf.build/connectrpc/dart
    out: lib/src/core/api/gen
```

Implementation rules:

- Pin generator versions before checking in the template.
- Commit generated Dart code so release builds do not depend on a remote generator.
- CI regenerates and fails when the worktree differs.
- Use Connect-Dart; do not maintain a parallel REST model layer.
- Preserve server pagination through `page_token`.
- Generate a small hand-written domain adapter layer only where UI-friendly types differ from protobuf types.

Client interceptors should provide:

- bearer-token injection from the active session;
- W3C `traceparent` propagation;
- one serialized session-expiry/sign-out transition shared by concurrent requests;
- normalized unauthenticated, forbidden, unavailable, timeout, validation, and conflict errors;
- retry only for safe/idempotent calls;
- request IDs useful for support without logging PII.

Official Connect references: [getting started](https://connectrpc.com/docs/dart/getting-started/), [code generation](https://connectrpc.com/docs/dart/generating-code/), [clients](https://connectrpc.com/docs/dart/using-clients/), and [interceptors](https://connectrpc.com/docs/dart/interceptors/).

## Authentication and tenant discovery

The mobile app uses the native auth surface in `services/crm-api`; it is not an OAuth client of a separate Savvy identity provider. Email OTP is a direct JSON flow. Google sign-in opens the CRM API's start endpoint in the system browser; the server remains Google's confidential client, keeps the client secret and PKCE verifier, validates Google state/nonce, and returns to the app through a short-lived one-time handoff. Never embed Google login in a WebView or ship the Google client secret.

### Mobile flow contract

1. Email: reuse `POST /auth/email/request`, collect the six-digit code, then complete it through a dedicated native endpoint with the challenge ID and signup profile when required. The existing `POST /auth/email/verify` is the cookie-only web completion contract and must not be treated as mobile token issuance.
2. Google: open `GET /auth/google/start` in the system browser. After the server callback validates Google, it redirects to an exact claimed HTTPS App/Universal Link carrying only a single-use, short-lived exchange code.
3. The native email completion or Google exchange returns a high-entropy opaque CRM session token exactly once over TLS. Both issuance contracts are required backend work; do not put the session token itself in a redirect URL.
4. Store the opaque token in OS secure storage and send it as `Authorization: Bearer <token>` to CRM API requests.
5. The server resolves the user, active session, organization, role, and scopes on each request. Mobile UI checks improve usability but are not an authorization boundary.

### Redirect links

Prefer claimed HTTPS links after the application domain is live:

- Android App Links: [Flutter setup](https://docs.flutter.dev/cookbook/navigation/set-up-app-links)
- iOS Universal Links through `apple-app-site-association`
- Flutter [deep-link guidance](https://docs.flutter.dev/ui/navigation/deep-linking)

A reverse-domain private scheme is acceptable only for an early internal build with an exact redirect registration and the same one-time exchange; claimed HTTPS links remain the release requirement.

### Session handling

- Store one opaque CRM session token in iOS Keychain / Android Keystore through [`flutter_secure_storage`](https://pub.dev/packages/flutter_secure_storage); keep only the active copy in memory while unlocked.
- The token has no client-readable claims and there is no access/refresh JWT pair. Load identity, organization, role, scopes, and expiry from `GET /auth/session`.
- On expiry or revocation, stop concurrent retries, clear the token, wipe the tenant cache, and require fresh email/Google authentication.
- Logout calls `POST /auth/logout`, waits for server revocation, clears secure storage and memory, and wipes tenant cache.
- Never log session tokens, one-time exchange codes, OTPs, cookies, phone numbers, emails, or provider payloads.

### Tenant discovery decision

The native service derives `org_id` from an active membership rather than accepting it from the client. Before supporting multiple memberships per user, choose one:

1. **Invite/workspace link for MVP:** a signed invite or HTTPS app link supplies a non-secret workspace handle and creates/selects the intended membership.
2. **Authenticated chooser for general release:** after identity verification, a rate-limited endpoint returns only that user's active organizations and rotates the session when one is selected.

Do not enumerate organizations by arbitrary email without anti-abuse controls.

## Navigation and feature slices

Recommended root tabs:

1. **My Day** — new leads, untouched leads, overdue work, due now, and upcoming tasks.
2. **Leads** — inbox, saved filters, search, lead detail, source, owner, timeline, and next action.
3. **Pipeline** — compact opportunity list/board and guarded stage transitions.
4. **Notifications** — canonical notification inbox.
5. **More** — contacts, accounts, templates, settings, support, and session controls.

The first release must make these actions reachable without desktop navigation:

- call, SMS, email, and WhatsApp;
- add note;
- create/complete/reopen task;
- assign owner when authorized;
- change lead status;
- create/advance opportunity;
- inspect timeline;
- open a push deep link to the correct tenant record.

External channel actions must be recorded as **attempted/opened**, not falsely marked delivered or replied. Confirmed delivery requires provider evidence.

## Offline and local data

Do not promise “the complete CRM offline” in version one. Flutter’s [offline-first guidance](https://docs.flutter.dev/app-architecture/design-patterns/offline-first) supports the safer first boundary: cached reads with online-only writes.

### Version-one policy

- Cache only recent, explicitly permitted Leads, Contacts, Tasks, Opportunities, lightweight Accounts, and lookup metadata.
- Partition the cache by environment, user subject, and tenant ID.
- Encrypt the database; holding its key in Keychain/Keystore is mandatory.
- Show last-sync time and stale/offline state.
- Write changes online and update the cache only after a confirmed server response.
- On conflict/Connect `Aborted`, refetch and show compare/reapply rather than silently overwriting.
- Purge the full cache on logout, organization change, account removal, or remote wipe.
- Exclude Gmail bodies, raw lead deliveries, exports, OAuth credentials, admin/security records, and full WhatsApp payloads from the first cache.

Drift is a reasonable typed SQLite layer, but plain Drift is not encryption. Run a package/security spike for SQLCipher or another maintained audited encrypted store before selection.

### Later offline writes

Only queue writes after the backend supports:

- idempotency keys;
- optimistic record version;
- authorization re-check at replay time;
- explicit pending/sent/failed/conflict state;
- ordered dependencies;
- retry/discard UI;
- record deletion and tenant-revocation handling.

Start with offline note/task drafts. Do not introduce a generic mutation queue first.

## Push notifications

Use Firebase Cloud Messaging as transport for both Android and iOS. Firebase still routes Apple delivery through APNs, so Apple keys and capabilities remain required.

Backend record:

```text
device_id
tenant_id
user_subject
platform
app_environment
push_token (encrypted/protected)
locale
app_version
last_seen_at
invalidated_at
```

Rules:

- Register/unregister through caller-scoped APIs.
- Handle push token rotation.
- Dispatch from the transactional outbox or durable `notification.created` events.
- Payload contains only opaque notification ID, record type, record ID, and route intent—no lead name, phone, email, message text, or deal value.
- Fetch authorized details after app open.
- Support foreground, background, terminated-app, and tap paths.
- Enforce tenant and role authorization again when resolving the link.
- Push delivery failure never deletes the canonical in-app notification.

References: [FCM setup for Flutter](https://firebase.google.com/docs/cloud-messaging/flutter/get-started) and [receiving messages](https://firebase.google.com/docs/cloud-messaging/flutter/receive-messages).

## Server-driven capability and metadata contract

Frappe’s official retrospective says its Flutter mobile experiment was discontinued partly because browser Client Script customisations could not transfer and the app added too little beyond responsive web. Savvy must solve that boundary explicitly rather than promise full parity: [Frappe Mobile announcement and update](https://frappe.io/blog/announcements/introducing-frappe-mobile).

Add a versioned capability response containing:

- server/product version and minimum supported mobile version;
- object and field IDs, type, label, required/read-only state;
- role/field visibility and editability;
- validation constraints and enum/options;
- supported mobile renderer;
- relationships and allowed actions;
- enabled modules/integrations;
- feature flags with safe older-client fallback.

The app must hide unsupported actions and show a clear “Open in web” fallback. Never execute tenant-supplied JavaScript inside Flutter.

## Testing strategy

### Unit

- protobuf/domain mapping;
- pagination and query composition;
- session expiry/revocation fan-out across concurrent requests;
- cache partitioning, expiry, and wipe;
- conflict mapping and safe retry classification;
- push/deep-link route parsing;
- capability-version fallback.

### Widget and accessibility

- loading, empty, error, stale, offline, forbidden, and conflict states;
- role-specific actions;
- dynamic text, screen readers, focus, contrast, tap targets, and reduced motion;
- narrow/large phones and text scaling.

### Contract and integration

- Buf lint/breaking checks and generated-diff check;
- live CRM API with native auth enabled;
- two-tenant isolation and all four built-in CRM roles;
- email OTP and Google login, expiry, logout, revocation, 401/403, conflict, and network recovery;
- Meta test lead arriving through routing, alert, mobile open, and WhatsApp handoff;
- real Gmail/Calendar connection only in controlled provider test tenants.

### Device

- physical iPhone and representative Android hardware;
- foreground/background/terminated push;
- Universal/App Links;
- call, SMS, email, and WhatsApp handoffs;
- biometric/device-lock interaction with secure storage;
- poor network, clock skew, process death, upgrade, and reinstall.

Flutter integration tests cannot fully drive every system-browser OAuth or native permission screen. Maintain platform automation plus a physical-device release checklist.

Release references: [testing overview](https://docs.flutter.dev/testing/overview), [integration tests](https://docs.flutter.dev/testing/integration-tests), [accessibility testing](https://docs.flutter.dev/ui/accessibility/accessibility-testing), [Android release](https://docs.flutter.dev/deployment/android), [iOS release](https://docs.flutter.dev/deployment/ios), and [continuous delivery](https://docs.flutter.dev/deployment/cd).

## Environments and release controls

Create fully separate staging and production values for:

- bundle/application IDs;
- display name and app icon marker;
- CRM API base URL and exact mobile handoff links;
- Google account-login client and server callback values (server-side configuration only);
- Firebase projects/configuration;
- APNs keys/topics;
- App/Universal Link domains;
- crash/analytics project;
- signing and store tracks.

CI gates:

1. formatting and `flutter analyze`;
2. unit and widget tests;
3. generated-contract drift check;
4. integration tests against an isolated stack;
5. Android debug build and unsigned iOS build;
6. dependency/license/security scan;
7. signed artifacts only in protected release jobs;
8. TestFlight and Play internal/closed track before staged production rollout.

## Delivery milestones

| Milestone | Scope | Indicative effort after decisions |
|---|---|---|
| M0 — backend/product enablement | One-time mobile session handoff, membership selection, Dart generation spike, secure-storage threat model, FCM/APNs, links | 1–2 weeks |
| M1 — mobile foundation | Flutter workspace, flavors, generated client, email/Google auth, session/logout handling, router, theme, telemetry, CI | 1–2 weeks |
| M2 — speed-to-lead MVP | My Day, leads, contacts/search, tasks, push, call/SMS/email/WhatsApp handoff | 3–4 weeks |
| M3 — revenue workflow | Accounts, opportunities, pipeline transitions, notes/timeline, templates, notification inbox | 2–3 weeks |
| M4 — resilience | Encrypted cached reads, stale state, cache wipe, conflict UX, recovery testing | about 2 weeks |
| M5 — beta and stores | Accessibility, performance, security review, TestFlight, Play closed test, disclosures, staged release | about 2 weeks |

Estimates exclude unresolved provider approvals, account verification, and App Store/Play review time.

## Accounts and decisions

Required:

- Apple Developer organization, App Store Connect, bundle IDs, signing team, and APNs key;
- Google Play Console organization, permanent application ID, and Play App Signing;
- separate Firebase/GCP projects for staging and production;
- owned HTTPS domain for support/privacy and association files;
- separate Google account-login registrations and exact server/mobile callback links for staging and production;
- two test tenants and users covering administrator, manager, member, and viewer;
- physical iPhone and Android devices.

Decide before M1:

- invite link versus workspace discovery;
- claimed HTTPS redirect versus temporary private scheme;
- login lifetime, idle timeout, and biometric re-lock;
- minimum OS versions, tablet scope, and languages;
- cached fields, retention, encryption implementation, and wipe triggers;
- push categories, quiet hours, and no-PII policy;
- crash/product analytics vendor, consent, residency, and PII scrubbing;
- WhatsApp app handoff versus official Business Platform integration.

The complete provider account matrix is in [THIRD_PARTY_ACCOUNT_TEST_PLAN.md](THIRD_PARTY_ACCOUNT_TEST_PLAN.md).
