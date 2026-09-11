# Bootstrap and account onboarding

Savvy CRM owns authentication in the Go CRM API and stores accounts,
organizations, memberships, challenges, and sessions in CRM Postgres. There is
no separate identity database or password-based administrator account.

## First local workspace

Copy `.env.example` to the ignored `.env`, then configure:

```dotenv
AUTH_BOOTSTRAP_ORG_NAME=Savvy CRM
AUTH_BOOTSTRAP_EMAIL=support@savvyadvi.com
# Optional: preserve an existing tenant UUID.
AUTH_BOOTSTRAP_ORG_ID=
```

Set the SMTP fields and a random `OTP_PEPPER`, then run:

```sh
make up
```

The CRM migration container applies the auth schema before the API starts.
Startup creates the organization, active user, and `crm_admin` membership
idempotently. It never creates or stores an application password. Re-running
with the same email and organization ID is safe.

For an explicit one-off invocation:

```sh
AUTH_BOOTSTRAP_ORG_NAME='Acme CRM' \
AUTH_BOOTSTRAP_EMAIL='owner@example.com' \
make bootstrap
```

`AUTH_BOOTSTRAP_EMAIL` also identifies the passwordless Savvy Super Admin. In
the Savvy deployment this value is fixed to `support@savvyadvi.com`. The email
alone grants nothing: the user must still have an active account, protected
anchor membership, and valid session. Its session receives only the
`platform_admin` role and no tenant CRM scopes. New customer workspaces should
be created from `/admin` or verified signup, not by changing startup variables.

Migration `0038_protect_super_admin.sql` protects the Savvy support identity,
its administrator anchor membership, and its home organization from ordinary
SQL deletion or deactivation. It also blocks moving or downgrading the anchor
and adding the identity to another customer organization. Replacing this
recovery identity therefore requires an intentional operator migration and a
matching protected configuration change; it is not an application UI action.

## Preserving an existing CRM tenant

CRM business rows use the organization UUID as `tenant_id`. When replacing a
previous identity system, set `AUTH_BOOTSTRAP_ORG_ID` to that same UUID before
the first native-auth startup. This creates the new membership without moving
or rewriting CRM rows.

Never generate a different organization ID for an existing tenant with data.
Check the distinct tenant IDs first and perform any multi-tenant identity
migration with an explicit mapping and backup.

## Login and signup

- Login request: `POST /auth/email/request` with an existing email and
  `flow: "login"`.
- Signup request: the same endpoint with `flow: "signup"`.
- Verification: `POST /auth/email/verify` with the returned challenge ID,
  six-digit code, and signup profile when applicable.
- Session: `GET /auth/session`.
- Logout: `POST /auth/logout`.

Login responses are account-enumeration safe. Signup creates the organization,
user, `crm_admin` membership, onboarding profile, and session in one transaction
only after verification.

## Roles and tenant safety

The first member of a signup-created organization receives `crm_admin`.
Administrators assign invited members one of the fixed CRM roles server-side:
`crm_admin`, `crm_manager`, `crm_member`, or `crm_viewer`.
Job title and team size are onboarding attributes, never authorization input.

`crm_admin` can manage only its own organization and members. The Savvy Super
Admin sees a dedicated platform console instead of Accounts, Contacts, Leads,
Opportunities, Tasks, Calendar, Automation, or tenant configuration. It can
review every customer registration and current plan, create organizations, assign
Free / Pro / Enterprise plans, activate or deactivate customer organizations,
and manage membership across them. The protected login and its internal
session-anchor organization are excluded from customer users, seats,
organizations, and plan totals. Administrative mutations are recorded in
`auth_admin_audit`; the API prevents removal of the last active organization
administrator and separately protects the Super Admin itself.

Every protected API request resolves the active membership from the session.
The resulting organization ID binds PostgreSQL row-level security before any
CRM query executes.

## Local origins

The normal local endpoints are:

- CRM API: `http://localhost:19431`
- CRM web app: `http://localhost:3003` (or `CRM_WEB_PORT`)
- Marketing preview: `http://localhost:4173`

Use the same hostname consistently. Browser origins are exact, so
`localhost` and `127.0.0.1` are different CORS entries. Production must use
HTTPS, Secure cookies, and separate exact allowlists: the application origin
for the full CRM API, and the marketing origin only for public auth/onboarding
routes.

## Automated tests

`AUTH_DEV_OTP` may supply one deterministic six-digit code only when
`APP_ENV=test`. Development, staging, and production reject it. Real use must
generate a random code and deliver it through configured SMTP.
