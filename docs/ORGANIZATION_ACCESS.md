# Organization-code access

Savvy CRM supports two distinct onboarding paths:

- **Create a new organization** creates a workspace and makes the verified creator its CRM administrator.
- **Join an existing organization** accepts a Savvy organization code, verifies the applicant's email with OTP or Google, and creates a pending request. It never creates a membership by itself.

## Security boundary

The organization code identifies a workspace; it is not an access credential. Access begins only after an authenticated `crm_admin` for that workspace (or the protected platform administrator) approves the request and assigns one of the fixed server-owned CRM roles.

Codes contain 60 bits of cryptographic randomness, omit ambiguous characters, and are stored only as a domain-separated SHA-256 digest plus a four-character hint. The complete code is returned once when generated or rotated. Rotation immediately invalidates the previous code; administrators can also pause a code without changing it.

Email OTP limits and Google OAuth state/nonce/PKCE protections apply before a request can be submitted. One user can have only one unresolved request, repeated submissions are idempotent, and a pending applicant cannot create an accidental second workspace. Existing or disabled memberships are never silently reactivated by this flow.

## Administrator flow

1. Open **Platform admin / Organization administration → Organization access**.
2. Generate the organization code and copy the one-time value.
3. Share it through a trusted team channel.
4. Review the applicant under **Pending approval**.
5. Select `Administrator`, `Manager`, `Member`, or `Viewer`, then approve—or reject the request.

Every code change and decision is written to `auth_admin_audit`. If SMTP is configured, the applicant receives a best-effort decision email. A mail-provider failure does not roll back an already committed access decision.

## API surface

Public identity endpoints reuse the existing bounded auth surface:

- `POST /auth/email/request` with `flow: "join"`
- `POST /auth/email/verify` with `join: { firstName, lastName, organizationCode }`
- `GET /auth/google/start?flow=join&organizationCode=...`

Authenticated administration endpoints:

- `POST /auth/admin/organizations/{organizationID}/join-code` generates or rotates a code.
- `PATCH /auth/admin/organizations/{organizationID}/join-code` pauses or enables it.
- `GET /auth/admin/join-requests?organizationId=...` lists pending requests.
- `PATCH /auth/admin/join-requests/{requestID}` approves or rejects a request.

`AUTH_JOIN_RESULT_URL` is the fixed HTTPS marketing-site destination used after a Google join attempt. The API appends a bounded `joinStatus` value; the callback never accepts a redirect URL from the browser.
