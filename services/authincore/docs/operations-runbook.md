# AuthinCore Operations Runbook

## Service objectives

- Availability: 99.95% monthly for login and token endpoints.
- Latency: p95 login under 500 ms; p95 machine token issuance under 300 ms.
- Recovery: PostgreSQL RPO 5 minutes and RTO 60 minutes. Signing-key RPO 0;
  the active/previous key ring must be stored in SecretCore/KMS and backed up
  with database credentials.

## Backup and restore

1. Enable encrypted PostgreSQL point-in-time recovery with continuous WAL
   archiving and a daily full backup. Retain 35 days and copy to a second region.
2. Back up the SecretCore entries for `RSA_PRIVATE_KEYS_JSON`, `JWT_KEY_ID`, and
   `JWT_PREVIOUS_KEY_ID`. Never export unencrypted key material to an operator laptop.
3. Redis is disposable. Losing it invalidates rate-limit/lockout transient state;
   authentication endpoints fail closed until Redis is healthy.
4. Quarterly, restore PostgreSQL into an isolated namespace, populate the key-ring
   Secret, run migrations, start two AuthinCore replicas, and exercise registration,
   login, refresh, JWKS validation, SCIM, and logout. Record achieved RPO/RTO.

## Dependency incidents

- PostgreSQL failure: readiness becomes unhealthy; stop traffic, fail over or
  restore, then verify migration status and command/event atomicity.
- Redis failure: login/recovery/service-token issuance returns 503 by design.
  Restore Redis before relaxing any protection.
- Signing key issue: keep the previous key published for the maximum access-token
  lifetime. Change the shared active key ID once in SecretCore and roll pods.
- Provider failure: check upstream latency/errors without logging codes, tokens,
  assertions, passwords, or identity traits.

## Required exercises

Run `test/chaos/chaos-mesh.yaml` in staging after every infrastructure change.
Run both k6 scripts before a release and attach the raw result, pod CPU/memory,
and PostgreSQL/Redis saturation charts to the release evidence.
