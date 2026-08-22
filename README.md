# Independent CRM

This repository is an isolated CRM product assembled from source snapshots of
CompanyOS CRM and AuthinCore. It does not depend on, modify, or run from the
CompanyOS workspace.

## Services

- `services/crm-api` — the standalone CRM backend and its migrations.
- `services/authincore` — identity, OIDC, JWT, MFA, roles, and administration.
- `apps/web` — the CRM browser application. See [apps/web/README.md](apps/web/README.md)
  for setup — it needs the organization ID from the bootstrap step below.
- `packages/contracts` — generated Connect-ES client for `services/crm-api`'s
  proto contracts.
- `packages/auth-client` — OIDC Authorization Code + PKCE browser client used
  by `apps/web`.

## Local development

1. Copy .env.example to .env and set development-only values.
2. Generate a signing key with `make dev-key`.
3. Start the stack with `make up`. The tracked CRM migration runner applies
   pending SQL migrations before the API starts.
4. Check AuthinCore at http://localhost:18080/healthz, its admin UI at
   http://localhost:13001, CRM API at http://localhost:19431, and the CRM
   browser app at `CRM_WEB_ORIGIN` (http://localhost:3000 by default).

### First organization bootstrap

After the stack has migrated, create the first organization, its administrator,
the CRM roles/scopes, and the public CRM web client in one idempotent command.
Supply values through your shell or the ignored `.env` file; do not put a real
password in this repository.

```sh
BOOTSTRAP_ORG_NAME='Acme CRM' \
BOOTSTRAP_ORG_DOMAIN='localhost' \
BOOTSTRAP_ADMIN_EMAIL='admin@localhost' \
BOOTSTRAP_ADMIN_PASSWORD='use-a-unique-12-character-minimum-password' \
make bootstrap
```

The local web client is `crm-web`, with callback
`http://localhost:3000/auth/callback`. Override `CRM_WEB_CLIENT_ID` or
`CRM_WEB_REDIRECT_URI` only when the deployment URL changes. Re-running the
command reconciles the roles, grants and client; it never prints or stores the
administrator password and does not reset an existing administrator password.

The CRM API validates AuthinCore tokens using the internal JWKS endpoint while
requiring the public issuer http://localhost:18080.

For organization domain rules, staging/production DNS and TLS prerequisites,
and how to recover an administrator who cannot sign in, see
[docs/BOOTSTRAP.md](docs/BOOTSTRAP.md). Bootstrap deliberately never changes an
existing password; `make reset-admin-password` is the command that does.

For encrypted local logical backups, destructive restore safeguards, and the
migration/rollback policy, see [docs/BACKUP_RESTORE.md](docs/BACKUP_RESTORE.md).

### Web app

Set `CRM_WEB_AUTH_ORG_ID` in the ignored root `.env` to the organization ID
printed by bootstrap. `make up` builds and serves the CRM browser application
persistently at `CRM_WEB_ORIGIN`; it is no longer tied to a temporary Vite
development process.

For Vite hot-reload component development instead, use the ignored
`apps/web/.env` and run `npm run dev --workspace=@crm/web`. See
[apps/web/README.md](apps/web/README.md) for matching a custom local port with
the OAuth redirect and CORS configuration.

## Safety boundary

The original CompanyOS directories are provenance only. Never symlink them,
mount them into containers, or use them as build contexts. Future work happens
only in this repository.
