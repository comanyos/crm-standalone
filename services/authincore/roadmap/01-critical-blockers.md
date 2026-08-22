# Critical Blockers — Nothing Runs Without These

These must be completed before authincore can authenticate a single user end-to-end.

---

## 1. Wire Identity Postgres Repository

**File:** `internal/command/commands.go` — `command.New(pool, es, nil, nil)`  
Both `identities` and `sessions` are passed as `nil`. Every command handler will panic at runtime.

**What to build:**
- `internal/storage/postgres/identity_repo.go` — implements `command.IdentityRepository`
  - `Insert(ctx, identity) error`
  - `Update(ctx, identity) error`
  - `Delete(ctx, id, orgID uuid.UUID) error`
  - `ByID(ctx, id, orgID uuid.UUID) (*identity.Identity, error)`
- Wire into `command.New(pool, es, identityRepo, sessionRepo)` in `cmd/authincore/main.go`

---

## 2. Wire Session Postgres Repository

**File:** `internal/command/commands.go`  
Same as above — `sessionWriter` interface is never satisfied.

**What to build:**
- `internal/storage/postgres/session_repo.go` — implements `command.SessionRepository`
  - `Insert(ctx, session) error`
  - `UpdateState(ctx, id uuid.UUID, state session.State) error`
  - `UpdateExpiry(ctx, id uuid.UUID, expiresAt time.Time) error`

---

## 3. Wire Password CredentialStore

**File:** `internal/flow/strategy/password/strategy.go`  
`pwdstrategy.New(hasher, nil)` in `main.go` — `store` is nil.  
Every login attempt will panic when `s.store.FindIdentityByIdentifier` is called.

**What to build:**
- `internal/storage/postgres/credential_store.go` — implements `password.CredentialStore`
  - `FindIdentityByIdentifier(ctx, orgID, identifier) (uuid.UUID, string, error)` — queries `identity_credentials` joined with `identities`
  - `StorePasswordCredential(ctx, identityID uuid.UUID, hash string) error`

---

## 4. Wire OIDC ClientStore and TokenStore

**File:** `internal/protocol/oidc/provider.go`  
`oidc.NewProvider(..., nil, nil, ...)` — both stores are nil.  
`/oauth2/token` will panic on any token request.

**What to build:**
- `internal/storage/postgres/oidc_client_store.go` — `ByClientID(ctx, orgID, clientID) (*oidc.Client, error)`
- `internal/storage/postgres/oidc_token_store.go` — `StoreAuthCode`, `ConsumeAuthCode`, `StoreRefreshToken`, `ConsumeRefreshToken`, `RevokeRefreshToken`

---

## 5. Wire Tenancy Resolver

**File:** `cmd/authincore/main.go` — `Resolver: nil`  
`orgIDFromRequest` falls back to `X-Org-ID` header when resolver is nil, which is acceptable for development but must be replaced for multi-tenant production.

**What to build:**
- `internal/storage/postgres/tenancy_repo.go` — implements `tenancy.Resolver`
  - `ByDomain(ctx, domain) (*tenancy.Tenant, error)`
  - `ByOrgID(ctx, orgID) (*tenancy.Tenant, error)`

---

## 6. Run Migrations Before First Start

**Command:** `DATABASE_URL=... ./bin/authinctl migrate`  
The schema in `migrations/001_init.sql` must be applied before the server starts.  
Tables do not exist on a fresh Postgres instance.

---

## Status Tracker

- [ ] Identity Postgres repository
- [ ] Session Postgres repository  
- [ ] Password credential store
- [ ] OIDC client + token stores
- [ ] Tenancy resolver
- [ ] Migration applied on fresh DB
