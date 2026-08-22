# Bootstrap and administrator recovery

How an operator brings up a new deployment, what an organization's domain
means, and how to recover an administrator who cannot sign in.

Related: [CONFIGURATION.md](CONFIGURATION.md) for the full environment matrix,
[../CONTRIBUTING.md](../CONTRIBUTING.md) for changing roles and scopes.

## What bootstrap does

`make bootstrap` runs `authinctl bootstrap` against AuthinCore's database. In
one transaction it reconciles:

| Thing | Reconciliation |
| --- | --- |
| Organization | Found by `primary_domain`; created if absent |
| Administrator identity + password credential | Found by email within the organization; **created only if absent** |
| CRM role model (`crm_admin`, `crm_manager`, `crm_member`, `crm_viewer`) and `owner` | Upserted, scopes overwritten from `packages/contracts/rbac/crm-roles.json` |
| Resources (`authincore:administration`, `crm:crm`) | Created if absent |
| Role grants for the administrator (`owner`, `crm_admin`) | Inserted if absent |
| `CRM Applications` project and the public `crm-web` OAuth client | Created if absent, otherwise reconciled |

It is idempotent: running it a second or third time inserts nothing new. That
is the point — re-running it is the supported way to apply a change to the
role model to an organization that already exists.

Two consequences worth knowing before you run it:

- **Scopes are overwritten, not merged.** If someone edited a built-in role's
  permissions by hand, the next bootstrap puts them back to what the shared
  contract says. Custom roles you created yourself are untouched.
- **The administrator's password is never changed.** See
  [Administrator recovery](#administrator-recovery) below.

### Required inputs

```bash
BOOTSTRAP_ORG_NAME='Acme' \
BOOTSTRAP_ORG_DOMAIN='localhost' \
BOOTSTRAP_ADMIN_EMAIL='admin@localhost' \
BOOTSTRAP_ADMIN_PASSWORD='change-me-now-please' \
make bootstrap
```

`BOOTSTRAP_ADMIN_NAME` defaults to the email address. `CRM_WEB_CLIENT_ID`
defaults to `crm-web` and `CRM_WEB_REDIRECT_URI` to
`http://localhost:3000/auth/callback`. The password must be at least 12
characters.

Bootstrap prints organization, administrator and client ids as JSON. It never
prints the password, a hash, a private key, or a client secret — there is a
test asserting that, because the output ends up in CI logs.

## Organization domain rules

An organization's `primary_domain` is its identity in AuthinCore. It is
matched by **exact equality** against the canonicalized host of an incoming
request, so what gets stored has to be exactly what a request will produce.
`tenancy.CanonicalDomain` enforces that at write time rather than letting an
unreachable organization be created:

- **A host, never a URL.** `acme.example` is valid;
  `https://acme.example/login` is refused. A scheme, path, query, fragment,
  embedded credentials or whitespace are all rejected rather than guessed at,
  because guessing which part was meant is how the wrong tenant gets stored.
- **Lowercased.** `ACME.test` is stored as `acme.test`.
- **A trailing dot is dropped.** `acme.test.` and `acme.test` are the same
  organization.
- **A port is stripped, not stored.** `localhost:8080` is stored as
  `localhost`, and matches requests arriving on any port. An organization
  therefore cannot be distinguished by port — two organizations on
  `localhost:3000` and `localhost:3001` are the same organization as far as
  domain resolution is concerned.
- **IP literals are accepted and normalized**, IPv6 included, so `::0001` and
  `::1` agree.
- **Label rules apply**: 253 characters overall, 63 per label, no empty
  labels, no leading or trailing hyphen.

Two organizations cannot share a domain — that is what makes the lookup
unambiguous. They *can* share an administrator's email address and a client
id; both are scoped per organization, and there is an integration test
asserting so, because a global unique constraint on either would make it
impossible for two customers to use one deployment.

## Local development

The default Compose stack serves everything on `localhost`, so the simplest
setup uses `localhost` as the organization domain:

```bash
make up
BOOTSTRAP_ORG_NAME='Acme' BOOTSTRAP_ORG_DOMAIN='localhost' \
BOOTSTRAP_ADMIN_EMAIL='admin@localhost' \
BOOTSTRAP_ADMIN_PASSWORD='change-me-now-please' \
make bootstrap
```

Then open the web app. **Mind the redirect URI.** The `crm-web` client is
provisioned for `http://localhost:3000/auth/callback` by default, and the web
container publishes on `${CRM_WEB_PORT:-3000}`. If you move the web app to a
different port — port 3000 is a common collision with other local stacks —
you have to move three things together, or sign-in fails with an invalid
redirect URI:

```bash
# in .env
CRM_WEB_PORT=3002
CRM_WEB_ORIGIN=http://localhost:3002
CRM_WEB_REDIRECT_URI=http://localhost:3002/auth/callback
```

Then re-run `make bootstrap`: the OAuth client is reconciled, so the new
redirect URI replaces the old one. `CRM_WEB_ORIGIN` also has to match, because
it is what AuthinCore's CORS allowlist is built from.

To simulate two organizations locally, give each a distinct host and point
both at the same stack — for example `acme.localhost` and `globex.localhost`,
which resolve to `127.0.0.1` on most systems without editing `/etc/hosts`.

## Staging and production prerequisites

Before bootstrapping a non-development deployment:

- **DNS.** Every organization domain you intend to store must resolve to the
  ingress in front of AuthinCore and the CRM. If you plan to give each
  customer a subdomain, a wildcard record (`*.crm.example.com`) avoids a DNS
  change per customer; a customer using their own domain needs a CNAME they
  control.
- **TLS.** AuthinCore's issuer, JWKS and CORS URLs must be `https` in
  staging and production — `config.Validate` refuses to start otherwise. A
  wildcard certificate covers a wildcard subdomain scheme; per-customer
  domains need certificates per domain (ACME/Let's Encrypt with HTTP-01 or
  DNS-01, issued at the ingress).
- **The domain you bootstrap with must be the domain browsers use.** Storing
  an internal hostname while users arrive on a public one produces an
  organization no request ever matches. If a proxy terminates TLS, make sure
  it forwards the original `Host`.
- **Secrets.** `BOOTSTRAP_ADMIN_PASSWORD` is read from the environment of the
  `make bootstrap` invocation. Supply it from your secret manager for that one
  command; do not put it in `.env`, and note that a shell that still has it
  exported is a shell whose history and environment now contain it.
- **The signing key must be persistent.** A development stack generates an
  ephemeral key; a deployment that regenerates its key on restart invalidates
  every issued token and every JWKS cache.

## Administrator recovery

Bootstrap **never** changes an existing administrator's password. That is
deliberate, and it is the reason recovery is a separate command:

Bootstrap is re-run routinely, and it reads `BOOTSTRAP_ADMIN_PASSWORD` from
the environment every time. If re-running it also reset the password, an
operator whose shell still had the original value exported would reset it
invisibly, and one whose shell did not would lock the administrator out of
their own deployment. Neither failure announces itself.

So changing a password is asked for by name:

```bash
RESET_ORG_DOMAIN='localhost' \
RESET_ADMIN_EMAIL='admin@localhost' \
RESET_ADMIN_CONFIRM='admin@localhost' \
RESET_ADMIN_PASSWORD='a-new-password-at-least-12-chars' \
make reset-admin-password
```

`RESET_ADMIN_CONFIRM` must equal `RESET_ADMIN_EMAIL`. It is the account's own
address rather than a bare `yes` so that pasting the command out of this
document without editing it cannot reset an account you did not mean. Case
does not have to match.

What the command guarantees:

- It **creates nothing.** An unknown organization or an unknown address is an
  error, not an invitation to provision a second administrator.
- It **replaces** the credential rather than adding one, so a reset never
  leaves a second way in that nobody knows about.
- It **writes an audit event** (`identity.password.reset`) naming the account
  and recording that an operator did it — never the password or its hash. A
  recovery that leaves no trace is indistinguishable from someone who
  obtained database access.
- It **touches nothing else**: roles, grants, resources, projects and the
  OAuth client are unchanged.

Repeated resets are supported; each appends its own audit event.

## Known issue: roles from an unrelated product

Every organization created in this deployment also receives three roles for
**DBCore** — a different product, whose source is not in this repository —
plus a `dbcore` resource, and the `owner` role is granted `dbcore:*` on it.
They come from an `AFTER INSERT` trigger on `orgs` installed by
`services/authincore/migrations/025_dbcore_role_scopes.sql`, which also
backfills organizations that already existed.

None of those roles carries any CRM scope, so they grant no access to this
product — there is a test asserting that
(`TestOrganizationsCarryRolesForAnUnrelatedProduct`). They are visible in an
administration UI listing roles, and they are noise in an audit of who can do
what.

Removing them means deleting authority rows from organizations that already
exist, which is a product decision rather than a cleanup. It is tracked
alongside the other DBCore provenance items in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
