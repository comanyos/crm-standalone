# Extraction record

This project was copied from CompanyOS as a source-only snapshot. The source
workspace is not a dependency and was not modified.

## CRM

- Source: companyos-suite/02-customer-os/crm
- Commit: 82e6f55819238bcdcc79bc5e4fe99f280ac896fc
- Working tree at extraction: clean
- Copied component: standalone/backend plus root migrations
- Deliberately omitted: plug, nested Git metadata, and compiled binaries

## AuthinCore

**Historical record:** this extracted component was later removed in full when
Savvy CRM replaced the separate identity platform with minimal first-party Go
authentication inside `services/crm-api`. It is not part of the current source,
build, runtime, dependency graph, or distribution. The details below are kept
only to preserve provenance of the earlier repository state.

- Source: companyos-suite/08-trust-os/authincore
- Base commit: 3716fdc2a1b5eef0f6422cf7d3fd28431fab2f7c
- Working tree at extraction: modified with newer source and untracked source
- Source status fingerprint: d5b292f970ef2a82ec96cbb667c70c372d49f61c3ae552788d059b8b8fdae17e
- Snapshot policy: copy the current source tree and record destination checksums
- Deliberately omitted: Git metadata, local environment files, compiled binaries,
  local editor settings, screenshots, caches, private keys, and certificates

AuthinCore did not contain a standalone license file at extraction time. Its
README described it as MIT-licensed. Removing the component resolved that
license-provenance concern for current distributions; historical snapshots that
included it still require their own review.

## Isolation guarantee

No symlink, container mount, build context, or module replacement points to the
CompanyOS workspace. All future changes belong in this independent repository.
