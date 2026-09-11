# Security policy

## Supported versions

This project has not made a tagged release yet. Until the first release, only
the `main` branch is supported and receives security fixes. Once releases
begin, this section will list which released versions receive security
patches (generally the latest minor release of the current major version).

| Version | Supported |
| --- | --- |
| `main` (pre-release) | Yes |

## Reporting a vulnerability

If you discover a security vulnerability in this repository (in
`services/crm-api`, `apps/web`, deployment manifests,
or documentation that would lead to a credential or data exposure):

1. **Do not open a public GitHub issue.**
2. Report it privately to the repository owner. Until a dedicated security
   contact address is published for this project, use GitHub's private
   [security advisory](../../security/advisories/new) feature on this
   repository so the report and any discussion stay private until a fix
   ships.
3. Include: affected component/path, reproduction steps or proof of concept,
   impact (e.g., cross-tenant data access, authentication bypass, credential
   exposure), and any suggested remediation.

We will acknowledge new reports as soon as possible and aim to provide an
initial assessment within 5 business days. Coordinated disclosure is
appreciated: please allow time for a fix to be developed and released before
any public disclosure.

## Scope

In scope: authentication/authorization/tenant-isolation flaws, injection
vulnerabilities, credential or secret exposure, and anything that could let
one tenant organization access another tenant's data. Out of scope until the
relevant product decisions are made (see `task.md`): issues specific to
integrations, providers, or infrastructure that have not yet been implemented
(e.g., email/calendar sync, production hosting).

## Known, tracked gaps

This repository is under active development toward the completion criteria in
[task.md](task.md). Known security-relevant gaps that are already tracked
there (not new reports) include: native email-OTP and Google-login negative
paths, opaque-session revocation/expiry tests, production configuration
validation (rejecting development OTPs and insecure cookies), and the full
Phase 10 security hardening pass. Please still
report anything you find that is not already reflected in `task.md`.
