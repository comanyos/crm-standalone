# Savvy CRM passwordless email delivery

**Operational note — 10 September 2026**

The customer-visible sender is:

```text
Savvy CRM <support@savvyadvi.com>
```

The domain is `savvyadvi.com` (there is no extra `d` after `savvy`). Never put
an SMTP password, Google app password, AWS secret, or OTP in this repository,
browser code, a screenshot, an issue, or chat.

## Current external state

- `savvyadvi.com` receives mail through Google Workspace.
- Google DKIM is published and DMARC is set to quarantine.
- The current SPF record authorizes GoDaddy sending infrastructure but does
  not list Google's SPF include. Reconcile SPF before relying on it for Google
  sending; verify the final message's actual DKIM/SPF/DMARC alignment.
- AWS SES in `ap-south-1` is enabled but remains in the sandbox.
- Neither `savvyadvi.com` nor `support@savvyadvi.com` is currently an SES
  verified identity.
- Local development has a private SMTP credential in the ignored `.env`. The
  CRM correctly attempted a real send, but this development host did not
  complete the connection to Google: port 465 accepted TCP and then timed out
  during the TLS handshake, while port 587 timed out during TCP connection.
  Delivery and credential acceptance therefore remain **unverified** until the
  API runs from a network with outbound SMTP egress. The credential is not
  present in source control or this document.

## Fastest real local test: Google Workspace SMTP

First confirm that `support@savvyadvi.com` is a licensed Google Workspace user
that can sign in. A website contact address, alias, or Google Group is not
automatically an SMTP login. If it is an alias, authenticate as the underlying
licensed user and configure an authorized **Send mail as** identity, or use the
Google Workspace SMTP relay.

For a login-capable mailbox, turn on 2-Step Verification and create a dedicated
Google app password for Savvy CRM. Put it only in the gitignored root `.env`:

```dotenv
SMTP_HOST=smtp.gmail.com
SMTP_PORT=465
SMTP_USERNAME=support@savvyadvi.com
SMTP_PASSWORD=<Google app password; local secret only>
SMTP_FROM="Savvy CRM <support@savvyadvi.com>"
SMTP_TLS=true
SMTP_REQUIRE_STARTTLS=false
```

Port 465 is configured for implicit TLS, which encrypts the connection from
the first byte. Port 587 is also supported by Google; when using it, set
`SMTP_TLS=false` and `SMTP_REQUIRE_STARTTLS=true`. The current development
network blocks both usable SMTP paths, so test from the deployment network (or
allow the required outbound egress) before treating delivery as complete.

Google's current Workspace guidance lists SMTP relay as the recommended option
for applications and also documents authenticated `smtp.gmail.com` on 465/587:

- <https://knowledge.workspace.google.com/admin/gmail/send-email-from-a-printer-scanner-or-app>
- <https://support.google.com/accounts/answer/185833>

After the secret is installed, recreate `crm-api` so its environment is
reloaded and use an **existing CRM user's** email in the marketing site's Login dialog. The
request must return a challenge ID, the code must arrive in the inbox, and
verification must create the native HttpOnly CRM session. Unknown login emails
receive the same generic browser response but no identity or session is
created.

## Production path: transactional provider

For production volume, bounce/complaint handling, suppression management, and
operational isolation, use a dedicated transactional provider. AWS SES fits the
planned AWS architecture, but it is not ready yet. Before switching production
traffic:

1. Verify `savvyadvi.com` as an SES domain in the deployment region and publish
   the generated DKIM records at the authoritative DNS provider.
2. Reconcile the single SPF record so every authorized sender is represented;
   do not publish a second SPF record.
3. Request SES production access with the real OTP use case, expected volume,
   bounce/complaint process, support URL, and incident owner.
4. Keep each recipient verified while the account is still sandboxed.
5. Create a least-privilege SES SMTP credential and inject it through the
   deployment secret manager. AWS API credentials are not SES SMTP credentials.
6. Test delivery, expiry, one-time use, resend/attempt limits, bounce,
   complaint, suppression, throttling, provider outage, and SPF/DKIM/DMARC
   alignment using a received message's headers.

## What is real now versus still gated

- The CRM API's native Go auth module generates, digests, stores, and verifies
  the passwordless code and sends it over encrypted SMTP.
- Login accepts only an existing active membership; it never creates an
  account implicitly.
- Signup uses a separate challenge purpose and creates the user, organization,
  owner membership, onboarding profile, and session atomically after verified
  email.
- Google account login is a separate identity-only OAuth client and must not
  reuse Gmail/Calendar integration permissions.
