# Savvy CRM third-party account and acceptance-test plan

**Internal reference — never place credentials in this repository**
**Snapshot:** 11 September 2026

This is the curated list of external accounts, assets, approvals, devices, and acceptance evidence required before Savvy CRM can claim each integration in production.

## Summary matrix

| Area | Account/assets required | Used for | Current evidence | Launch gate |
|---|---|---|---|---|
| AWS | Production AWS account, Route 53 zone, ACM, S3, CloudFront, deploy-role OIDC | Marketing site and later application infrastructure | Local architecture/IaC only | Exact domain, account/region owner, least-privilege deploy role, DNS/TLS and rollback test |
| Azure | Azure subscription, VM/container runtime, Key Vault and custom-domain validation | Current canonical Savvy CRM beta host | Release `beta-20260911-010` is deployed; pre-release backup `crm-20260911T115221Z.dump`, migration 0040, immutable image digests, container readiness and all three public domains were verified | Retain rollback/restore evidence and complete capacity and cost acceptance before GA |
| GitHub | Organization/repository and protected production environment | CI/CD and AWS OIDC | Workflow template only | Branch protection, environment approver, restricted OIDC trust, no long-lived AWS key |
| Google Cloud | Project, OAuth consent screen, separate account-login and Gmail/Calendar OAuth clients, test users | Google account login plus Gmail and Calendar connection | `savvyadvi-crm-prod` has Gmail, Calendar and People APIs enabled; account-login client is live, while the separate Gmail/Calendar web client is prepared but still needs console creation and Key Vault installation | Complete real callback/login/logout/session tests, then connect/reconnect/revoke, integration-token rotation, Gmail sync/send, Calendar sync, tenant isolation, and Google verification plan |
| Google Workspace | Controlled Gmail + Calendar mailboxes in at least two tenants | Real provider data and deletion tests | Pending | Dedicated non-personal test mailboxes, seeded messages/events, erasure and resync evidence |
| Meta developer | Meta developer account/app and required products/permissions | Facebook and Instagram Lead Ads, later WhatsApp/CAPI | Native OAuth/webhook/import/attribution connector and automated provider-contract tests implemented; no production app grant yet | App review/permissions, Key Vault credentials, webhook subscription, provider test lead and live-form evidence |
| Meta Business | Business portfolio, Facebook Page, ad account, Instant Form | Facebook lead flow and attribution | Business → Page → Form selection implemented and tenant-isolated; controlled assets not connected | Page/form subscription, admin loss, duplicate/retry, campaign/ad/form attribution |
| Instagram | Professional account connected to the test Facebook Page and lead form | Instagram Lead Ads | Shared native ingestion path implemented; no Instagram-origin provider evidence | Instagram-origin test lead, attribution, disconnect/reconnect, tenant isolation |
| WhatsApp Business | WABA, test sender/number, Meta app/system-user setup, approved templates | Cloud API messages and inbound conversations | Only external app handoff concepts exist | Provider/cost decision, consent, templates, 24-hour window, STOP, receipts/replies, quiet hours and rate limits |
| Firebase | Separate staging and production projects, FCM configuration | Android and iOS push transport | Backend/device registration not built | Device token lifecycle, foreground/background/terminated delivery, no-PII payload, deep-link authorization |
| Apple | Apple Developer organization, App Store Connect, identifiers, signing, APNs key, test devices | iOS build, push, Universal Links, TestFlight/App Store | Flutter app not created | Company enrollment, protected signing, privacy disclosures, TestFlight, physical-device checklist |
| Google Play | Play Console organization, application ID, Play App Signing, test devices/tracks | Android build and Play release | Flutter app not created | Company enrollment, internal/closed testing, data-safety form, signing/rollback checklist |
| Email delivery | Production SMTP/transactional email account and verified sending domain | Passwordless email OTP, other auth emails, and CRM notifications | Local SMTP values and `Savvy CRM <support@savvyadvi.com>` sender are configured; production deliverability/DNS acceptance is not yet proven | OTP expiry/single use/attempt limits, encrypted SMTP, SPF, DKIM, DMARC, bounce/complaint handling, suppression, rate limits, recovery ownership |
| SMS provider | Provider decision and test sender if CRM-originated SMS is approved | Native SMS delivery; not needed for `sms:` handoff | No provider selected | Legal/commercial approval, sender registration, STOP/HELP, delivery receipts, quiet hours, spend caps |
| Error/analytics | Vendor decision or AWS-native-only posture | Crash, product analytics, traces, logs | OpenTelemetry/Prometheus code exists | Consent, retention, residency, PII scrubbing, tenant-safe labels, access and incident owner |

## 1. AWS and domain

### Required

- A Savvy-owned AWS production account; use a separate staging account where practical.
- Confirmed public hosted zone for the actual company domain.
- Exact hostnames:
  - marketing/auth dialog: `crm.savvyadvi.com`;
  - app: `crmapp.savvyadvi.com`;
  - API/auth callbacks: `crmapi.savvyadvi.com`;
- IAM administrator for initial infrastructure only.
- GitHub OIDC deploy role scoped to the exact repository/branch/environment.

### Acceptance

- S3 bucket is private, all public access blocked, ACLs disabled.
- CloudFront Origin Access Control is the only reader.
- HTTP redirects to HTTPS; certificate chain and renewal records are valid.
- A/AAAA aliases resolve globally.
- `/404.html` returns real 404; missing assets do not receive the home page.
- Security headers, compression, HTML revalidation, and asset caching are correct.
- Prior version can be restored without changing DNS.

Marketing architecture and CloudFormation live in `/Users/santoshguru/Desktop/savvy/savvy_crm_website`. The API (including `/auth/*`), worker, data, security, deployment, and recovery design is in [PRODUCTION_ARCHITECTURE.md](PRODUCTION_ARCHITECTURE.md).

## 2. Google Gmail and Calendar

### Accounts/assets

- Dedicated Google Cloud project for staging and another for production.
- OAuth consent screen with Savvy product/support/privacy URLs.
- Web OAuth client with exact redirect URIs.
- A separate Google account-login client whose confidential secret and callback are owned by `crm-api`; do not reuse its grant or scopes for Gmail/Calendar access.
- At least two controlled test users in separate Savvy CRM tenants.
- One mailbox/calendar with ordinary seed data and one sparse/empty account.

### Test matrix

- Google account login and signup: approve/deny/cancel, one-time state, nonce, server-held PKCE verifier, verified-email requirement, existing-account-only login, first-workspace signup, logout, expired/revoked opaque CRM session, and no cross-tenant membership selection;
- connect, deny, cancel, reconnect, disconnect, revoke in Google, and reconnect after revocation;
- expired access token and rotated refresh token;
- missing/reduced scopes and consent-screen changes;
- Gmail first sync, incremental sync, pagination, duplicate event, deleted message, draft/send, send failure, and bounce visibility where supported;
- Calendar first sync, incremental sync, changed/deleted events, recurrence/time zone, pagination, and partial failure;
- no cross-tenant mailbox/calendar visibility;
- contact suppression, privacy export, erasure, reconnect, and no accidental resurrection;
- rate limit, 429/backoff, provider outage, recovery, and health UI;
- secret/token encryption and redacted logs.

### Before production

- Complete Google OAuth verification if non-test users need sensitive/restricted scopes.
- Publish accurate privacy policy, data-use explanation, retention, and deletion path.
- Keep Gmail/Calendar test tenants free of real personal or customer data.

## 3. Meta: Facebook and Instagram Lead Ads

Savvy CRM now has one native Meta connector for Facebook and Instagram Instant
Forms. It converges on the same person-first Lead, outbox and consent behavior
as generic capture while retaining provider attribution and connection health.

### Accounts/assets

- Meta developer account and Savvy-owned app.
- Meta Business portfolio with verified business details when required.
- Test Facebook Page and Page admin/test users.
- Test ad account and at least two Instant Forms with different fields.
- Instagram Professional account connected to the Page.
- System user/token strategy for production operations where applicable.
- Public HTTPS webhook and privacy/data-deletion URLs.

### Permissions to validate

The code currently requests the following exact set; validate it again in the
Meta App dashboard during app review and whenever the pinned Graph version is
changed:

- `pages_show_list`
- `pages_read_engagement`
- `pages_manage_metadata`
- `leads_retrieval`
- `business_management`
- `ads_read`

`ads_read` is used only to resolve ad/ad-set/campaign labels. Savvy CRM does not
request an ads-write permission. The API pins Graph `v25.0`; changing that pin
requires rerunning the provider contract, webhook, import and attribution
matrix before release.

### Production endpoints and Azure secrets

- OAuth redirect:
  `https://crmapi.savvyadvi.com/integrations/meta/callback`
- Page webhook callback:
  `https://crmapi.savvyadvi.com/integrations/meta/webhook`
- Azure Key Vault secret names: `meta-app-id`, `meta-app-secret`,
  `meta-webhook-verify-token`, and `meta-token-key`.

Do not add any Meta secret to the marketing site, React build arguments,
repository, screenshots, or support chat. `meta-token-key` must be a new
base64-encoded 32-byte key, and the webhook verify token must be an independent
random value. A partial secret group deliberately prevents the API from
starting.

### Test matrix

- OAuth allow/deny/cancel and asset selection;
- Page, Instagram account, and Instant Form discovery;
- webhook verification challenge and Meta signature validation;
- provider test-lead tool and a controlled live-form lead;
- phone-only, email-only, optional company, custom fields, consent fields, and non-Latin text;
- stable provider-event deduplication and repeated delivery;
- notification event followed by full lead retrieval;
- campaign, ad set, ad, form, Page, and platform attribution;
- routing, owner alert, task/draft automation, lead-level WhatsApp action, and first-response timestamp;
- delayed/out-of-order event, retrieval 404, 429/backoff, token expiry, permission loss, Page admin removal, form deletion, and reconnect;
- two-tenant isolation and wrong-tenant signature/asset attempts;
- raw-payload retention, privacy export, erasure, and redacted logs.

### Launch evidence

- A saved event ID traces from Meta delivery through one visible Lead, one owner assignment, one alert, and one audit trail.
- Duplicate replay creates no second Lead.
- Token expiry is visible before silent data loss.
- A failed delivery can be retried safely.

Automated repository evidence covers OAuth state replay, permission refusal and
revocation, encrypted credential storage, token expiry/reconnect, asset
selection, subscription calls, HMAC validation, transient retry, duplicate
delivery/import overlap, person-first mapping, campaign attribution, tenant
isolation and transactional Lead/outbox creation. It does **not** replace the
Facebook/Instagram provider test leads or physical-device checks above.

## 4. WhatsApp Business

Treat these as two separate products.

### Stage A — external app handoff

No WABA is required for `wa.me`/app handoff. Acceptance still requires:

- normalized E.164 phone;
- consent/opt-out visibility;
- personalised but editable prefilled text;
- real iPhone and Android WhatsApp/WhatsApp Business handoff;
- an activity labelled attempted/opened, never delivered;
- URL encoding and missing/invalid phone behaviour.

### Stage B — official WhatsApp Business Platform

Required:

- decision: direct Meta Cloud API or approved BSP;
- Savvy-owned WABA, test phone number/sender, business profile, and payment method;
- production token/system user and rotation owner;
- approved utility/marketing/authentication templates as actually needed;
- published consent, privacy, support, and opt-out paths.

Acceptance:

- inbound text/media/status webhooks and signatures;
- correct tenant and lead/contact/conversation matching;
- agent reply within and outside the customer-service window;
- approved-template send, rejected template, variable validation, and language fallback;
- sent/delivered/read/failed/replied status transitions;
- STOP/opt-out immediately blocks queued and future outreach;
- quiet hours, rate limits, per-tenant spend caps, quality/rate-tier degradation, and provider outage;
- sequence stops on reply, opt-out, conversion, invalid number, or permanent failure;
- media security, malware/scanning decision, retention, export, erasure, and audit.

Do not enable automatic provider sending until owner, budget, legal basis, consent evidence, quiet hours, rate limits, and incident response are approved.

## 5. Apple, Google Play, Firebase, and physical devices

### Apple

- Enroll the company, not an individual, where possible.
- Create permanent bundle IDs for staging/production.
- Configure App Store Connect, APNs key, Associated Domains, privacy manifest/disclosures, and support/privacy URLs.
- Protect signing credentials in the release system.

### Android/Google Play

- Enroll the organization.
- Choose the permanent application ID before first upload.
- Enable Play App Signing and internal/closed tracks.
- Complete Data safety, content rating, privacy/support, and account-deletion declarations.

### Firebase

- Separate staging/production projects.
- Register exact iOS/Android apps.
- Keep service credentials server-side.
- Test token rotation, multiple devices, logout/unregister, revoked user, deleted tenant, and app upgrade.

### Device coverage

- Verify dedicated native email completion and Google app-link exchange issue
  an opaque CRM Bearer session exactly once, never in a URL; confirm expiry,
  revocation, Keychain/Keystore persistence, logout wipe, reinstall, and lost-
  device handling. This issuance path is not yet implemented.
- Current physical iPhone supported by the chosen minimum iOS.
- One current Android and one lower-memory/older supported Android.
- WhatsApp and WhatsApp Business installed in controlled profiles.
- Gmail/default mail, SMS, phone, Safari/Chrome, and notification permissions.
- Test with slow/offline transitions, background restrictions, battery saver, denied permissions, large text, screen reader, and device time changes.

## 6. Email and optional SMS delivery

### Transactional email

Choose a provider only after confirming region, support, price, deliverability features, and data-processing terms. Production passwordless authentication depends on this account; the marketing-site OTP interaction alone is not email delivery.

Account and configuration prerequisites:

- production and staging SMTP/transactional-email accounts or isolated provider environments;
- the existing `support@savvyadvi.com` mailbox and a provider-verified sending identity for that domain;
- an SMTP credential stored in the approved secret manager, with a named owner and recovery path;
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_TLS`, and `SMTP_REQUIRE_STARTTLS` injected into `crm-api`;
- `SMTP_FROM="Savvy CRM <support@savvyadvi.com>"`, matching the existing company website; verify it with the provider before sending;
- required STARTTLS on the explicit-TLS path (normally port `587`) or implicit TLS/SMTPS (normally port `465`), with no production plaintext fallback.

Provider selection, verification of the `savvyadvi.com` sending identity, credentials, and SPF/DKIM/DMARC DNS records are still pending external setup. Required acceptance:

- verified domain with SPF/DKIM and a deliberate DMARC policy;
- From-domain and envelope-sender alignment pass using the final Savvy sender;
- separated staging/production senders;
- bounce, complaint, suppression, unsubscribe, quota, and provider outage;
- no secrets or message bodies in logs;
- passwordless six-digit OTP templates render in supported clients and clearly identify expiry, device/time context, and what to do if the request was unexpected;
- OTPs are stored only as a verifier/hash, expire quickly, are single-use and purpose-bound, enforce attempt/resend limits, and are invalidated after successful session creation;
- login and resend responses do not disclose whether an email address already has an account;
- authentication and lead-alert templates render in supported clients;
- alert links open the correct tenant/record after authentication.

### CRM-originated SMS

One-tap `sms:` uses the user’s device and does not require an SMS provider. A server-side provider is a separate future decision requiring sender/telecom registration, STOP/HELP, receipts, quiet hours, rate limits, consent evidence, budget/spend caps, and country-specific compliance.

## 7. Ownership register

Complete before any real customer or paid-ad data enters the system:

| Responsibility | Named owner required |
|---|---|
| AWS account, DNS, certificates, budgets, and infrastructure incidents | Yes |
| OAuth/Meta/WhatsApp apps, tokens, app review, and provider incidents | Yes |
| Apple/Google/Firebase enrollment, signing, releases, and store compliance | Yes |
| Retention, erasure, consent, privacy policy, and data-subject requests | Yes |
| KMS/secrets rotation, access review, and break-glass procedure | Yes |
| Quiet hours, rate limits, message costs, templates, and automatic outreach | Yes |
| Backup/restore drills, monitoring, on-call, and customer communication | Yes |

## Credential rules

- Never put client secrets, app secrets, private keys, refresh tokens, access tokens, WABA tokens, APNs keys, Firebase service accounts, SMTP credentials, or AWS access keys in source, issues, screenshots, or test fixtures.
- Use separate staging and production credentials.
- Store production secrets in the approved secret manager with KMS ownership, access logs, and rotation dates.
- CI uses short-lived federation/OIDC wherever supported.
- Provider test fixtures contain synthetic people only.
- Record who owns each account, the recovery contact, MFA state, renewal/billing date, and revocation procedure outside the repository.
