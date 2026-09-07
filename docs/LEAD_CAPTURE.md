# Inbound lead capture — v1

`task_crm.md` §1. Written before the implementation, in the shape
`docs/CSV_IMPORT_EXPORT.md` set: the decisions first, so the narrowings are
named rather than discovered.

## What it does

An external system — a website form, an ad platform, a Zapier step — POSTs a
lead to a URL this CRM issues, and a Lead appears in the right tenant within a
request. That is the head of the whole speed-to-lead chain: every other item
in `task_crm.md` is downstream of a lead arriving.

## The decision this answers

`task_crm.md` lists **D1** as blocking: *are unauthenticated inbound webhook
endpoints acceptable on the CRM API surface, and behind what?*

This implements the conservative answer, which is the one a security review
would arrive at anyway:

- Unauthenticated, because the caller is a third-party server that cannot hold
  a CRM session. This is the second such route, after the Google OAuth
  callback, and it is built from the same parts.
- Behind a **required HMAC-SHA256 signature** over the raw body. There is no
  "unsigned" mode, not even for testing: an endpoint that accepts unsigned
  posts is an open lead-injection hole for anybody who learns the URL, and a
  mode that exists is a mode somebody leaves on.
- Behind an **unguessable endpoint token** in the path, which is also how the
  tenant is resolved. Enumerating endpoints is not possible.
- Behind a **body size cap and a per-endpoint rate limit**, so a delivery
  cannot become a memory or storage exhaustion vector.

If the product owner's answer to D1 turns out to be stricter — an IP allowlist,
a WAF, mutual TLS — those sit in front of this and none of them replaces it.

## How a delivery is authorized

There is no principal, so three things stand in for one, and each is
independent:

1. **The endpoint token.** 32 bytes of `crypto/rand`, unique across all
   tenants below RLS, carried in the path. It is what makes the tenant
   resolvable at all, and it is not a secret in the cryptographic sense — it
   appears in URLs and logs of the systems that call it, which is exactly why
   it is not sufficient on its own.
2. **The signature.** HMAC-SHA256 over the exact bytes received, using a
   secret that is generated server-side, shown once at creation, and stored
   sealed. Compared with `hmac.Equal`, never with `==`.
3. **The dedup key.** A provider's own event id, or a hash of the normalised
   body. It does not authorize anything, but it makes a replayed delivery a
   no-op rather than a second lead — and replay is the ordinary case, not the
   attack: every webhook provider retries.

## Tenant resolution

Identical in shape to the Google OAuth callback, and for the same reason: the
request names no tenant and must not be able to.

The endpoint token is resolved on the **drain pool**, in one query that
returns identifiers only — the tenant, the endpoint id, and the sealed secret
needed to check the signature. Every read and write after that runs on the
application pool with that tenant bound, under RLS. `crm_drain` is granted
`SELECT` on `lead_capture_endpoints` and nothing else added by this feature;
it is never granted on the deliveries table, which holds submitted personal
data.

## What reaches the caller

Almost nothing, and that is deliberate. The caller is unauthenticated, so
every distinguishable response is an oracle:

| Situation | Response |
| --- | --- |
| Valid, new | `202 Accepted` |
| Valid, already delivered | `202 Accepted` — a retry must look like a success, or providers retry forever |
| Bad signature | `202 Accepted`, recorded as rejected |
| Unknown endpoint token | `202 Accepted` |
| Disabled endpoint | `202 Accepted`, recorded as rejected |
| Body too large | `413` |
| Wrong method | `405` |

An unknown token and a bad signature are answered identically, so the response
cannot be used to test whether a token is real. Nothing from the request body
is echoed. The reason lives in the delivery log and the server log, where the
tenant can see it and an attacker cannot.

`202` rather than `200`: the lead is created synchronously today, but the
contract this states is "accepted for processing", which is what lets the
creation move to a worker later without every integration needing rewiring.

## Field mapping

The stored `field_mapping` maps a provider's field names onto Lead fields,
using the same vocabulary as CSV import (`docs/CSV_IMPORT_EXPORT.md`) rather
than a second one. Unmapped fields are kept on the delivery row rather than
dropped — "the lead came in but the phone number is missing" is the first
support question this feature will generate, and it is unanswerable if the
original payload is gone.

## What a captured lead is

An ordinary Lead. It emits the existing `lead.created` outbox event, so the
workflow engine — including the round-robin distribution in `task_crm.md` §8 —
fires on it with no change to any of them. **This is the whole point of the
design.** A "captured lead" that were its own kind of record would need every
downstream feature taught about it.

Duplicate detection uses the existing lead dedup flags: a captured lead
matching an existing contact is flagged, never merged automatically, exactly
as one typed in by hand would be.

## Named narrowings

These are decisions, not omissions.

- **One source kind: `generic`.** A documented HMAC-SHA256 contract that any
  system can meet, including Zapier and a plain HTML form behind a small
  server-side proxy. Per-provider adapters (Meta's `X-Hub-Signature-256`,
  TikTok, LinkedIn) are configuration of an endpoint that already works, and
  will be added when a provider's payload actually refuses to fit — not
  speculatively.
- **Synchronous.** The lead is created inside the request. Bounded by the body
  cap and one insert, so it is fast; but a slow database is a slow response
  to the provider. Moving it behind the outbox is the upgrade path and the
  `202` already permits it.
- **No retry queue on our side.** If lead creation fails, the delivery is
  recorded as failed and the provider's own retry is what recovers it. Every
  webhook provider has one; building a second is duplicating theirs.
- **Rate limiting is per endpoint and in-process.** Adequate for one instance
  and honest about it: a horizontally scaled deployment needs a shared
  counter. Marked with a `ponytail:` comment at the implementation.

## Setting one up

1. Settings → Lead capture → New endpoint. Choose an owner and a name.
2. Copy the URL and the signing secret. **The secret is shown once.** It is
   stored sealed and cannot be read back — only rotated.
3. Configure the caller to POST JSON with an `X-CRM-Signature` header
   containing `sha256=` followed by the lowercase hex HMAC-SHA256 of the exact
   request body, keyed with the secret.
4. Send a test delivery and check Settings → Lead capture → the endpoint's
   delivery log. Every delivery is there with its outcome, including the
   rejected ones — that log is this feature's debugger, and without it every
   failure is a support call.

### Worked example

```
POST /integrations/lead-capture/<endpoint token>
Content-Type: application/json
X-CRM-Signature: sha256=<hex hmac of the body>

{"email":"alex@example.com","full_name":"Alex Morgan","company":"Example Ltd"}
```

```bash
BODY='{"email":"alex@example.com","full_name":"Alex Morgan"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $2}')
curl -X POST "$URL" -H "Content-Type: application/json" \
     -H "X-CRM-Signature: sha256=$SIG" -d "$BODY"
```

## Authorization of the administration surface

Creating an endpoint is tenant-wide authority to create records, so it sits
behind its own `crm:capture:read` / `crm:capture:write` scope pair, granted to
administrators and managers only. A member who can create a lead by hand still
cannot mint a URL that lets the whole internet create them.
