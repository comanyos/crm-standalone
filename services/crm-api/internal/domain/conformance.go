package domain

// Compile-time proof that every Store implementation satisfies the whole
// contract.
//
// Without these, adding a method to Store leaves an implementation silently
// non-conforming until something assigns it at runtime. The store is chosen
// from config — MemStore when DATABASE_URL is unset — so a MemStore-backed test
// suite stays green while the deployed, Postgres-backed service fails to
// construct. That is the worst shape of bug this tree can produce: invisible in
// CI, fatal in production, and only in the deployment path.
var (
	_ Store = (*MemStore)(nil)
	_ Store = (*PgStore)(nil)
)
