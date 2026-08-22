# Federation certification harness

The scripts in this directory run black-box OIDC, SAML, and SCIM certification
against a deployed AuthinCore instance. Vendor credentials and tenant URLs must
come from CI secrets; no reusable provider secret is stored here.

Required certification targets for each release:

- Microsoft Entra ID: OIDC authorization-code + PKCE, SAML SP initiated login,
  signed response/assertion rejection cases, SCIM create/update/deactivate/group.
- Okta: the same OIDC, SAML, and SCIM matrix.

Attach the machine-readable result and vendor configuration version to release
evidence. A protocol unit test does not replace this external certification.
