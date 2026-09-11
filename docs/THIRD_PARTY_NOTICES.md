# License inventory and third-party notices

This is a manually assembled inventory, not a substitute for an automated
SBOM/license scan. Package licenses are based on published metadata and must be
re-verified before external distribution.

## This project's license

The repository root and `services/crm-api` carry Apache License 2.0 with the
project notice `Copyright 2026 CRM contributors`. The earlier copied Connect
example copyright line has been replaced. A legal/product owner must still
confirm that this is the intended grant before the first public distribution.

The former commercial license/tier package under `services/crm-api/internal/license`
was unrelated CompanyOS/CloudOps code. It and its flag-service calls were
removed before the current native-auth architecture.

## Removed AuthinCore component — historical notice

An AuthinCore source snapshot was originally extracted from CompanyOS and was
temporarily present under `services/authincore`. It has now been removed in
full and replaced by minimal first-party authentication inside
`services/crm-api`. AuthinCore is no longer part of this repository's current
source, build, runtime, dependency graph, container images, or distribution.

The removal also closes these earlier distribution findings for the current
tree:

1. AuthinCore had no standalone `LICENSE` file even though its README claimed
   MIT licensing.
2. It contained a commercial instance-license and feature-gating subsystem for
   SAML, SCIM, branding, SSO enforcement, and audit export. That subsystem had
   already been removed once during the audit, but the surrounding component
   still lacked clear provenance.
3. `migrations/025_dbcore_role_scopes.sql` installed unrelated DBCore roles,
   resources, scopes, and an organization trigger into every identity tenant.
4. `third_party/platform-contract` was an unlicensed vendored CompanyOS source
   snapshot referenced by a local Go `replace` directive.
5. Its additional admin/hosted UIs and Go dependencies were shipped only for
   that identity platform and are no longer applicable notices for Savvy CRM.

These facts remain relevant to any old commit, binary, image, archive, or
distribution that included AuthinCore. See [EXTRACTION.md](EXTRACTION.md) for
the original provenance record; do not infer that historical artifacts became
licensed merely because the current source was removed.

`provenance/SHA256SUMS` intentionally still names the removed source paths. It
is an immutable extraction checksum record, not a runtime/build manifest and
must not be rewritten to imply a different historical snapshot.

## Module identifier / CompanyOS naming

`services/crm-api` was originally imported as `github.com/comanyos/crm` and
used `companyos.crm.<entity>.<tenant>` event subjects. It was renamed to
`github.com/santoshguru/crm`; event subjects now use
`<source_app>.<entity>.<tenant>` (for example,
`crm.opportunity.<tenant>`), with the JetStream wildcard `crm.>`.

## Current direct Go dependencies

This table covers direct non-test requirements expected in
`services/crm-api/go.mod` after removal of AuthinCore and Savvy-issued JWTs.
Google ID tokens are still JWTs signed by Google and require the verifier
libraries below. Generated SBOM output is authoritative if it differs.

| Module | License (believed) |
| --- | --- |
| `connectrpc.com/connect` | Apache-2.0 |
| `github.com/MicahParks/jwkset` | MIT |
| `github.com/MicahParks/keyfunc/v3` | MIT |
| `github.com/golang-jwt/jwt/v5` | MIT |
| `github.com/jackc/pgx/v5` | MIT |
| `github.com/nats-io/nats.go` | Apache-2.0 |
| `github.com/prometheus/client_golang` | Apache-2.0 |
| `go.opentelemetry.io/otel` and sibling exporter/SDK packages | Apache-2.0 |
| `golang.org/x/time` | BSD-3-Clause |
| `google.golang.org/protobuf` | BSD-3-Clause |

The native authentication implementation uses the Go standard library for
cryptographic random generation, HMAC/SHA-256 token digests, SMTP/TLS, and
HTTP. The three focused JWT/JWKS packages above validate Google-issued ID
tokens only; Savvy does not issue or accept its own JWT access tokens. There is
no separately distributed identity-server dependency or vendored identity
platform.

## Current web dependencies

Representative direct package families used by `apps/web` are listed here;
the lockfile and generated SBOM must supply the complete transitive inventory.

| Package | License (believed) |
| --- | --- |
| `react`, `react-dom` | MIT |
| `react-router-dom` | MIT |
| `@connectrpc/connect`, `@connectrpc/connect-web` | Apache-2.0 |
| `@tanstack/react-query` | MIT |
| `react-hook-form`, `zod` | MIT |
| `vite`, `vitest` | MIT |
| `typescript` | Apache-2.0 |
| `tailwindcss`, `postcss`, `autoprefixer` | MIT |
| `@testing-library/*`, `jsdom` | MIT |

## Required follow-up

- Run an automated SBOM/license scanner (for example Syft plus ecosystem
  license tooling) against `services/crm-api`, `apps/web`, built images, and
  checked-in/generated browser assets.
- Verify the direct-dependency tables against the final Go module and npm
  lockfile after each release build.
- Retain license texts and notices required by direct and transitive
  dependencies in distributed images/assets.
- Confirm the repository's Apache-2.0 licensing choice and copyright owner
  before public or commercial distribution.
