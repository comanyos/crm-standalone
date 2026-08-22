# License inventory and third-party notices

This is a first-pass, manually assembled inventory (Phase 0.1 of `task.md`).
It is **not** a substitute for an automated SBOM/license scan — that is a
separate Phase 10 action item (`Perform dependency/SBOM scans`). Licenses
below are recorded from each package's published metadata at the time of
writing and should be re-verified by an automated tool before any external
distribution.

## This project's own licensing — unresolved

These are open findings, not settled facts. They need a product/legal owner
decision before public distribution (see the "Required external decisions"
table in `task.md`).

1. **`services/crm-api/LICENSE` does not appear to be this project's license.**
   Its text is the Apache License 2.0 boilerplate with the example
   copyright line left in place: `Copyright 2021-2025 The Connect Authors`.
   That is the license notice for the `connectrpc.com/connect` Go module, not
   a license grant for this repository's own code. Before distribution,
   either replace it with the actual intended license for `services/crm-api`
   (with correct copyright holder), or confirm and document that the Connect
   project's Apache-2.0 terms were deliberately chosen for this codebase too.

2. **`services/authincore` has no `LICENSE` file**, but its `README.md` states
   "License: MIT — do whatever you want with it." This was already flagged at
   extraction time in `docs/EXTRACTION.md` ("license provenance must be
   confirmed before external distribution") and remains unresolved.

3. **RESOLVED (removed).** AuthinCore shipped a live, wired-in commercial
   license/feature-gating subsystem that contradicted the "MIT, do whatever
   you want" claim above: `internal/license/` verified signed JWT instance
   licenses and gated enterprise features (SAML, SCIM, custom branding, SSO
   enforcement, audit export) behind a paid tier — and, materially, capped
   the whole instance at **`MaxOrgs() == 1` organization whenever no license
   was active** (`internal/command/org.go`), which conflicts with this
   project's own multi-tenant definition of done in `task.md` (proving two
   organizations are isolated from each other requires being able to create
   a second one). At the user's direction this has been removed: the
   `internal/license` package, its Postgres persistence, the
   `/api/v1/license` HTTP endpoints, the admin-ui License page, and
   `migrations/018_license.sql` are all deleted. The underlying SAML/SCIM/
   branding/SSO-enforcement/audit-export features themselves were **not**
   removed — only the license paywall wrapping them — so they remain
   available (still only compiled in under `-tags enterprise`; the default
   OSS build still omits them, unchanged from before). Both build variants
   (`go build ./...` and `go build -tags enterprise ./...`) build and pass
   their existing tests after this change.

4. **RESOLVED (removed).** `services/crm-api/internal/license/` was a
   separate, seemingly unrelated commercial tier-gating package. Its own doc
   comment read: *"Package license provides Ed25519 license verification and
   tier gating for CloudOps. Mirrors DBCore's pattern[...]"* — referencing
   product names ("CloudOps", "DBCore") that did not appear anywhere else in
   this repository or in `docs/EXTRACTION.md`'s provenance record for the
   CompanyOS CRM source. It defined OSS/PRO/ENTERPRISE tiers, was actively
   loaded at startup in `cmd/crm/main.go`, and made an outbound HTTP call on
   every `Allows`/`HasFeature` check to a local flag-evaluation service
   (`FLAGD_URL`, OFREP protocol). At the user's direction this package and
   all its wiring (`cmd/crm/main.go`, `internal/server`, `internal/api`) have
   been removed; `go build ./...`, `golangci-lint run ./...` (0 issues), and
   `go test ./...` all pass afterward.

   **New, related finding (not yet resolved):** `services/authincore` has its
   own, separate "DBCore" trace unrelated to the above: a whole migration,
   `migrations/025_dbcore_role_scopes.sql`, seeds a role→scope catalogue
   whose comment says scopes are "stored fully qualified (`dbcore:query:read`)
   because that is the string DBCore's interceptor compares against," and
   `internal/authz/claims.go` has a comment referencing "organization-wide
   DBCore authority." This means a fresh AuthinCore database is, by default,
   seeded with role/scope data for an unrelated product ("DBCore"), not CRM.
   This was not touched in this change and needs its own decision: is this
   seed data required by AuthinCore's own tests/behavior (`internal/authz`
   tests reference it), or is it as removable as the license system was?

   **Measured since (2026-08-22), while building the §1.1 bootstrap
   integration tests.** The migration does more than seed existing rows: it
   installs an `AFTER INSERT` trigger on `orgs`, so *every organization ever
   created* — including every customer organization this CRM provisions — is
   given three DBCore roles (`admin`, `operator`, `readonly`) and a `dbcore`
   resource, and the `owner` role is granted `dbcore:*` on it. The migration
   also backfills organizations that already existed. A freshly bootstrapped
   organization therefore has 8 roles and 3 resources where this product
   defines 5 and 2.

   Blast radius is bounded and asserted: none of those roles carries any CRM
   scope, so they grant no access to this product. See
   `TestOrganizationsCarryRolesForAnUnrelatedProduct` in
   `services/authincore/cmd/authinctl/bootstrap_integration_test.go`, which
   also pins the CRM's own roles to
   `packages/contracts/rbac/crm-roles.json`. What the extra roles do cost is
   accuracy: they appear in any administration UI that lists roles, and in any
   audit of who can do what.

   Deciding to remove them means writing a migration that drops the trigger,
   the function, and the seeded rows — deleting authority rows from
   organizations that already exist, which is why it is a product decision and
   not a cleanup. **Deferred by the repository owner to this licensing pass**
   rather than done opportunistically. If the seeding is removed, the test
   named above will fail; deleting it is then the correct fix, and its comment
   says so.

5. **`services/authincore/third_party/platform-contract`** is a vendored
   source snapshot whose own package comment says: *"Package platform defines
   the shared CompanyOS module contract."* It carries no license file or
   notice of its own. It is wired in via a local Go `replace` directive
   (`replace github.com/santoshguru/platform-contract v0.0.0 =>
   ./third_party/platform-contract`) so it does not point outside this repo,
   but its content is an explicit, unlicensed CompanyOS-owned artifact copied
   into this codebase. Per `docs/EXTRACTION.md`'s stated policy ("the canonical
   contract" should be replaced by a tagged, immutable module release), this
   snapshot needs either a proper license grant from its origin or replacement
   with independently written code before external distribution.

Findings 3 and 4 are resolved (see above). Findings 1, 2, and 5, and the new
DBCore migration finding under 4, remain open judgment calls that need a
decision from whoever owns this project's licensing before the affected code
ships externally.

## Module identifier / CompanyOS naming (separate from licensing, related finding)

`services/crm-api`'s Go module was `github.com/comanyos/crm` and its NATS
event-subject convention was literally `companyos.crm.<entity>.<tenant>`. At
the user's direction both have been renamed: the module is now
`github.com/santoshguru/crm` (matching the naming AuthinCore's own
`third_party/platform-contract` snapshot already uses), and event subjects
are now `<source_app>.<entity>.<tenant>` (e.g. `crm.opportunity.<tenant>`),
with the JetStream stream wildcard updated to `crm.>` to match. Connect/
protobuf code was regenerated via `buf generate` after updating the `.proto`
`go_package` options. `gofmt`, `go vet`, `golangci-lint run ./...` (0 issues),
`go build ./...`, and `go test ./...` all pass after the rename.

`services/authincore/third_party/platform-contract` (finding 5 below) was
**not** part of this rename — it is a separate, larger decision since its
content, not just its name, originates from CompanyOS.

## Direct Go dependencies

Recorded from each `go.mod`'s direct (non-`// indirect`) requires. License
column is the package's commonly published license; verify with an SBOM tool
before relying on it for distribution.

### `services/crm-api` and `services/authincore` (shared)

| Module | License (believed) |
| --- | --- |
| `connectrpc.com/connect` | Apache-2.0 |
| `github.com/golang-jwt/jwt/v5` | MIT |
| `github.com/jackc/pgx/v5` | MIT |
| `github.com/MicahParks/keyfunc/v3` | MIT |
| `github.com/nats-io/nats.go` (crm-api only) | Apache-2.0 |
| `google.golang.org/protobuf` (crm-api only) | BSD-3-Clause |

### `services/authincore` additional direct dependencies

| Module | License (believed) |
| --- | --- |
| `github.com/alexedwards/argon2id` | BSD-3-Clause |
| `github.com/beevik/etree` | BSD-2-Clause |
| `github.com/crewjam/saml` | Apache-2.0 |
| `github.com/dop251/goja` | MIT |
| `github.com/go-webauthn/webauthn` | BSD-3-Clause |
| `github.com/google/uuid` | BSD-3-Clause |
| `github.com/pquerna/otp` | Apache-2.0 |
| `github.com/pressly/goose/v3` | MIT |
| `github.com/prometheus/client_golang` | Apache-2.0 |
| `github.com/redis/go-redis/v9` | BSD-2-Clause |
| `github.com/russellhaering/goxmldsig` | Apache-2.0 |
| `github.com/santhosh-tekuri/jsonschema/v5` | Apache-2.0 |
| `github.com/stretchr/testify` | MIT |
| `golang.org/x/crypto` | BSD-3-Clause |
| `gopkg.in/yaml.v3` | Apache-2.0 / MIT (dual) |
| `go.opentelemetry.io/otel` (and sibling otel packages) | Apache-2.0 |
| `github.com/santoshguru/platform-contract` | **Unresolved — see finding 5 above** |

### npm dependencies (`admin-ui`, `hosted-ui`)

| Package | License (believed) |
| --- | --- |
| `react`, `react-dom` | MIT |
| `react-router-dom` (admin-ui only) | MIT |
| `vite`, `vitest` | MIT |
| `typescript` | Apache-2.0 |
| `tailwindcss`, `postcss`, `autoprefixer` | MIT |
| `@testing-library/*`, `jsdom` | MIT |

## Recommended follow-up (Phase 10 scope)

- Run an automated SBOM/license scanner (e.g. `go-licenses`, `syft`, or
  equivalent for npm) against both services and `apps/web` once it exists,
  and replace this manual table with generated output.
- Resolve the five unresolved items above with the project's licensing/legal
  owner before any external or production distribution.
