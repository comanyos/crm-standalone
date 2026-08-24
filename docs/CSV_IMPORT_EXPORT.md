# CSV import and export — v1

This document is the product decision record the goal for M4 asked to exist
before implementation started, and now also the description of what shipped.
Where a decision was narrowed to keep the surface bounded and honest rather
than half-built across every entity, that narrowing is named explicitly.

## Scope

**Entity: accounts only, in v1.** Contacts, leads, opportunities and tasks
are not importable or exportable through this feature yet — a named gap, the
same posture account merge takes on being account-only (see `task.md`
Phase 6). Accounts were chosen because they are the record type reachable
from customer-facing data sources (a CRM export, a spreadsheet of prospects)
most often, and because the dedup-flag machinery (`account_hierarchy.go`)
already exists to catch a website-domain collision an import introduces.

## Format, size, and encoding

- CSV only, UTF-8, comma-separated, one header row required. The header
  names are never trusted as field names directly — a caller supplies an
  explicit `mapping` from header text to one of the importable Account
  fields (see below), so a spreadsheet titled "Company Name" instead of
  "name" still imports correctly, and an extra column the caller has not
  mapped is silently ignored rather than rejected.
- Bounded at **500 data rows** and **2 MiB** of raw file content per import
  job (`maxImportRows`, `maxImportFileBytes` in `internal/domain/import_export.go`).
  A file over either limit is rejected before any row is parsed. This is
  deliberately small: it keeps the whole operation synchronous and
  inside one RPC's timeout (see "Synchronous, not asynchronous" below)
  rather than requiring a background worker this version does not have.
- Exports are bounded at **500 rows** per request (`maxExportRows`) — the
  same cap `Page`'s own `maxPageSize` (`types.go`) already enforces on every
  other list RPC, reused here rather than introducing a second, larger
  page size and the multi-page looping it would need — and returned inline
  in the RPC response as CSV text, not as a separate downloadable file —
  see "No object storage" below. A tenant with more than 500 accounts
  needs more than one export call to cover all of them; there is no
  cursor continuation in v1.

## Field mapping

Importable fields: `name` (required), `industry`, `tier`, `owner_id`,
`website`, `annual_revenue`, `parent_id` — the same set
`internal/domain/merge_accounts.go`'s `mergeableAccountFields` allowlist
resolves for a merge, reused here because both are "which Account fields
does an external, untrusted source get to set" questions with the same
answer. Custom fields are not importable in v1: they would need the same
`CoerceCustomValues` authorization and typing path `CreateAccount` uses per
field, which a bulk CSV path does not replicate — the same reason
`MergeAccountsInput` also excludes them.

`mapping` is a `map<string, string>` from **CSV header text** to **one of
the fields above**. A header not present in the map is ignored. A mapped
field name outside the allowlist is rejected at job creation, the same
posture `MergeAccountsInput.Validate` takes for merge field resolutions.

## Duplicate policy

Every imported row runs through `applyAccountDuplicateFlag`
(`account_hierarchy.go`) exactly like a row created through the UI or API:
an exact website-domain match against an existing account sets
`duplicate_of_account_id` on the new row. **The import never merges, skips,
or updates an existing account automatically** — every row becomes a new
`Account`, flagged if it looks like a duplicate, left for a human to review
and merge afterward through the existing merge workflow
(`MergeAccounts`). An automatic "update the matching existing account
instead of creating a new one" mode is a real, wanted feature this version
does not have: it needs a decision about which fields an import may
overwrite on a record nobody uploaded it for, which nobody has made.

## Row-level errors

A row fails independently of the others — one malformed `annual_revenue`
value does not block the other 499 rows. Each failure is recorded as
`{row_number, message}` in the job's `errors`, where `row_number` is
1-indexed against the data rows (excluding the header) so it lines up with
what a spreadsheet program shows. `message` is the same bounded,
user-safe text `bulkErrorMessage` (`internal/api/bulk.go`) gives a failed
bulk-operation item — never a raw internal error.

## Dry run and commit

Import is a two-call sequence, not one:

1. **`CreateImportJob`** uploads the CSV content and mapping, validates
   every row (required field present, `annual_revenue` parses as a number,
   row and file bounds), and returns a job in `previewed` status with the
   per-row error list — nothing is written to any Account yet. This is the
   dry run; there is no separate "preview" endpoint because creating the
   job *is* the preview.
2. **`CommitImportJob`** re-reads the stored job and actually creates the
   valid rows as Accounts. It is idempotent: calling it again on a job
   already `completed` returns the same result without creating rows
   twice, the same idempotency posture `AssignTag` and the bulk RPCs have.
   A row that failed validation at preview time is still skipped at commit
   time and still counted in the same `errors` list.

A caller who never calls `CommitImportJob` has changed nothing — the job
record and its stored CSV content are the only artifacts, and no Account
exists because of it.

## No object storage — the stored-content decision

Production object storage is one of the explicitly undecided items in
`task.md`'s "Required external decisions" table. Rather than build against a
provider nobody has chosen, an import job's raw CSV content is stored
**inline, as text, in the `import_jobs` row itself**
(`migrations/0027_import_export.sql`), bounded by the same 2 MiB limit the
upload itself is bounded by. This is the "clearly bounded local/dev
implementation" the M4 goal asked for in place of object storage, not a
production design: a real deployment should move the file to object storage
and store a reference, with virus/malware scanning on upload, before this
handles anything larger or anything other than plain CSV text. Nothing here
executes or interprets the uploaded content as anything but delimited text.

Export content is not stored at all — it is generated for one request and
returned in that response. There is no expiring download link, because
there is nowhere durable to put the file. A production implementation needs
the same object-storage decision, plus an expiry and a signed-URL story,
before "secure download" can mean more than "sent once over the same
authenticated RPC connection."

## Synchronous, not asynchronous

The M4 goal asked for asynchronous import jobs and asynchronous exports.
**This version processes both synchronously**, inside the RPC call, bounded
by the row/size limits above so that a call reliably finishes inside a
normal request timeout. `ImportJob`/`ExportJob` are still durable, queryable
records — the "job" *concept* and its audit trail are real — but there is no
background worker, no queue, and no progress-while-running state a client
could poll. A production version needs a worker in the shape
`internal/integration/workflow`'s outbox poller already is: claim, process
a bounded batch, persist progress, release — extending the row/size bounds
past what fits in one request only makes sense once that exists.

## Audit and retention

The `ImportJob` row itself — status, actor, mapping, per-row errors, final
counts — is the audit record, updated in place when `CommitImportJob` runs.
There is deliberately no separate outbox event: `EnqueueEvent` is meant to
be called from inside the one transaction that makes the state change it
describes (`event_outbox.go`'s doc comment), and a multi-row import commits
each row through the ordinary `CreateAccount` path independently rather
than in one transaction — the same non-atomic, per-item posture the bulk
RPCs (`internal/api/bulk.go`) already document, for the same reason.
Inventing a new outbox event type here would also mean re-checking it
against workflow automation's loop-prevention allowlist
(`AssertTriggersCannotLoop`), which is unwarranted scope for a bounded v1
feature. Export requests are similarly recorded as `ExportJob` rows at
generation time, an authorization snapshot in the sense that the job
records which scope generated it, though the file itself is never
persisted (see above).

**Retention is not enforced.** `import_jobs` and `export_jobs` rows,
including the stored CSV content, are kept indefinitely — there is no
cleanup job. A production deployment needs one, sized to whatever retention
policy Phase 10's still-open privacy/retention decision settles on
(`task.md`'s "Required external decisions" table).

## Rollback

**There is no rollback for a committed import.** Once `CommitImportJob` has
created Accounts, those rows are ordinary Accounts — editable, mergeable,
deletable one at a time through the existing account RPCs, but not
distinguishable as "came from import job X" in a way a bulk "undo this
import" operation could act on beyond the job's own row-id list, and there
is no bulk-delete RPC to point at that list even if there were. This is the
same posture `MergeAccounts` documents explicitly rather than implying:
undo is a real, wanted feature that needs its own design, not an assumed
side effect of storing an audit trail.

## Authorization

`CreateImportJob` and `CommitImportJob` require `crm:account:write` — an
import is authorization-equivalent to calling `CreateAccount` once per row.
`ListImportJobs`/`GetImportJob` require `crm:account:read`. Export requires
`crm:account:read`. There is no separate import/export scope: the goal is
"may this principal create/read accounts," which the existing scopes
already answer, the same reasoning bulk operations and merge both reuse
their entity's existing scope pair instead of inventing a new one.
