# Backup, restore, and upgrade runbook

This runbook provides encrypted logical backups and isolated local restore
verification for the standalone CRM's two PostgreSQL databases. It is not a
production backup service: managed encrypted backups, point-in-time recovery,
cross-region replication, retention enforcement, and a production restore
drill require a selected platform and accountable operators.

## Backup scope and prerequisites

Back up both databases as one consistency window:

- `authincore`: identities, sessions, OAuth/OIDC state, RBAC, and AuthinCore
  configuration.
- `crm`: tenant CRM records, pipelines, tasks/notes, and the transactional
  outbox.

The backup tool requires [age](https://age-encryption.org/) and an operator
owned recipient public key. It refuses to write a plaintext dump. Backups
contain customer data; store the resulting `*.sql.gz.age` files in an access
controlled, encrypted location outside this repository. `deploy/backups/` is
ignored only as a convenience for local testing, not as an approved store.

```sh
mkdir -p deploy/backups
BACKUP_OUTPUT_DIR="$PWD/deploy/backups" \
BACKUP_AGE_RECIPIENT='age1…recipient-public-key…' \
./scripts/backup-compose.sh
```

Record the UTC timestamp, image digests, migration versions, archive hashes,
and intended retention/deletion date with the backup. The script deliberately
does not print database URLs, passwords, rows, or archive contents.

## Restore verification

Never restore over a shared or production database with this script. Start an
isolated Compose environment or disposable test database, stop application
writers, and use a separately stored age identity file:

```sh
RESTORE_CONFIRM=RESTORE_LOCAL_COMPOSE_DATABASES \
RESTORE_AUTH_ARCHIVE=/secure/path/authincore-YYYYMMDDTHHMMSSZ.sql.gz.age \
RESTORE_CRM_ARCHIVE=/secure/path/crm-YYYYMMDDTHHMMSSZ.sql.gz.age \
BACKUP_AGE_IDENTITY_FILE=/secure/path/age-identity.txt \
./scripts/restore-compose.sh
```

The restore script works in two phases. It first decrypts every archive to a
private staging directory and checks that each one decompresses and carries a
plain-format `pg_dump` header, and that the roles the dump's `GRANT`
statements reference (`crm_app`, `crm_drain`) exist in the target cluster.
Only then does it drop and recreate the `public` schema in **both** target
databases and restore. Nothing is destroyed until every input is known good,
so a wrong `BACKUP_AGE_IDENTITY_FILE` or a truncated archive fails with the
databases untouched. It also has an exact confirmation string to prevent a
pasted command from silently erasing the local stack.

Backups keep their `GRANT` statements. `crm_app` and `crm_drain` receive their
privileges from `migrations/0003_rls_enforce.sql`, and a dump taken with
`--no-privileges` restores a database the CRM cannot read — which looks like a
successful restore until the first request fails. Roles themselves are
cluster-global and are not carried in a logical dump, which is why the restore
checks for them up front rather than failing part-way through.

### Verifying the scripts themselves

`./scripts/test-backup-restore.sh` runs the whole round trip — seed, back up,
destroy, restore, assert — against a throwaway PostgreSQL cluster it creates
and removes itself. It needs no Docker and touches no development database,
and it runs in CI. Use it after any change to either script.

After restore:

1. Run the migration runners; migrations are forward-only and append-only.
2. Start AuthinCore and CRM with the same (or an intentionally compatible)
   signing-key ring. A database backup does not replace signing-key recovery.
3. Check `/healthz` and `/readyz` for both services, sign in with a known test
   account, refresh a token, validate CRM API access, and verify tenant
   isolation/outbox behavior with the focused tests.
4. Record achieved recovery time and data timestamp. Do not claim RPO/RTO
   compliance until an owner has measured a production-like drill.

## Migration and rollback policy

Migrations must follow expand/contract:

1. **Expand:** add nullable columns, new tables/indexes, or dual-write code in
   a backward-compatible release. Never modify a shipped migration.
2. **Migrate:** run tracked forward migrations before moving application
   traffic. Verify `auth-migrate` and `crm-migrate` completion plus readiness.
3. **Contract:** remove obsolete reads/writes only in a later release after
   every running version is compatible and data backfill is verified.

Application rollback means deploying the previous immutable image digest only
while all applied migrations remain backward-compatible. Do not run automatic
down-migrations. A destructive schema rollback requires an approved restore
from a verified backup into an isolated environment, followed by a deliberate
cutover decision.

## Production decisions still required

Before this can become a production capability, an owner must select the
database provider and regions, encryption/KMS ownership, PITR/WAL retention,
backup retention/deletion policy, cross-region copy policy, storage access
roles, RPO/RTO objectives, restore approvers, and an incident escalation path.
Those choices are deliberately not invented by this repository.
