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

- Source: companyos-suite/08-trust-os/authincore
- Base commit: 3716fdc2a1b5eef0f6422cf7d3fd28431fab2f7c
- Working tree at extraction: modified with newer source and untracked source
- Source status fingerprint: d5b292f970ef2a82ec96cbb667c70c372d49f61c3ae552788d059b8b8fdae17e
- Snapshot policy: copy the current source tree and record destination checksums
- Deliberately omitted: Git metadata, local environment files, compiled binaries,
  local editor settings, screenshots, caches, private keys, and certificates

AuthinCore did not contain a standalone license file at extraction time. Its
README describes it as MIT-licensed; license provenance must be confirmed before
external distribution.

## Isolation guarantee

No symlink, container mount, build context, or module replacement points to the
CompanyOS workspace. All future changes belong in this independent repository.
