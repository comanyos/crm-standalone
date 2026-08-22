# AuthinCore Release and Rollback

1. Run the complete CI workflow, production-like integration tests, supported
   federation certification, load test, and disaster-recovery exercise.
2. Update the chart and application versions together and create a signed `vX.Y.Z`
   tag. The release workflow publishes multi-architecture OSS, enterprise, and
   outpost images with SBOM/provenance attestations and keyless signatures.
3. Deploy by immutable image digest. Run the migration init container before
   traffic moves to the new pods. Verify readiness, JWKS active/previous keys,
   login, token refresh, SCIM, SAML/OIDC, and service-account issuance.
4. Roll back application pods to the previous digest when migrations are
   backward-compatible. Database down-migrations require an approved backup and
   restore plan; never automatically reverse destructive schema changes.
5. Keep the previous signing key in the SecretCore key ring for at least the
   maximum access-token TTL. Remove it only after metrics show no use.
