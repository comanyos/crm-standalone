# Design — the metadata layer (task_v2 Phase 1)

Status: **accepted and partially implemented** (2026-08-22). Both owner
decisions are made: §1 storage is JSONB values with relational definitions,
and §5 wire contract is `map<string, CustomValue>` with a typed oneof.

Implemented: tenant-scoped custom field definitions and values on every
shipped object; typed Connect contracts; administrator field/object screens;
shared-table custom objects and records; bounded formula evaluation;
per-field validation expressions; and server-side field visibility/editability.
The domain, API, authorization, real-Postgres RLS, and web-build checks cover
these paths. Filtering, reporting, search, and automation integration remain
deliberately open: those are the Phase 3–5 systems this design was created to
support, and their acceptance must not be claimed before those systems exist.

## Why this document exists first

Phase 1 lets a tenant administrator extend the data model without a migration
or a deploy. It is the largest architectural distance to Salesforce, Dynamics,
and SAP, and the hardest thing in `task_v2.md` to reverse: automation (Phase
3), reporting (Phase 4), search (Phase 5), and the public API (Phase 6) each
have to address custom fields, so choosing wrong means rewriting four phases
rather than one.

It is also the change most able to damage what this codebase does best. The
tenant isolation proof is the product's differentiator; a metadata layer that
weakens it trades the one thing competitors cannot offer for a feature they
all already have.

## What the existing code constrains

These are properties of the tree today, not preferences. Every option below is
judged against them.

1. **Isolation is enforced by PostgreSQL, not by Go.** `internal/domain/rls.go`
   binds `app.tenant_id` per transaction with `set_config(..., true)`, and
   `migrations/0003_rls_enforce.sql` gives every tenant table `ENABLE` +
   `FORCE ROW LEVEL SECURITY` with a policy carrying both `USING` and
   `WITH CHECK`. `crm_app` holds no `BYPASSRLS`. A missing `WHERE tenant_id`
   in Go returns zero rows rather than another tenant's.
2. **The isolation test discovers tables from the catalog.**
   `TestEveryTenantTableEnforcesRLS` queries `pg_class` for every table
   carrying `tenant_id` and asserts enablement, forcing, and policy shape. A
   new table is covered the moment it exists — but only if it carries
   `tenant_id`.
3. **`(tenant_id, id)` is the primary key**, tenant first, on every entity table.
4. **Filtering and sorting are allowlists.** `internal/domain/query.go` defines
   `AccountSort`/`AccountFilter` and friends; `ParseAccountSort` rejects
   anything unenumerated at the RPC boundary. No caller-supplied string has
   ever reached a query as an identifier.
5. **Migrations are append-only** and applied by a tracked runner
   (`scripts/migrate.sh` against `schema_migrations`). Nothing in the running
   system issues DDL.
6. **`crm_app` has DML only** — `GRANT SELECT, INSERT, UPDATE, DELETE`. It
   cannot create, alter, or drop anything.
7. **Two Store implementations** (`MemStore`, `PgStore`) are held to one
   interface by compile-time assertions in `conformance.go`. Anything added
   here lands in both.

Constraint 6 deserves emphasis. Because the application role cannot issue DDL,
a SQL injection in this service cannot alter the schema. Any design that gives
`crm_app` `CREATE`/`ALTER` to support custom fields spends that property, and
spends it permanently.

---

## 1. Custom-field value storage — OWNER DECISION

### Option A — `JSONB` column on each shipped table (recommended)

```sql
ALTER TABLE accounts ADD COLUMN custom_fields jsonb NOT NULL DEFAULT '{}';
CREATE INDEX accounts_custom_fields_gin
    ON accounts USING gin (custom_fields jsonb_path_ops);
```

- **Isolation: unchanged.** Values live on a row already covered by
  `accounts_tenant_policy`. There is no new isolation surface for values at
  all — not a new table, not a new join, not a new policy to get right. This
  is the single strongest argument for A.
- **No runtime DDL.** Adding a field is an `UPDATE` to a definitions row, not
  an `ALTER TABLE`. `crm_app` keeps DML-only rights (constraint 6).
- **No new locking risk.** `ALTER TABLE` on a large multi-tenant table takes
  an `ACCESS EXCLUSIVE` lock affecting *every* tenant. One tenant adding a
  field must never stall the others.
- **One row, one read.** List queries stay single-table; no join and no
  per-record fan-out.
- **Filtering** works through GIN containment (`custom_fields @> '{"k":"v"}'`)
  for equality, which covers the common "filter by picklist / lookup" case.
- Costs: type enforcement moves to Go (§2 mitigates); ordering by a custom
  field needs work deferred to Phase 4 (§4); JSONB has no per-field `NOT NULL`
  or `CHECK` at the database level.

### Option B — sparse pre-allocated typed columns

`custom_text_1..40`, `custom_number_1..20`, … with a mapping table.

- Native types, native indexes, native constraints.
- **Rejected**: it imposes an arbitrary per-type field cap, wastes width on
  every row of every tenant, and makes the mapping table a second place
  isolation must hold. The alternative — allocating columns on demand — is
  runtime DDL, which violates constraints 5 and 6 and reintroduces the
  cross-tenant `ACCESS EXCLUSIVE` lock. Salesforce does operate a variant of
  this, on machinery far larger than this project will build.

### Option C — EAV side table

`custom_field_values(tenant_id, object_type, record_id, field_id, value_text, value_number, …)`

- Unlimited fields, typed value columns, per-field indexes.
- The catalog test would cover it automatically (it carries `tenant_id`).
- **Rejected**: every record read becomes a fan-out, and a list of 50 accounts
  showing 10 custom fields becomes 500 extra rows to fetch and pivot. Phase 4
  reporting turns that into aggregate-over-join, which is where EAV schemas
  characteristically fall over. The performance cost is paid on every read
  forever, to buy database-level typing that §2 provides adequately at the
  write boundary.

### Recommendation

**Option A**, in a hybrid with relational *definitions*:

- **Definitions are relational** — a real `custom_field_definitions` table with
  `tenant_id`, constraints, and RLS. Definitions are low-cardinality,
  frequently queried, and must be validated; they earn a real table.
- **Values are `JSONB`** on the owning row, keyed by the definition's stable
  `field_key`.

This puts the structure where it needs querying and the flexibility where it
needs to vary, and it changes nothing about how isolation is enforced.

```sql
CREATE TABLE custom_field_definitions (
    tenant_id     text NOT NULL,
    id            text NOT NULL,
    object_type   text NOT NULL,   -- 'account' | 'contact' | … | custom object id
    field_key     text NOT NULL,   -- stable key used in the JSONB document
    label         text NOT NULL,
    data_type     text NOT NULL,   -- see §2
    config        jsonb NOT NULL DEFAULT '{}',  -- picklist values, lookup target, precision
    required      boolean NOT NULL DEFAULT false,
    archived_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, object_type, field_key),
    CONSTRAINT cfd_data_type_known CHECK (data_type IN (
        'text','long_text','number','currency','percent','boolean',
        'date','datetime','picklist','multi_picklist','lookup','formula','rollup')),
    CONSTRAINT cfd_field_key_shape CHECK (field_key ~ '^[a-z][a-z0-9_]{0,62}$')
);
```

`field_key` is regex-constrained at the database because it becomes a JSONB
object key and appears in API payloads; it must never be free-form text.
Definitions are **archived, never deleted** — a deleted definition would
orphan values already written into rows, and orphaned values are how a
"removed" field resurfaces later carrying data nobody expects.

---

## 2. Typing and validation

Database-level typing is what Option A gives up, so the write boundary has to
be genuinely strict rather than nominally strict.

- One `coerceAndValidate(definition, rawValue)` in `internal/domain`, called by
  every write path before the JSONB document is assembled. Not in
  `internal/api` — the domain owns invariants here, and `MemStore` must
  enforce the same rules as `PgStore` or the two diverge.
- Unknown keys are **rejected**, not stored. A JSONB column that accepts
  arbitrary keys is an undocumented schema, and it is also a place to smuggle
  data past field-level security (§4).
- Numbers are stored as JSON numbers only where a double is lossless.
  **Currency and any exact decimal are stored as strings** and parsed as
  decimals. A CRM that rounds an opportunity amount because JSON numbers are
  IEEE-754 doubles is not acceptable, and this is the same reason §5 rejects
  `google.protobuf.Struct`.
- `lookup` values store the referenced record id and are validated to exist
  **in the same tenant** at write time, mirroring how `ParentID` is validated
  in `account_hierarchy.go`. A lookup is the one custom field type that can
  create a cross-tenant reference, so it gets the existing treatment for
  exactly that risk.
- A JSONB document size ceiling per record, enforced in Go, so one tenant
  cannot make its own rows unreadable.

---

## 3. Formula and validation-rule evaluation (1.3)

A sandboxed, **non-Turing-complete** expression language: literals, field
references, arithmetic, comparison, boolean logic, and an allowlisted function
set. No loops, no recursion, no user-defined functions, no I/O.

The former AuthinCore component ran goja (real JavaScript) for actions and
policy; that source has been removed from this repository. The metadata layer
deliberately does **not** follow that historical precedent. Formulas are
authored by ordinary tenant administrators rather than instance operators, they run on
every record read or write rather than at a few control points, and a JS engine
brings an attack surface — prototype pollution, engine CVEs, timing and memory
exhaustion — that a 300-line expression evaluator does not have. The narrower
language is the safer and the cheaper one.

Because it parses tenant-authored input, it is a security boundary and gets
treated like one: a bounded evaluation budget (step count and wall clock), a
bounded expression depth and length at parse time, a fuzz suite in CI, and no
capability to reference another tenant's data by construction — evaluation
receives only the record already fetched under RLS.

Formula fields are **computed on read and never stored**, so a definition
change cannot leave stale values behind. Rollups are the exception and are
deferred to Phase 4, where the aggregation layer they need is being built
anyway.

---

## 4. Field-level security (1.4)

Enforced **server-side, by omission**. A field a role may not see is absent
from the response, not hidden by the client. The rule follows the existing
tenant-safe not-found convention: the response must not distinguish "no value"
from "not permitted".

Order of operations on read: fetch under RLS → drop values the principal's role
may not see → compute formulas over what remains. Computing first would let a
formula referencing a restricted field leak it through its result.

On write, a restricted field present in the payload is a rejection, not a
silent drop — a silent drop tells the caller their edit succeeded when it did
not.

This lands with Phase 2's permission work, which is why `task_v2.md` sequences
Phase 2 immediately after Phase 1.

---

## 5. Wire contract — OWNER DECISION

Shipped fields stay explicit, typed proto fields. Only custom values are
dynamic, so type safety on the 99% path is untouched.

### Option A — `google.protobuf.Struct custom_fields = N`

Standard and well supported in both `connect-go` and `connect-es`. **Rejected**:
`Struct` models JSON, so every number is a double and there is no date, no
decimal, and no way to distinguish an integer from a float. Currency loses
exactness, which §2 already establishes is not acceptable.

### Option B — `map<string, CustomValue>` with a typed oneof (recommended)

```proto
message CustomValue {
  oneof value {
    string      text        = 1;
    double      number      = 2;
    string      decimal     = 3;  // exact; currency, percent
    bool        boolean     = 4;
    google.protobuf.Timestamp timestamp = 5;
    string      date        = 6;  // RFC 3339 date, no zone
    string      reference   = 7;  // lookup target record id
    StringList  text_list   = 8;  // multi-picklist
  }
}
```

Preserves type fidelity including exact decimals, generates type-safe code on
both sides, and is self-documenting. Costs one `oneof` switch in the client.

### Option C — a parallel dynamic endpoint

**Rejected**: two round trips per record, two sources of truth, and no way to
write a record and its custom values in one transaction.

### Recommendation

**Option B**, with `map<string, CustomValue> custom_fields` added to each
entity message, and definitions served by their own typed CRUD service.

---

## 6. Custom objects (1.2)

One shared table, not a table per object:

```sql
CREATE TABLE custom_records (
    tenant_id   text NOT NULL,
    object_type text NOT NULL,   -- FK-by-convention to custom_object_definitions
    id          text NOT NULL,
    fields      jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, object_type, id)
);
```

A table per custom object would mean runtime DDL — constraints 5 and 6 again —
and unbounded table count. The shared table carries `tenant_id`, so
`TestEveryTenantTableEnforcesRLS` covers it automatically the moment the
migration lands, and its policy is the same shape as every other.

---

## 7. Query, filter, and sort

Constraint 4 is not relaxed. Callers still never supply a column name.

- A custom-field filter names a `field_key`, which is resolved against
  `custom_field_definitions` for that tenant. An unresolved key is
  `ErrInvalid` at the RPC boundary, exactly as `ParseAccountSort` behaves now.
- Equality and containment filters compile to `custom_fields @> $1` against the
  GIN index; the parameter is a bound JSONB value, never interpolated SQL.
- **Ordering by a custom field is deferred to Phase 4.** Doing it well needs
  either a per-definition expression index (runtime DDL, which this design
  spends real effort avoiding) or the materialized aggregation layer Phase 4
  builds. Shipping a sort that silently degrades to a full scan on a large
  tenant would be worse than not shipping it, so Phase 1 supports filtering
  only and says so in the API.

---

## 8. Sequencing

1. `custom_field_definitions` migration, domain type, validation, typed CRUD.
2. `custom_fields` JSONB column on shipped tables + GIN indexes.
3. `coerceAndValidate` and the write-path wiring, in `MemStore` and `PgStore`.
4. Proto `CustomValue` + `map` field, regenerate, convert in `internal/api`.
5. Filtering via GIN containment.
6. Expression evaluator + validation rules + fuzz suite.
7. Field-level security (with Phase 2).
8. Custom objects.
9. Admin UI for definitions.

Each step ends by re-running the two-organization isolation suite, per
`task_v2.md`'s progress rule.

## 9. Risks

| Risk | Mitigation |
| --- | --- |
| JSONB becomes an untyped dumping ground | Unknown keys rejected; every write goes through `coerceAndValidate` |
| Currency rounding through JSON doubles | Exact decimals stored and transported as strings (§2, §5) |
| A lookup creates a cross-tenant reference | Existence validated in-tenant at write, as `ParentID` already is |
| Formula evaluator becomes an execution sink | Non-Turing-complete, bounded budget, fuzzed in CI |
| Custom fields bypass field-level security | Values dropped server-side before formulas evaluate (§4) |
| One tenant's field count degrades shared tables | Per-record document ceiling; no runtime DDL, so no shared-table locks |
| `MemStore` and `PgStore` diverge | Validation lives in `internal/domain`, above both |
