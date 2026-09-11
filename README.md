# Savvy CRM

This repository contains the standalone Savvy CRM product. Its Go API owns a
small, product-specific authentication layer instead of depending on a
separate identity platform.

## Services

- `services/crm-api` — the standalone CRM backend and its migrations.
- `apps/web` — the CRM browser application. See [apps/web/README.md](apps/web/README.md).
- `packages/contracts` — generated Connect-ES client for `services/crm-api`'s
  proto contracts.

## Product and launch references

- [Savvy CRM product blueprint](docs/SAVVY_CRM_PRODUCT_BLUEPRINT.md)
- [Production application architecture](docs/PRODUCTION_ARCHITECTURE.md)
- [Flutter mobile architecture](docs/FLUTTER_MOBILE_ARCHITECTURE.md)
- [Third-party account and acceptance-test plan](docs/THIRD_PARTY_ACCOUNT_TEST_PLAN.md)
- [Privyr-specific comparison](docs/PRIVYR_COMPARISON.md)
- [Organization-code access and approval](docs/ORGANIZATION_ACCESS.md)

## Local development

1. Copy `.env.example` to the ignored `.env`.
2. Set `OTP_PEPPER`, the SMTP values, and the initial
   `AUTH_BOOTSTRAP_ORG_NAME` / `AUTH_BOOTSTRAP_EMAIL` values.
3. Run `make up`. The migration runner applies the CRM and native-auth schema;
   startup idempotently creates the requested first workspace administrator.
4. Check the CRM API at http://localhost:19431 and the browser app at
   `CRM_WEB_ORIGIN` (http://localhost:3003 by default).

The marketing preview at `http://localhost:4173` owns the combined Login /
Sign-up dialog. An unauthenticated app visit redirects there and returns to the
app after the API sets its HttpOnly session cookie; the app does not render a
second login page or receive a token in the URL.

### First organization bootstrap

Create the first organization and administrator membership idempotently. There is no
application password: the administrator signs in using a one-time email code
or a configured Google identity.

```sh
AUTH_BOOTSTRAP_ORG_NAME='Acme CRM' \
AUTH_BOOTSTRAP_EMAIL='admin@example.com' \
make bootstrap
```

Use `AUTH_BOOTSTRAP_ORG_ID` when importing existing CRM rows whose tenant ID
must remain stable. In the Savvy deployment, `support@savvyadvi.com` is the
protected, platform-only Super Admin. It can manage customer organizations,
plans, and access, but receives no tenant CRM scopes and cannot be deleted,
disabled, moved, or downgraded through the API or ordinary database writes. It
does not bypass passwordless login or session checks. See
[docs/BOOTSTRAP.md](docs/BOOTSTRAP.md).

For encrypted local logical backups, destructive restore safeguards, and the
migration/rollback policy, see [docs/BACKUP_RESTORE.md](docs/BACKUP_RESTORE.md).

### Web app

`make up` builds and serves the CRM browser application persistently at
`CRM_WEB_ORIGIN`. It uses the CRM API's HttpOnly session cookie and never
stores an access or refresh token in browser storage.

For Vite hot-reload component development instead, use the ignored
`apps/web/.env` and run `npm run dev --workspace=@crm/web`. See
[apps/web/README.md](apps/web/README.md) for matching a custom local port with
the OAuth redirect and CORS configuration.

## Safety boundary

The original CompanyOS directories are provenance only. Never symlink them,
mount them into containers, or use them as build contexts. Future work happens
only in this repository.

## License

Licensed under the [Apache License 2.0](LICENSE).
