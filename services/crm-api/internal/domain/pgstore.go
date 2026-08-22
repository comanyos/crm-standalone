package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santoshguru/crm/internal/metrics"
)

// PgStore is the PostgreSQL Store. Every statement filters on tenant_id, which is
// also the leading column of each table's primary key, and every statement runs
// through db (scopedDB) so migrations/0003_rls_enforce.sql's policies actually
// apply — a query issued straight off pool would bypass RLS on a connection
// that owns the tables. See rls.go.
type PgStore struct {
	pool *pgxpool.Pool
	db   *scopedDB

	// drain is the pool DueOutboxTenants queries on. It is the application
	// pool by default (pool), which is correct only while that pool's role
	// bypasses RLS. A deployment running as a restricted role must use
	// NewPgStoreWithDrain, or DueOutboxTenants silently sees nothing.
	drain *pgxpool.Pool
}

// Ping verifies the application database dependency without revealing its DSN.
func (s *PgStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// PoolStats returns only aggregate application-pool state for metrics.
func (s *PgStore) PoolStats() (acquired, idle, total, max int32) {
	stats := s.pool.Stat()
	return stats.AcquiredConns(), stats.IdleConns(), stats.TotalConns(), stats.MaxConns()
}

// NewPgStore connects to Postgres and verifies the connection before returning.
func NewPgStore(ctx context.Context, dsn string) (*PgStore, error) {
	return NewPgStoreWithDrain(ctx, dsn, "")
}

// NewPgStoreWithDrain connects the application pool and, when drainDSN is
// set, a second pool (meant for the crm_drain role) used only by
// DueOutboxTenants — the one cross-tenant read the outbox dispatcher needs
// (migrations/0004_event_outbox.sql).
func NewPgStoreWithDrain(ctx context.Context, dsn, drainDSN string) (*PgStore, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	poolCfg.ConnConfig.Tracer = metrics.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	s := &PgStore{pool: pool, db: newScopedDB(pool), drain: pool}
	if drainDSN == "" {
		return s, nil
	}

	drainCfg, err := pgxpool.ParseConfig(drainDSN)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("parse drain config: %w", err)
	}
	drainCfg.ConnConfig.Tracer = metrics.QueryTracer{}
	drain, err := pgxpool.NewWithConfig(ctx, drainCfg)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect drain: %w", err)
	}
	if err := drain.Ping(ctx); err != nil {
		drain.Close()
		pool.Close()
		return nil, fmt.Errorf("ping drain: %w", err)
	}
	s.drain = drain
	return s, nil
}

// accountColumns is the column list (in scan order) shared by
// GetAccount/ListAccounts/FindAccountByDomain — one list so the read paths
// cannot drift on which columns come back. The lead-conversion tx helpers
// (pgstore_lead_conversion.go) intentionally do not read/write
// duplicate_of_account_id: a conversion-created Account is never
// dedup-flagged (see MemStore.createAccount's requireOwner doc comment for
// the same asymmetry on owner_id). website_domain is deliberately excluded:
// it is a write-only, indexed mirror of websiteDomain(Website) that
// FindAccountByDomain queries directly (migrations/0010) — never read back
// into an Account, so it cannot drift from being the sole reason for
// Website's own value.
const accountColumns = `id, name, industry, tier, owner_id, website, annual_revenue, parent_id, duplicate_of_account_id, created_at, updated_at, custom_fields`

func scanAccount(row pgx.Row, v *Account) error {
	var customFields []byte
	if err := row.Scan(&v.ID, &v.Name, &v.Industry, &v.Tier, &v.OwnerID, &v.Website, &v.AnnualRevenue,
		&v.ParentID, &v.DuplicateOfAccountID, &v.CreatedAt, &v.UpdatedAt, &customFields); err != nil {
		return err
	}
	values, err := parseCustomValues(customFields)
	if err != nil {
		return err
	}
	v.CustomFields = values
	return nil
}

// queryBuilder accumulates WHERE-clause fragments and their positional
// arguments together, so a fragment and its placeholder number can never
// drift apart. arg appends v and returns its "$N" placeholder.
type queryBuilder struct {
	args []any
}

func (q *queryBuilder) arg(v any) string {
	q.args = append(q.args, v)
	return fmt.Sprintf("$%d", len(q.args))
}

// CreateAccount inserts a Account for the scope's tenant. It requires
// owner_id, rejects a dangling or cyclic parent_id (account_hierarchy.go),
// and flags (never blocks) an exact Website-domain match against another
// tenant-scoped Account.
func (s *PgStore) CreateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := requireAccountOwner(v); err != nil {
		return nil, err
	}
	if err := requireValidAccountParent(ctx, s, scope, "", v.ParentID); err != nil {
		return nil, err
	}
	if err := applyAccountDuplicateFlag(ctx, s, scope, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "account", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now

	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO accounts (tenant_id, id, name, industry, tier, owner_id, website, annual_revenue, parent_id, duplicate_of_account_id, created_at, updated_at, website_domain, custom_fields) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		scope.TenantID, v.ID, v.Name, v.Industry, v.Tier, v.OwnerID, v.Website, v.AnnualRevenue, v.ParentID, v.DuplicateOfAccountID, v.CreatedAt, v.UpdatedAt, websiteDomain(v.Website), document)
	if err != nil {
		return nil, fmt.Errorf("create account: %w", err)
	}
	return cloneAccount(v), nil
}

// GetAccount reads one Account belonging to the scope's tenant.
func (s *PgStore) GetAccount(ctx context.Context, scope Scope, id string) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v := &Account{}
	err := scanAccount(s.db.QueryRow(ctx, scope,
		`SELECT `+accountColumns+` FROM accounts WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	return v, nil
}

// accountSortColumn resolves the physical column + direction for sortBy —
// the only place a sort value maps to a column, so ListAccounts' WHERE/
// ORDER BY/cursor-decode below cannot disagree with each other about it.
func accountSortColumn(sortBy AccountSort) (column, dir string) {
	if sortBy == SortAccountCreatedDesc {
		return "created_at", "DESC"
	}
	return "name", "ASC"
}

// ListAccounts returns one keyset-paginated page of Accounts, filtered and
// ordered per sortBy. The cursor is a (sort column, id) tuple compared with
// Postgres row-value comparison (`(col, id) > ($a, $b)`), which is the
// standard way to keyset-paginate on a non-unique sort column without
// missing or repeating rows across pages when the sort column has ties.
func (s *PgStore) ListAccounts(ctx context.Context, scope Scope, page Page, filter AccountFilter, sortBy AccountSort) ([]*Account, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}

	q := &queryBuilder{args: []any{scope.TenantID}}
	where := []string{"tenant_id = $1"}

	if filter.NameContains != "" {
		where = append(where, "name ILIKE '%' || "+q.arg(filter.NameContains)+" || '%'")
	}
	if filter.Tier != "" {
		where = append(where, "tier = "+q.arg(filter.Tier))
	}
	if filter.OwnerID != "" {
		where = append(where, "owner_id = "+q.arg(filter.OwnerID))
	}
	if filter.ParentID != "" {
		where = append(where, "parent_id = "+q.arg(filter.ParentID))
	}

	column, dir := accountSortColumn(sortBy)
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		var cursorVal any = sortValue
		cmp := ">"
		if sortBy == SortAccountCreatedDesc {
			cmp = "<"
			t, err := time.Parse(time.RFC3339Nano, sortValue)
			if err != nil {
				return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
			}
			cursorVal = t
		}
		where = append(where, fmt.Sprintf("(%s, id) %s (%s, %s)", column, cmp, q.arg(cursorVal), q.arg(afterID)))
	}

	query := `SELECT ` + accountColumns + ` FROM accounts WHERE ` + strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY %s %s, id %s LIMIT %s", column, dir, dir, q.arg(page.size()+1))

	rows, err := s.db.Query(ctx, scope, query, q.args...)
	if err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	out := make([]*Account, 0, page.size())
	for rows.Next() {
		v := &Account{}
		if err := scanAccount(rows, v); err != nil {
			return nil, "", fmt.Errorf("scan account: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		last := out[len(out)-1]
		next = encodeCursor(accountSortValue(last, sortBy), last.ID)
	}
	return out, next, nil
}

// accountSortValue renders v's sort key as the string encodeCursor stores in
// the page token for sortBy — shared with MemStore's identical helper so
// both stores produce and consume the same token shape.
func accountSortValue(v *Account, sortBy AccountSort) string {
	if sortBy == SortAccountCreatedDesc {
		return v.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return v.Name
}

// UpdateAccount replaces an existing Account, preserving created_at. It
// requires owner_id, rejects a dangling or cyclic parent_id, refreshes the
// duplicate-domain flag, and — when the write actually changes OwnerID —
// appends a row to account_ownership_history (migrations/0007).
//
// Optimistic concurrency: a non-zero v.UpdatedAt must match the stored
// record's, or ErrConflict — see MemStore.UpdateAccount's doc comment for
// why this cannot break a caller that does not opt in.
func (s *PgStore) UpdateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetAccount(ctx, scope, v.ID)
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Account %s was modified since it was last read", ErrConflict, v.ID)
	}
	if err := requireAccountOwner(v); err != nil {
		return nil, err
	}
	if err := requireValidAccountParent(ctx, s, scope, v.ID, v.ParentID); err != nil {
		return nil, err
	}
	if err := applyAccountDuplicateFlag(ctx, s, scope, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "account", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = nowStamp()

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE accounts SET name = $3, industry = $4, tier = $5, owner_id = $6, website = $7, annual_revenue = $8, parent_id = $9, duplicate_of_account_id = $10, updated_at = $11, website_domain = $12, custom_fields = $13
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, v.ID, v.Name, v.Industry, v.Tier, v.OwnerID, v.Website, v.AnnualRevenue, v.ParentID, v.DuplicateOfAccountID, v.UpdatedAt, websiteDomain(v.Website), document)
	if err != nil {
		return nil, fmt.Errorf("update account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}

	updated, err := s.GetAccount(ctx, scope, v.ID)
	if err != nil {
		return nil, err
	}

	if existing.OwnerID != updated.OwnerID {
		if err := s.recordAccountOwnershipChange(ctx, scope, updated.ID, existing.OwnerID, updated.OwnerID); err != nil {
			return nil, err
		}
	}

	return updated, nil
}

// recordAccountOwnershipChange appends one row to account_ownership_history.
func (s *PgStore) recordAccountOwnershipChange(ctx context.Context, scope Scope, accountID, from, to string) error {
	_, err := s.db.Exec(ctx, scope,
		`INSERT INTO account_ownership_history (tenant_id, id, account_id, from_owner_id, to_owner_id, changed_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		scope.TenantID, newID(), accountID, from, to, nowStamp())
	if err != nil {
		return fmt.Errorf("record account ownership history: %w", err)
	}
	return nil
}

// ListOwnershipHistory returns every recorded owner_id change for one
// Account in the scope's tenant, oldest first.
func (s *PgStore) ListOwnershipHistory(ctx context.Context, scope Scope, accountID string) ([]*AccountOwnershipHistory, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT id, account_id, from_owner_id, to_owner_id, changed_at FROM account_ownership_history
		 WHERE tenant_id = $1 AND account_id = $2
		 ORDER BY changed_at, id`,
		scope.TenantID, accountID)
	if err != nil {
		return nil, fmt.Errorf("list account ownership history: %w", err)
	}
	defer rows.Close()

	out := make([]*AccountOwnershipHistory, 0, 4)
	for rows.Next() {
		v := &AccountOwnershipHistory{}
		if err := rows.Scan(&v.ID, &v.AccountID, &v.FromOwnerID, &v.ToOwnerID, &v.ChangedAt); err != nil {
			return nil, fmt.Errorf("scan account ownership history: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list account ownership history: %w", err)
	}
	return out, nil
}

// DeleteAccount removes an Account from the scope's tenant.
func (s *PgStore) DeleteAccount(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM accounts WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateContact inserts a Contact for the scope's tenant.
// contactColumns is the column list (in scan order) shared by
// GetContact/ListContacts/FindContactByEmail — one list so the read paths
// cannot drift on which columns come back.
const contactColumns = `id, email, account_id, first_name, last_name, phone, title, role, duplicate_of_contact_id, created_at, updated_at, custom_fields`

func scanContact(row pgx.Row, v *Contact) error {
	var customFields []byte
	if err := row.Scan(&v.ID, &v.Email, &v.AccountID, &v.FirstName, &v.LastName, &v.Phone, &v.Title,
		&v.Role, &v.DuplicateOfContactID, &v.CreatedAt, &v.UpdatedAt, &customFields); err != nil {
		return err
	}
	values, err := parseCustomValues(customFields)
	if err != nil {
		return err
	}
	v.CustomFields = values
	return nil
}

// CreateContact inserts a Contact for the scope's tenant. It rejects a
// dangling or cross-tenant account_id, rejects an unknown role value, and
// flags (never blocks) an exact-email match against another tenant-scoped
// Contact (B4 — docs/customer-os/PLAN.md, contact_integrity.go).
func (s *PgStore) CreateContact(ctx context.Context, scope Scope, v *Contact) (*Contact, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := requireContactRole(v); err != nil {
		return nil, err
	}
	if err := requireContactAccount(ctx, s, scope, v.AccountID); err != nil {
		return nil, err
	}
	if err := applyContactDuplicateFlag(ctx, s, scope, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "contact", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now

	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO contacts (tenant_id, id, email, account_id, first_name, last_name, phone, title, role, duplicate_of_contact_id, created_at, updated_at, custom_fields) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		scope.TenantID, v.ID, v.Email, v.AccountID, v.FirstName, v.LastName, v.Phone, v.Title, v.Role, v.DuplicateOfContactID, v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("create contact: %w", err)
	}
	return cloneContact(v), nil
}

// GetContact reads one Contact belonging to the scope's tenant.
func (s *PgStore) GetContact(ctx context.Context, scope Scope, id string) (*Contact, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v := &Contact{}
	err := scanContact(s.db.QueryRow(ctx, scope,
		`SELECT `+contactColumns+` FROM contacts WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get contact: %w", err)
	}
	return v, nil
}

// contactSortColumn resolves the physical column + direction for sortBy —
// see accountSortColumn's doc comment for why this is its own function.
func contactSortColumn(sortBy ContactSort) (column, dir string) {
	if sortBy == SortContactEmail {
		return "email", "ASC"
	}
	return "created_at", "DESC"
}

// ListContacts returns one keyset-paginated page of Contacts, filtered and
// ordered per sortBy — see ListAccounts' doc comment for the cursor design.
func (s *PgStore) ListContacts(ctx context.Context, scope Scope, page Page, filter ContactFilter, sortBy ContactSort) ([]*Contact, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}

	q := &queryBuilder{args: []any{scope.TenantID}}
	where := []string{"tenant_id = $1"}

	if filter.AccountID != "" {
		where = append(where, "account_id = "+q.arg(filter.AccountID))
	}
	if filter.NameContains != "" {
		ph := q.arg(filter.NameContains)
		where = append(where, "(first_name ILIKE '%' || "+ph+" || '%' OR last_name ILIKE '%' || "+ph+" || '%')")
	}
	if filter.EmailContains != "" {
		where = append(where, "email ILIKE '%' || "+q.arg(filter.EmailContains)+" || '%'")
	}

	column, dir := contactSortColumn(sortBy)
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		var cursorVal any = sortValue
		cmp := ">"
		if sortBy != SortContactEmail {
			cmp = "<"
			t, err := time.Parse(time.RFC3339Nano, sortValue)
			if err != nil {
				return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
			}
			cursorVal = t
		}
		where = append(where, fmt.Sprintf("(%s, id) %s (%s, %s)", column, cmp, q.arg(cursorVal), q.arg(afterID)))
	}

	query := `SELECT ` + contactColumns + ` FROM contacts WHERE ` + strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY %s %s, id %s LIMIT %s", column, dir, dir, q.arg(page.size()+1))

	rows, err := s.db.Query(ctx, scope, query, q.args...)
	if err != nil {
		return nil, "", fmt.Errorf("list contacts: %w", err)
	}
	defer rows.Close()

	out := make([]*Contact, 0, page.size())
	for rows.Next() {
		v := &Contact{}
		if err := scanContact(rows, v); err != nil {
			return nil, "", fmt.Errorf("scan contact: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list contacts: %w", err)
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		last := out[len(out)-1]
		next = encodeCursor(contactSortValue(last, sortBy), last.ID)
	}
	return out, next, nil
}

// contactSortValue renders v's sort key as the string encodeCursor stores in
// the page token for sortBy — shared with MemStore's identical helper.
func contactSortValue(v *Contact, sortBy ContactSort) string {
	if sortBy == SortContactEmail {
		return v.Email
	}
	return v.CreatedAt.UTC().Format(time.RFC3339Nano)
}

// UpdateContact replaces an existing Contact, preserving created_at.
// account_id is re-checked on every update, not only when it changes — the
// same convention UpdateOpportunity uses for its own account_id — and the
// duplicate-email flag is recomputed so it never goes stale.
//
// Optimistic concurrency: same convention as UpdateAccount — a non-zero
// v.UpdatedAt must match the stored record's, or ErrConflict.
func (s *PgStore) UpdateContact(ctx context.Context, scope Scope, v *Contact) (*Contact, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetContact(ctx, scope, v.ID)
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Contact %s was modified since it was last read", ErrConflict, v.ID)
	}
	if err := requireContactRole(v); err != nil {
		return nil, err
	}
	if err := requireContactAccount(ctx, s, scope, v.AccountID); err != nil {
		return nil, err
	}
	if err := applyContactDuplicateFlag(ctx, s, scope, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "contact", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = nowStamp()

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE contacts SET email = $3, account_id = $4, first_name = $5, last_name = $6, phone = $7, title = $8, role = $9, duplicate_of_contact_id = $10, updated_at = $11, custom_fields = $12
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, v.ID, v.Email, v.AccountID, v.FirstName, v.LastName, v.Phone, v.Title, v.Role, v.DuplicateOfContactID, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("update contact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetContact(ctx, scope, v.ID)
}

// DeleteContact removes an Contact from the scope's tenant.
func (s *PgStore) DeleteContact(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM contacts WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete contact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// leadColumns is the column list (in scan order) shared by every Lead read
// in this file, including the tx-scoped ones in
// pgstore_lead_conversion.go — one list so the two paths cannot drift on
// which columns come back.
const leadColumns = `id, company, source, contact_email, status, score, owner_id,
	duplicate_of_contact_id, converted_contact_id, converted_account_id, converted_opportunity_id,
	created_at, updated_at, custom_fields`

func scanLead(row pgx.Row, v *Lead) error {
	var customFields []byte
	if err := row.Scan(&v.ID, &v.Company, &v.Source, &v.ContactEmail, &v.Status, &v.Score, &v.OwnerID,
		&v.DuplicateOfContactID, &v.ConvertedContactID, &v.ConvertedAccountID, &v.ConvertedOpportunityID,
		&v.CreatedAt, &v.UpdatedAt, &customFields); err != nil {
		return err
	}
	values, err := parseCustomValues(customFields)
	if err != nil {
		return err
	}
	v.CustomFields = values
	return nil
}

// CreateLead inserts a Lead for the scope's tenant. It defaults and
// validates Status (B1's lifecycle machine — lead_lifecycle.go) and flags
// (never blocks) an exact-email match against an existing Contact.
func (s *PgStore) CreateLead(ctx context.Context, scope Scope, v *Lead) (*Lead, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	applyLeadStatusDefault(v)
	if err := checkLeadStatusKnown(v.Status); err != nil {
		return nil, err
	}
	if v.Status == LeadStatusConverted {
		return nil, fmt.Errorf(
			"%w: Lead.status cannot be created as %q directly; use ConvertLead", ErrInvalid, LeadStatusConverted)
	}
	if err := applyLeadDuplicateFlag(ctx, s, scope, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "lead", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now

	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO leads (tenant_id, id, company, source, contact_email, status, score, owner_id,
			duplicate_of_contact_id, converted_contact_id, converted_account_id, converted_opportunity_id,
			created_at, updated_at, custom_fields)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		scope.TenantID, v.ID, v.Company, v.Source, v.ContactEmail, v.Status, v.Score, v.OwnerID,
		v.DuplicateOfContactID, v.ConvertedContactID, v.ConvertedAccountID, v.ConvertedOpportunityID,
		v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("create lead: %w", err)
	}
	return cloneLead(v), nil
}

// GetLead reads one Lead belonging to the scope's tenant.
func (s *PgStore) GetLead(ctx context.Context, scope Scope, id string) (*Lead, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v := &Lead{}
	err := scanLead(s.db.QueryRow(ctx, scope,
		`SELECT `+leadColumns+` FROM leads WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get lead: %w", err)
	}
	return v, nil
}

// ListLeads returns one keyset-paginated page of Leads.
func (s *PgStore) ListLeads(ctx context.Context, scope Scope, page Page) ([]*Lead, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+leadColumns+` FROM leads
		 WHERE tenant_id = $1 AND id > $2
		 ORDER BY id LIMIT $3`,
		scope.TenantID, page.Token, page.size()+1)
	if err != nil {
		return nil, "", fmt.Errorf("list leads: %w", err)
	}
	defer rows.Close()

	out := make([]*Lead, 0, page.size())
	for rows.Next() {
		v := &Lead{}
		if err := scanLead(rows, v); err != nil {
			return nil, "", fmt.Errorf("scan lead: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list leads: %w", err)
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// UpdateLead replaces an existing Lead, preserving created_at. It enforces
// the status state machine and refreshes the duplicate-contact flag. Status
// can never be set to converted directly through this path — only
// ConvertLead may perform that transition, since it is the one that also
// creates the linked records.
//
// Optimistic concurrency: a non-zero v.UpdatedAt must match the stored
// record's, or the write is rejected with ErrConflict. A caller that does not
// round-trip UpdatedAt skips the check. See UpdateAccount for the same
// contract — leads and opportunities were the two entities missing it, while
// the web client had been sending the field for both all along.
func (s *PgStore) UpdateLead(ctx context.Context, scope Scope, v *Lead) (*Lead, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetLead(ctx, scope, v.ID)
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Lead %s was modified since it was last read", ErrConflict, v.ID)
	}
	if v.Status == "" {
		v.Status = existing.Status
	}
	if v.Status == LeadStatusConverted && existing.Status != LeadStatusConverted {
		return nil, fmt.Errorf(
			"%w: Lead.status cannot be set to %q directly; use ConvertLead", ErrInvalid, LeadStatusConverted)
	}
	if err := checkLeadStatusTransition(existing.Status, v.Status); err != nil {
		return nil, err
	}
	if err := applyLeadDuplicateFlag(ctx, s, scope, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "lead", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = nowStamp()

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE leads SET company = $3, source = $4, contact_email = $5, status = $6, score = $7, owner_id = $8,
			duplicate_of_contact_id = $9, converted_contact_id = $10, converted_account_id = $11,
			converted_opportunity_id = $12, updated_at = $13, custom_fields = $14
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, v.ID, v.Company, v.Source, v.ContactEmail, v.Status, v.Score, v.OwnerID,
		v.DuplicateOfContactID, v.ConvertedContactID, v.ConvertedAccountID, v.ConvertedOpportunityID, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("update lead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetLead(ctx, scope, v.ID)
}

// DeleteLead removes an Lead from the scope's tenant.
func (s *PgStore) DeleteLead(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM leads WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete lead: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FindContactByEmail returns the tenant's Contact whose email exactly
// matches (case-insensitive), or ErrNotFound.
func (s *PgStore) FindContactByEmail(ctx context.Context, scope Scope, email string) (*Contact, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrNotFound
	}
	v := &Contact{}
	err := scanContact(s.db.QueryRow(ctx, scope,
		`SELECT `+contactColumns+` FROM contacts
		 WHERE tenant_id = $1 AND lower(email) = lower($2)
		 ORDER BY id LIMIT 1`,
		scope.TenantID, email), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find contact by email: %w", err)
	}
	return v, nil
}

// FindAccountByDomain returns the tenant's Account whose Website names the
// given domain, or ErrNotFound. website_domain (migrations/0010) is a
// write-time mirror of websiteDomain(Website), kept in sync by every
// Create/UpdateAccount and indexed per-tenant, so this is a single indexed
// lookup rather than a scan-and-compare over every account in the tenant —
// domain is already normalized by the caller (account_hierarchy.go), so an
// exact match against the stored mirror is the same comparison sameDomain
// would make.
func (s *PgStore) FindAccountByDomain(ctx context.Context, scope Scope, domain string) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, ErrNotFound
	}
	v := &Account{}
	err := scanAccount(s.db.QueryRow(ctx, scope,
		`SELECT `+accountColumns+` FROM accounts
		 WHERE tenant_id = $1 AND website_domain = $2
		 ORDER BY id LIMIT 1`,
		scope.TenantID, domain), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find account by domain: %w", err)
	}
	return v, nil
}

const opportunityColumns = `id, name, account_id, stage, pipeline_id, stage_id, amount, probability, close_date, close_reason, created_at, updated_at, custom_fields`

func scanOpportunity(row pgx.Row, v *Opportunity) error {
	var customFields []byte
	if err := row.Scan(&v.ID, &v.Name, &v.AccountID, &v.Stage, &v.PipelineID, &v.StageID,
		&v.Amount, &v.Probability, &v.CloseDate, &v.CloseReason, &v.CreatedAt, &v.UpdatedAt, &customFields); err != nil {
		return err
	}
	values, err := parseCustomValues(customFields)
	if err != nil {
		return err
	}
	v.CustomFields = values
	return nil
}

// CreateOpportunity inserts a Opportunity for the scope's tenant. It
// defaults and validates the initial stage, rejects a dangling account_id,
// and — if created directly in a closed stage — enforces the win/loss rule
// (transitions.go) the same way a closing Update does.
func (s *PgStore) CreateOpportunity(ctx context.Context, scope Scope, v *Opportunity) (*Opportunity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	stage, err := prepareOpportunityStage(ctx, s, scope, v, nil)
	if err != nil {
		return nil, err
	}
	if err := requireAccount(ctx, s, scope, v.AccountID); err != nil {
		return nil, err
	}
	if stage.TerminalType != "open" {
		if err := applyConfiguredClosingRules(v, stage); err != nil {
			return nil, err
		}
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "opportunity", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now

	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO opportunities (tenant_id, id, name, account_id, stage, pipeline_id, stage_id, amount, probability, close_date, close_reason, created_at, updated_at, custom_fields) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		scope.TenantID, v.ID, v.Name, v.AccountID, v.Stage, v.PipelineID, v.StageID, v.Amount, v.Probability, v.CloseDate, v.CloseReason, v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("create opportunity: %w", err)
	}
	return cloneOpportunity(v), nil
}

// GetOpportunity reads one Opportunity belonging to the scope's tenant.
func (s *PgStore) GetOpportunity(ctx context.Context, scope Scope, id string) (*Opportunity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v := &Opportunity{}
	err := scanOpportunity(s.db.QueryRow(ctx, scope,
		`SELECT `+opportunityColumns+` FROM opportunities WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get opportunity: %w", err)
	}
	return v, nil
}

// ListOpportunities returns one keyset-paginated page of Opportunities.
func (s *PgStore) ListOpportunities(ctx context.Context, scope Scope, page Page) ([]*Opportunity, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+opportunityColumns+` FROM opportunities
		 WHERE tenant_id = $1 AND id > $2
		 ORDER BY id LIMIT $3`,
		scope.TenantID, page.Token, page.size()+1)
	if err != nil {
		return nil, "", fmt.Errorf("list opportunities: %w", err)
	}
	defer rows.Close()

	out := make([]*Opportunity, 0, page.size())
	for rows.Next() {
		v := &Opportunity{}
		if err := scanOpportunity(rows, v); err != nil {
			return nil, "", fmt.Errorf("scan opportunity: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list opportunities: %w", err)
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// ListStageHistory returns every recorded stage change for one Opportunity
// in the scope's tenant, oldest first.
func (s *PgStore) ListStageHistory(ctx context.Context, scope Scope, opportunityID string) ([]*OpportunityStageChange, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT id, opportunity_id, from_stage, to_stage, changed_at FROM opportunity_stage_history
		 WHERE tenant_id = $1 AND opportunity_id = $2
		 ORDER BY changed_at, id`,
		scope.TenantID, opportunityID)
	if err != nil {
		return nil, fmt.Errorf("list opportunity stage history: %w", err)
	}
	defer rows.Close()

	out := make([]*OpportunityStageChange, 0, 4)
	for rows.Next() {
		v := &OpportunityStageChange{}
		if err := rows.Scan(&v.ID, &v.OpportunityID, &v.FromStage, &v.ToStage, &v.ChangedAt); err != nil {
			return nil, fmt.Errorf("scan opportunity stage history: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list opportunity stage history: %w", err)
	}
	return out, nil
}

// DeleteOpportunity removes an Opportunity from the scope's tenant.
func (s *PgStore) DeleteOpportunity(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM opportunities WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete opportunity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const activityColumns = `id, kind, subject_type, subject_id, notes, actor_id, occurred_at, created_at, updated_at, custom_fields`

func scanActivity(row pgx.Row, v *Activity) error {
	var customFields []byte
	if err := row.Scan(&v.ID, &v.Kind, &v.SubjectType, &v.SubjectID, &v.Notes, &v.ActorID,
		&v.OccurredAt, &v.CreatedAt, &v.UpdatedAt, &customFields); err != nil {
		return err
	}
	values, err := parseCustomValues(customFields)
	if err != nil {
		return err
	}
	v.CustomFields = values
	return nil
}

// CreateActivity inserts a Activity for the scope's tenant. It rejects an
// unrecognized subject_type and a dangling/cross-tenant subject_id (B5 —
// activity_timeline.go): the polymorphic subject must resolve to a real,
// tenant-scoped Account, Contact, Lead or Opportunity.
func (s *PgStore) CreateActivity(ctx context.Context, scope Scope, v *Activity) (*Activity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if err := requireActivitySubject(ctx, s, scope, v.SubjectType, v.SubjectID); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "activity", v.CustomFields, nil)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		v.ID = newID()
	}
	now := nowStamp()
	v.CreatedAt, v.UpdatedAt = now, now

	_, err = s.db.Exec(ctx, scope,
		`INSERT INTO activities (tenant_id, id, kind, subject_type, subject_id, notes, actor_id, occurred_at, created_at, updated_at, custom_fields) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		scope.TenantID, v.ID, v.Kind, v.SubjectType, v.SubjectID, v.Notes, v.ActorID, v.OccurredAt, v.CreatedAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("create activity: %w", err)
	}
	return cloneActivity(v), nil
}

// GetActivity reads one Activity belonging to the scope's tenant.
func (s *PgStore) GetActivity(ctx context.Context, scope Scope, id string) (*Activity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	v := &Activity{}
	err := scanActivity(s.db.QueryRow(ctx, scope,
		`SELECT `+activityColumns+` FROM activities WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, id), v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get activity: %w", err)
	}
	return v, nil
}

// ListActivities returns one keyset-paginated page of Activities.
func (s *PgStore) ListActivities(ctx context.Context, scope Scope, page Page) ([]*Activity, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+activityColumns+` FROM activities
		 WHERE tenant_id = $1 AND id > $2
		 ORDER BY id LIMIT $3`,
		scope.TenantID, page.Token, page.size()+1)
	if err != nil {
		return nil, "", fmt.Errorf("list activities: %w", err)
	}
	defer rows.Close()

	out := make([]*Activity, 0, page.size())
	for rows.Next() {
		v := &Activity{}
		if err := scanActivity(rows, v); err != nil {
			return nil, "", fmt.Errorf("scan activity: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list activities: %w", err)
	}

	next := ""
	if len(out) > page.size() {
		out = out[:page.size()]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// UpdateActivity is a no-op unless v repeats the existing record's content
// unchanged. An Activity is immutable once logged (B5 —
// activity_timeline.go): any real change to kind, subject_type, subject_id,
// notes, actor_id or occurred_at is ErrInvalidTransition. Record a
// correction as a new Activity instead.
func (s *PgStore) UpdateActivity(ctx context.Context, scope Scope, v *Activity) (*Activity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v.ID == "" {
		return nil, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.GetActivity(ctx, scope, v.ID)
	if err != nil {
		return nil, err
	}
	if err := checkActivityImmutable(existing, v); err != nil {
		return nil, err
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "activity", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	if err := checkActivityImmutable(existing, v); err != nil {
		return nil, err
	}
	document, err := custom.MarshalDocument()
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = nowStamp()

	tag, err := s.db.Exec(ctx, scope,
		`UPDATE activities SET kind = $3, subject_type = $4, subject_id = $5, notes = $6, actor_id = $7, occurred_at = $8, updated_at = $9, custom_fields = $10
		 WHERE tenant_id = $1 AND id = $2`,
		scope.TenantID, v.ID, v.Kind, v.SubjectType, v.SubjectID, v.Notes, v.ActorID, v.OccurredAt, v.UpdatedAt, document)
	if err != nil {
		return nil, fmt.Errorf("update activity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetActivity(ctx, scope, v.ID)
}

// DeleteActivity removes an Activity from the scope's tenant.
func (s *PgStore) DeleteActivity(ctx context.Context, scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}
	tag, err := s.db.Exec(ctx, scope,
		`DELETE FROM activities WHERE tenant_id = $1 AND id = $2`, scope.TenantID, id)
	if err != nil {
		return fmt.Errorf("delete activity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListActivitiesForSubject returns every Activity attached to one
// polymorphic subject in the scope's tenant, newest first by occurred_at —
// the B5 timeline query (activity_timeline.go).
func (s *PgStore) ListActivitiesForSubject(ctx context.Context, scope Scope, subjectType, subjectID string) ([]*Activity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT id, kind, subject_type, subject_id, notes, actor_id, occurred_at, created_at, updated_at FROM activities
		 WHERE tenant_id = $1 AND subject_type = $2 AND subject_id = $3
		 ORDER BY occurred_at DESC, id DESC`,
		scope.TenantID, subjectType, subjectID)
	if err != nil {
		return nil, fmt.Errorf("list activities for subject: %w", err)
	}
	defer rows.Close()

	out := make([]*Activity, 0, 4)
	for rows.Next() {
		v := &Activity{}
		if err := rows.Scan(&v.ID, &v.Kind, &v.SubjectType, &v.SubjectID, &v.Notes, &v.ActorID, &v.OccurredAt, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan activity: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list activities for subject: %w", err)
	}
	return out, nil
}

// Summary counts this tenant's rows in every table.
func (s *PgStore) Summary(ctx context.Context, scope Scope) (Summary, error) {
	if !scope.Valid() {
		return Summary{}, ErrNoTenant
	}
	tables := map[string]string{
		"account":     "accounts",
		"contact":     "contacts",
		"lead":        "leads",
		"opportunity": "opportunities",
		"activity":    "activities",
		"task":        "tasks",
		"note":        "notes",
	}
	counts := make(map[string]int64, len(tables))
	var total int64
	for name, table := range tables {
		var n int64
		// Table names come from this generated map, never from input.
		q := "SELECT count(*) FROM " + table + " WHERE tenant_id = $1"
		if err := s.db.QueryRow(ctx, scope, q, scope.TenantID).Scan(&n); err != nil {
			return Summary{}, fmt.Errorf("count %s: %w", table, err)
		}
		counts[name] = n
		total += n
	}
	return Summary{Total: total, Counts: counts}, nil
}

// Close releases the connection pool(s).
func (s *PgStore) Close() error {
	if s.drain != nil && s.drain != s.pool {
		s.drain.Close()
	}
	s.pool.Close()
	return nil
}
