package domain

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// memTable is one tenant-partitioned table. The outer map key is the tenant, so a
// lookup cannot reach another tenant's rows even by guessing an ID.
type memTable[T Entity] struct {
	mu    sync.RWMutex
	rows  map[string]map[string]T
	clone func(T) T
}

func newMemTable[T Entity](clone func(T) T) *memTable[T] {
	return &memTable[T]{rows: make(map[string]map[string]T), clone: clone}
}

func (t *memTable[T]) create(scope Scope, v T) (T, error) {
	var zero T
	if !scope.Valid() {
		return zero, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return zero, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if v.EntityID() == "" {
		v.SetEntityID(newID())
	}
	now := nowStamp()
	v.SetTimestamps(now, now)

	if _, ok := t.rows[scope.TenantID]; !ok {
		t.rows[scope.TenantID] = make(map[string]T)
	}
	t.rows[scope.TenantID][v.EntityID()] = t.clone(v)
	return t.clone(v), nil
}

func (t *memTable[T]) get(scope Scope, id string) (T, error) {
	var zero T
	if !scope.Valid() {
		return zero, ErrNoTenant
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	row, ok := t.rows[scope.TenantID][id]
	if !ok {
		return zero, ErrNotFound
	}
	return t.clone(row), nil
}

func (t *memTable[T]) list(scope Scope, page Page) ([]T, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	ids := make([]string, 0, len(t.rows[scope.TenantID]))
	for id := range t.rows[scope.TenantID] {
		if page.Token == "" || id > page.Token {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	limit := page.size()
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}

	out := make([]T, 0, len(ids))
	for _, id := range ids {
		out = append(out, t.clone(t.rows[scope.TenantID][id]))
	}
	return out, next, nil
}

func (t *memTable[T]) update(scope Scope, v T) (T, error) {
	var zero T
	if !scope.Valid() {
		return zero, ErrNoTenant
	}
	if v.EntityID() == "" {
		return zero, ErrNotFound
	}
	if err := v.Validate(); err != nil {
		return zero, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	existing, ok := t.rows[scope.TenantID][v.EntityID()]
	if !ok {
		return zero, ErrNotFound
	}
	v.SetTimestamps(created(existing), nowStamp())
	t.rows[scope.TenantID][v.EntityID()] = t.clone(v)
	return t.clone(v), nil
}

func (t *memTable[T]) delete(scope Scope, id string) error {
	if !scope.Valid() {
		return ErrNoTenant
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.rows[scope.TenantID][id]; !ok {
		return ErrNotFound
	}
	delete(t.rows[scope.TenantID], id)
	return nil
}

func (t *memTable[T]) count(scope Scope) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return int64(len(t.rows[scope.TenantID]))
}

// all returns every row for the scope's tenant, unpaginated and unordered by
// caller-visible key (callers sort as their query needs). Used by the outbox,
// which reads its whole tenant slice rather than a page.
func (t *memTable[T]) all(scope Scope) ([]T, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	ids := make([]string, 0, len(t.rows[scope.TenantID]))
	for id := range t.rows[scope.TenantID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]T, 0, len(ids))
	for _, id := range ids {
		out = append(out, t.clone(t.rows[scope.TenantID][id]))
	}
	return out, nil
}

// created reads an entity's CreatedAt so updates preserve it. Entities are structs
// with a CreatedAt field; the interface deliberately exposes only the setter, so
// this reads through the timestamped interface instead.
func created[T Entity](v T) time.Time {
	if ts, ok := any(v).(interface{ Created() time.Time }); ok {
		return ts.Created()
	}
	return time.Time{}
}

// MemStore is the in-memory Store. It is the default when DATABASE_URL is unset and
// is intended for development and tests — it holds nothing across a restart.
type MemStore struct {
	// compound serializes the write paths that are more than one table
	// operation — ConvertLead, UpdateLead and UpdateOpportunity. Each
	// memTable has its own lock, which makes a single row operation atomic
	// but says nothing about a read-decide-write sequence spanning several
	// of them: two concurrent conversions of one lead would both read it as
	// qualified and both create a contact/account/opportunity, and two
	// concurrent stage advances would both decide their transition from the
	// same "from" stage and write two stage-history rows leaving it.
	//
	// PgStore closes the same two races with SELECT ... FOR UPDATE on the
	// lead/opportunity row (pgstore_lead_conversion.go,
	// pgstore_opportunity.go). This is the in-memory equivalent: coarser
	// than a per-row lock, which is the right trade for a store whose whole
	// point is being simple.
	compound sync.Mutex

	account          *memTable[*Account]
	contact          *memTable[*Contact]
	lead             *memTable[*Lead]
	opportunity      *memTable[*Opportunity]
	pipeline         *memTable[*Pipeline]
	pipelineStage    *memTable[*PipelineStage]
	activity         *memTable[*Activity]
	task             *memTable[*Task]
	note             *memTable[*Note]
	outbox           *memTable[*OutboxEvent]
	stageHistory     *memTable[*OpportunityStageChange]
	ownershipHistory *memTable[*AccountOwnershipHistory]
	customField      *memTable[*CustomFieldDefinition]
	customObject     *memTable[*CustomObjectDefinition]
	customRecord     *memTable[*CustomRecord]
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		account:          newMemTable(cloneAccount),
		contact:          newMemTable(cloneContact),
		lead:             newMemTable(cloneLead),
		opportunity:      newMemTable(cloneOpportunity),
		pipeline:         newMemTable(clonePipeline),
		pipelineStage:    newMemTable(clonePipelineStage),
		activity:         newMemTable(cloneActivity),
		task:             newMemTable(cloneTask),
		note:             newMemTable(cloneNote),
		outbox:           newMemTable(cloneOutboxEvent),
		stageHistory:     newMemTable(cloneOpportunityStageChange),
		ownershipHistory: newMemTable(cloneAccountOwnershipHistory),
		customField:      newMemTable(cloneCustomFieldDefinition),
		customObject:     newMemTable(cloneCustomObjectDefinition),
		customRecord:     newMemTable(cloneCustomRecord),
	}
}

// CreateAccount stores a new Account for the scope's tenant. It requires
// owner_id, rejects a dangling or cyclic parent_id (account_hierarchy.go),
// and flags (never blocks) an exact Website-domain match against another
// tenant-scoped Account.
func (s *MemStore) CreateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error) {
	return s.createAccount(ctx, scope, v, true)
}

// createAccount is CreateAccount's real implementation. requireOwner is
// false only for ConvertLead's internal account creation
// (resolveOrCreateConversionAccount): that Account has no user-supplied
// owner to check, and B1's conversion is not the "who owns this account"
// decision B3 is auditing.
func (s *MemStore) createAccount(ctx context.Context, scope Scope, v *Account, requireOwner bool) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if requireOwner {
		if err := requireAccountOwner(v); err != nil {
			return nil, err
		}
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
	return s.account.create(scope, v)
}

// GetAccount returns one Account belonging to the scope's tenant.
func (s *MemStore) GetAccount(_ context.Context, scope Scope, id string) (*Account, error) {
	return s.account.get(scope, id)
}

// accountLess orders two Accounts per sortBy, tie-broken by ID so the order
// is total (required for stable keyset pagination — see decodeCursor's
// caller in ListAccounts).
func accountLess(sortBy AccountSort) func(a, b *Account) bool {
	if sortBy == SortAccountCreatedDesc {
		return func(a, b *Account) bool {
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.ID > b.ID
		}
	}
	return func(a, b *Account) bool {
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	}
}

func matchesAccountFilter(a *Account, f AccountFilter) bool {
	if f.NameContains != "" && !strings.Contains(strings.ToLower(a.Name), strings.ToLower(f.NameContains)) {
		return false
	}
	if f.Tier != "" && a.Tier != f.Tier {
		return false
	}
	if f.OwnerID != "" && a.OwnerID != f.OwnerID {
		return false
	}
	if f.ParentID != "" && a.ParentID != f.ParentID {
		return false
	}
	return true
}

// accountCursorBoundary rebuilds a synthetic Account carrying just enough of
// the sort key and ID from a decoded cursor to be compared with accountLess
// — reusing the exact comparator that produced the sort avoids a second,
// separately-maintained "is this after the cursor" rule that could drift
// out of sync with it.
func accountCursorBoundary(sortValue, id string, sortBy AccountSort) *Account {
	b := &Account{ID: id}
	if sortBy == SortAccountCreatedDesc {
		if t, err := time.Parse(time.RFC3339Nano, sortValue); err == nil {
			b.CreatedAt = t
		}
	} else {
		b.Name = sortValue
	}
	return b
}

// ListAccounts returns one page of Accounts, filtered and ordered per sortBy.
func (s *MemStore) ListAccounts(_ context.Context, scope Scope, page Page, filter AccountFilter, sortBy AccountSort) ([]*Account, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	all, err := s.account.all(scope)
	if err != nil {
		return nil, "", err
	}

	less := accountLess(sortBy)
	rows := make([]*Account, 0, len(all))
	for _, a := range all {
		if matchesAccountFilter(a, filter) {
			rows = append(rows, a)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })

	start := 0
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		boundary := accountCursorBoundary(sortValue, afterID, sortBy)
		start = sort.Search(len(rows), func(i int) bool { return less(boundary, rows[i]) })
	}
	rows = rows[start:]

	limit := page.size()
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(accountSortValue(last, sortBy), last.ID)
	}
	return rows, next, nil
}

// UpdateAccount replaces an existing Account. It requires owner_id, rejects
// a dangling or cyclic parent_id, refreshes the duplicate-domain flag, and —
// when the write actually changes OwnerID — appends a row to the
// account_ownership_history log (account_hierarchy.go / migrations/0007).
//
// Optimistic concurrency: a non-zero v.UpdatedAt must match the stored
// record's UpdatedAt, or the write is rejected with ErrConflict. A caller
// that does not round-trip UpdatedAt (the zero value) skips the check —
// this is what every pre-existing caller does, so introducing the check
// could not break them; only a client that opts in by sending back the
// value it last read is subject to it.
func (s *MemStore) UpdateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existing, err := s.account.get(scope, v.EntityID())
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Account %s was modified since it was last read", ErrConflict, v.EntityID())
	}
	if err := requireAccountOwner(v); err != nil {
		return nil, err
	}
	if err := requireValidAccountParent(ctx, s, scope, v.EntityID(), v.ParentID); err != nil {
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

	updated, err := s.account.update(scope, v)
	if err != nil {
		return nil, err
	}

	if existing.OwnerID != updated.OwnerID {
		if _, err := s.ownershipHistory.create(scope, &AccountOwnershipHistory{
			AccountID:   updated.ID,
			FromOwnerID: existing.OwnerID,
			ToOwnerID:   updated.OwnerID,
			ChangedAt:   nowStamp(),
		}); err != nil {
			return nil, fmt.Errorf("record account ownership history: %w", err)
		}
	}

	return updated, nil
}

// DeleteAccount removes an Account from the scope's tenant.
func (s *MemStore) DeleteAccount(_ context.Context, scope Scope, id string) error {
	return s.account.delete(scope, id)
}

// ListOwnershipHistory returns every recorded owner_id change for one
// Account in the scope's tenant, oldest first.
func (s *MemStore) ListOwnershipHistory(_ context.Context, scope Scope, accountID string) ([]*AccountOwnershipHistory, error) {
	all, err := s.ownershipHistory.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*AccountOwnershipHistory, 0, 4)
	for _, h := range all {
		if h.AccountID == accountID {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ChangedAt.Equal(out[j].ChangedAt) {
			return out[i].ChangedAt.Before(out[j].ChangedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// CreateContact stores a new Contact for the scope's tenant. It rejects a
// dangling or cross-tenant account_id, rejects an unknown role value, and
// flags (never blocks) an exact-email match against another tenant-scoped
// Contact (B4 — docs/customer-os/PLAN.md, contact_integrity.go).
func (s *MemStore) CreateContact(ctx context.Context, scope Scope, v *Contact) (*Contact, error) {
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
	return s.contact.create(scope, v)
}

// GetContact returns one Contact belonging to the scope's tenant.
func (s *MemStore) GetContact(_ context.Context, scope Scope, id string) (*Contact, error) {
	return s.contact.get(scope, id)
}

// contactLess orders two Contacts per sortBy, tie-broken by ID.
func contactLess(sortBy ContactSort) func(a, b *Contact) bool {
	if sortBy == SortContactEmail {
		return func(a, b *Contact) bool {
			if a.Email != b.Email {
				return a.Email < b.Email
			}
			return a.ID < b.ID
		}
	}
	return func(a, b *Contact) bool {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	}
}

func matchesContactFilter(c *Contact, f ContactFilter) bool {
	if f.AccountID != "" && c.AccountID != f.AccountID {
		return false
	}
	if f.NameContains != "" {
		q := strings.ToLower(f.NameContains)
		if !strings.Contains(strings.ToLower(c.FirstName), q) && !strings.Contains(strings.ToLower(c.LastName), q) {
			return false
		}
	}
	if f.EmailContains != "" && !strings.Contains(strings.ToLower(c.Email), strings.ToLower(f.EmailContains)) {
		return false
	}
	return true
}

func contactCursorBoundary(sortValue, id string, sortBy ContactSort) *Contact {
	b := &Contact{ID: id}
	if sortBy == SortContactEmail {
		b.Email = sortValue
	} else if t, err := time.Parse(time.RFC3339Nano, sortValue); err == nil {
		b.CreatedAt = t
	}
	return b
}

// ListContacts returns one page of Contacts, filtered and ordered per sortBy.
func (s *MemStore) ListContacts(_ context.Context, scope Scope, page Page, filter ContactFilter, sortBy ContactSort) ([]*Contact, string, error) {
	if !scope.Valid() {
		return nil, "", ErrNoTenant
	}
	all, err := s.contact.all(scope)
	if err != nil {
		return nil, "", err
	}

	less := contactLess(sortBy)
	rows := make([]*Contact, 0, len(all))
	for _, c := range all {
		if matchesContactFilter(c, filter) {
			rows = append(rows, c)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })

	start := 0
	if page.Token != "" {
		sortValue, afterID, ok := decodeCursor(page.Token)
		if !ok {
			return nil, "", fmt.Errorf("%w: invalid page_token", ErrInvalid)
		}
		boundary := contactCursorBoundary(sortValue, afterID, sortBy)
		start = sort.Search(len(rows), func(i int) bool { return less(boundary, rows[i]) })
	}
	rows = rows[start:]

	limit := page.size()
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(contactSortValue(last, sortBy), last.ID)
	}
	return rows, next, nil
}

// UpdateContact replaces an existing Contact. account_id is re-checked on
// every update, not only when it changes — the same convention
// UpdateOpportunity uses for its own account_id — and the duplicate-email
// flag is recomputed so it never goes stale.
//
// Optimistic concurrency: same convention as UpdateAccount above — a
// non-zero v.UpdatedAt must match the stored record's, or ErrConflict.
func (s *MemStore) UpdateContact(ctx context.Context, scope Scope, v *Contact) (*Contact, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	existingContact, err := s.contact.get(scope, v.EntityID())
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existingContact.UpdatedAt) {
		return nil, fmt.Errorf("%w: Contact %s was modified since it was last read", ErrConflict, v.EntityID())
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
	custom, err := CoerceCustomValues(ctx, s, scope, "contact", v.CustomFields, existingContact.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom
	return s.contact.update(scope, v)
}

// DeleteContact removes an Contact from the scope's tenant.
func (s *MemStore) DeleteContact(_ context.Context, scope Scope, id string) error {
	return s.contact.delete(scope, id)
}

// CreateLead stores a new Lead for the scope's tenant. It defaults and
// validates Status (B1's lifecycle machine — lead_lifecycle.go) and flags
// (never blocks) an exact-email match against an existing Contact.
func (s *MemStore) CreateLead(ctx context.Context, scope Scope, v *Lead) (*Lead, error) {
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
	return s.lead.create(scope, v)
}

// GetLead returns one Lead belonging to the scope's tenant.
func (s *MemStore) GetLead(_ context.Context, scope Scope, id string) (*Lead, error) {
	return s.lead.get(scope, id)
}

// ListLeads returns one page of Leads, ordered by ID.
func (s *MemStore) ListLeads(_ context.Context, scope Scope, page Page) ([]*Lead, string, error) {
	return s.lead.list(scope, page)
}

// UpdateLead replaces an existing Lead, enforcing the status state machine
// and refreshing the duplicate-contact flag. Status can never be set to
// converted directly through this path — only ConvertLead may perform that
// transition, since it is the one that also creates the linked records.
//
// Optimistic concurrency: a non-zero v.UpdatedAt must match the stored
// record's, or the write is rejected with ErrConflict — the same contract as
// UpdateAccount.
func (s *MemStore) UpdateLead(ctx context.Context, scope Scope, v *Lead) (*Lead, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}

	s.compound.Lock()
	defer s.compound.Unlock()

	existing, err := s.lead.get(scope, v.EntityID())
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Lead %s was modified since it was last read", ErrConflict, v.EntityID())
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
	return s.lead.update(scope, v)
}

// DeleteLead removes an Lead from the scope's tenant.
func (s *MemStore) DeleteLead(_ context.Context, scope Scope, id string) error {
	return s.lead.delete(scope, id)
}

// FindContactByEmail returns the tenant's Contact whose email exactly
// matches (case-insensitive), or ErrNotFound. Scans the tenant's own
// contacts only (memTable.all is already keyed by scope.TenantID), ordered
// by id so a tie resolves deterministically.
func (s *MemStore) FindContactByEmail(_ context.Context, scope Scope, email string) (*Contact, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrNotFound
	}
	all, err := s.contact.all(scope)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if sameEmail(c.Email, email) {
			return c, nil
		}
	}
	return nil, ErrNotFound
}

// FindAccountByDomain returns the tenant's Account whose Website names the
// given domain, or ErrNotFound.
func (s *MemStore) FindAccountByDomain(_ context.Context, scope Scope, domain string) (*Account, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, ErrNotFound
	}
	all, err := s.account.all(scope)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if sameDomain(websiteDomain(a.Website), domain) {
			return a, nil
		}
	}
	return nil, ErrNotFound
}

// ConvertLead performs the B1 conversion (lead_conversion.go) sequentially
// against MemStore's tables. MemStore holds no cross-table transaction (see
// memTable's own doc comment): the guarantee here is the same one
// UpdateOpportunity already relies on for its compound writes — one
// goroutine driving these calls in order — not multi-writer atomicity.
// PgStore.ConvertLead is the one that actually runs inside a database
// transaction.
func (s *MemStore) ConvertLead(ctx context.Context, scope Scope, in ConvertLeadInput) (*ConvertLeadResult, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}

	s.compound.Lock()
	defer s.compound.Unlock()

	lead, err := s.lead.get(scope, in.LeadID)
	if err != nil {
		return nil, err
	}

	// Idempotent retry: a lead already converted returns exactly what the
	// first successful call produced, creating nothing new.
	if lead.Status == LeadStatusConverted {
		return s.loadConversionResult(ctx, scope, lead)
	}

	if err := checkLeadStatusTransition(lead.Status, LeadStatusConverted); err != nil {
		return nil, err
	}

	email, err := requireLeadContactEmail(lead.ContactEmail)
	if err != nil {
		return nil, err
	}

	contact, account, err := s.resolveConversionContactAndAccount(ctx, scope, lead, email)
	if err != nil {
		return nil, err
	}

	var opp *Opportunity
	if in.CreateOpportunity {
		o := &Opportunity{
			Name:      conversionOpportunityName(in.OpportunityName, lead.Company),
			AccountID: account.ID,
			Amount:    in.OpportunityAmount,
		}
		opp, err = s.CreateOpportunity(ctx, scope, o)
		if err != nil {
			return nil, fmt.Errorf("create conversion opportunity: %w", err)
		}
	}

	lead.Status = LeadStatusConverted
	lead.ConvertedContactID = contact.ID
	lead.ConvertedAccountID = account.ID
	if opp != nil {
		lead.ConvertedOpportunityID = opp.ID
	}
	// A direct table write, not s.UpdateLead: the exported path refuses to
	// set Status to converted, which is exactly the transition this method
	// exists to perform.
	updated, err := s.lead.update(scope, lead)
	if err != nil {
		return nil, fmt.Errorf("mark lead converted: %w", err)
	}

	return &ConvertLeadResult{Lead: updated, Contact: contact, Account: account, Opportunity: opp}, nil
}

// resolveConversionContactAndAccount implements the Contact/Account half of
// ConvertLead: reuse an exact-email Contact match verbatim (including
// whatever Account it already carries), or create a fresh Contact attached
// to a domain-matched or newly created Account.
func (s *MemStore) resolveConversionContactAndAccount(
	ctx context.Context, scope Scope, lead *Lead, email string,
) (*Contact, *Account, error) {
	existing, err := s.FindContactByEmail(ctx, scope, email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	if existing != nil {
		account, aerr := s.GetAccount(ctx, scope, existing.AccountID)
		if aerr == nil {
			return existing, account, nil
		}
		if !errors.Is(aerr, ErrNotFound) {
			return nil, nil, aerr
		}
		// The reused contact has no valid account (dangling or never set):
		// attach it, same as the fresh-contact path below.
		account, err = s.resolveOrCreateConversionAccount(ctx, scope, lead.Company, emailDomain(email))
		if err != nil {
			return nil, nil, err
		}
		existing.AccountID = account.ID
		updated, err := s.UpdateContact(ctx, scope, existing)
		if err != nil {
			return nil, nil, err
		}
		return updated, account, nil
	}

	account, err := s.resolveOrCreateConversionAccount(ctx, scope, lead.Company, emailDomain(email))
	if err != nil {
		return nil, nil, err
	}
	contact, err := s.CreateContact(ctx, scope, &Contact{Email: email, AccountID: account.ID})
	if err != nil {
		return nil, nil, fmt.Errorf("create conversion contact: %w", err)
	}
	return contact, account, nil
}

// resolveOrCreateConversionAccount matches an existing Account by email
// domain, or creates a new one.
func (s *MemStore) resolveOrCreateConversionAccount(
	ctx context.Context, scope Scope, company, domain string,
) (*Account, error) {
	if domain != "" {
		acct, err := s.FindAccountByDomain(ctx, scope, domain)
		if err == nil {
			return acct, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	acct, err := s.createAccount(ctx, scope, newConversionAccount(company, domain), false)
	if err != nil {
		return nil, fmt.Errorf("create conversion account: %w", err)
	}
	return acct, nil
}

// loadConversionResult re-reads the Contact/Account/Opportunity a prior
// ConvertLead call already linked, for the idempotent-retry path.
func (s *MemStore) loadConversionResult(ctx context.Context, scope Scope, lead *Lead) (*ConvertLeadResult, error) {
	contact, err := s.GetContact(ctx, scope, lead.ConvertedContactID)
	if err != nil {
		return nil, fmt.Errorf("load converted contact: %w", err)
	}
	account, err := s.GetAccount(ctx, scope, lead.ConvertedAccountID)
	if err != nil {
		return nil, fmt.Errorf("load converted account: %w", err)
	}
	var opp *Opportunity
	if lead.ConvertedOpportunityID != "" {
		opp, err = s.GetOpportunity(ctx, scope, lead.ConvertedOpportunityID)
		if err != nil {
			return nil, fmt.Errorf("load converted opportunity: %w", err)
		}
	}
	return &ConvertLeadResult{Lead: lead, Contact: contact, Account: account, Opportunity: opp}, nil
}

// CreateOpportunity stores a new Opportunity for the scope's tenant. It
// defaults and validates the initial stage, rejects a dangling account_id,
// and — if created directly in a closed stage — enforces the win/loss rule
// (transitions.go) the same way a closing Update does.
func (s *MemStore) CreateOpportunity(ctx context.Context, scope Scope, v *Opportunity) (*Opportunity, error) {
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
	return s.opportunity.create(scope, v)
}

// GetOpportunity returns one Opportunity belonging to the scope's tenant.
func (s *MemStore) GetOpportunity(_ context.Context, scope Scope, id string) (*Opportunity, error) {
	return s.opportunity.get(scope, id)
}

// ListOpportunities returns one page of Opportunities, ordered by ID.
func (s *MemStore) ListOpportunities(_ context.Context, scope Scope, page Page) ([]*Opportunity, string, error) {
	return s.opportunity.list(scope, page)
}

// UpdateOpportunity replaces an existing Opportunity, enforcing the stage
// state machine, re-checking the account_id reference, applying the win/loss
// rule on a fresh transition into a closed stage, recording stage history,
// and — on the specific transition into closed_won — publishing
// opportunity.closed_won.
//
// Optimistic concurrency: a non-zero v.UpdatedAt must match the stored
// record's, or the write is rejected with ErrConflict — the same contract as
// UpdateAccount.
func (s *MemStore) UpdateOpportunity(ctx context.Context, scope Scope, v *Opportunity) (*Opportunity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}

	s.compound.Lock()
	defer s.compound.Unlock()

	existing, err := s.opportunity.get(scope, v.EntityID())
	if err != nil {
		return nil, err
	}
	if !v.UpdatedAt.IsZero() && !v.UpdatedAt.Equal(existing.UpdatedAt) {
		return nil, fmt.Errorf("%w: Opportunity %s was modified since it was last read", ErrConflict, v.EntityID())
	}
	stage, err := prepareOpportunityStage(ctx, s, scope, v, existing)
	if err != nil {
		return nil, err
	}
	fromStage, err := activePipelineStage(ctx, s, scope, existing.PipelineID, existing.StageID)
	if err != nil {
		return nil, err
	}
	if err := checkConfiguredStageTransition(fromStage, stage); err != nil {
		return nil, err
	}
	// account_id is re-checked on every update, not only when it changes: a
	// dangling reference introduced after this Opportunity was created (the
	// account it named was deleted) must be caught here too.
	if err := requireAccount(ctx, s, scope, v.AccountID); err != nil {
		return nil, err
	}

	becameClosed := existing.StageID != v.StageID && stage.TerminalType != "open"
	if becameClosed {
		if err := applyConfiguredClosingRules(v, stage); err != nil {
			return nil, err
		}
	}
	custom, err := CoerceCustomValues(ctx, s, scope, "opportunity", v.CustomFields, existing.CustomFields)
	if err != nil {
		return nil, err
	}
	v.CustomFields = custom

	updated, err := s.opportunity.update(scope, v)
	if err != nil {
		return nil, err
	}

	if existing.StageID != updated.StageID {
		if _, err := s.stageHistory.create(scope, &OpportunityStageChange{
			OpportunityID: updated.ID,
			FromStage:     existing.Stage,
			ToStage:       updated.Stage,
			ChangedAt:     nowStamp(),
		}); err != nil {
			return nil, fmt.Errorf("record opportunity stage history: %w", err)
		}
	}

	if becameClosed && stage.TerminalType == "won" {
		if err := enqueueOpportunityClosedWon(ctx, s, scope, updated); err != nil {
			return nil, err
		}
	}

	return updated, nil
}

func (s *MemStore) defaultOpportunityPipeline(ctx context.Context, scope Scope) (*Pipeline, error) {
	pipelines, err := s.ListPipelines(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, pipeline := range pipelines {
		if pipeline.IsDefault && pipeline.ArchivedAt.IsZero() {
			return pipeline, nil
		}
	}
	pipeline, err := s.CreatePipeline(ctx, scope, &Pipeline{ID: systemSalesPipelineID, Name: "Sales", IsDefault: true})
	if err != nil {
		return nil, err
	}
	for _, definition := range systemSalesStages {
		stage := definition
		stage.PipelineID = pipeline.ID
		if _, err := s.CreatePipelineStage(ctx, scope, &stage); err != nil {
			return nil, err
		}
	}
	return pipeline, nil
}

// DeleteOpportunity removes an Opportunity from the scope's tenant.
func (s *MemStore) DeleteOpportunity(_ context.Context, scope Scope, id string) error {
	return s.opportunity.delete(scope, id)
}

func (s *MemStore) CreatePipeline(_ context.Context, scope Scope, v *Pipeline) (*Pipeline, error) {
	return s.pipeline.create(scope, v)
}

func (s *MemStore) GetPipeline(_ context.Context, scope Scope, id string) (*Pipeline, error) {
	return s.pipeline.get(scope, id)
}

func (s *MemStore) ListPipelines(_ context.Context, scope Scope) ([]*Pipeline, error) {
	return s.pipeline.all(scope)
}

func (s *MemStore) UpdatePipeline(ctx context.Context, scope Scope, v *Pipeline) (*Pipeline, error) {
	existing, err := s.pipeline.get(scope, v.ID)
	if err != nil {
		return nil, err
	}
	if err := checkPipelineArchival(ctx, s, scope, existing, v); err != nil {
		return nil, err
	}
	if v.IsDefault && v.ArchivedAt.IsZero() {
		all, err := s.pipeline.all(scope)
		if err != nil {
			return nil, err
		}
		for _, candidate := range all {
			if candidate.ID != v.ID && candidate.IsDefault && candidate.ArchivedAt.IsZero() {
				candidate.IsDefault = false
				if _, err := s.pipeline.update(scope, candidate); err != nil {
					return nil, err
				}
			}
		}
	}
	return s.pipeline.update(scope, v)
}

func (s *MemStore) CreatePipelineStage(ctx context.Context, scope Scope, v *PipelineStage) (*PipelineStage, error) {
	pipeline, err := s.GetPipeline(ctx, scope, v.PipelineID)
	if err != nil {
		return nil, err
	}
	if !pipeline.ArchivedAt.IsZero() {
		return nil, fmt.Errorf("%w: cannot add a stage to an archived pipeline", ErrInvalid)
	}
	return s.pipelineStage.create(scope, v)
}

func (s *MemStore) ListPipelineStages(_ context.Context, scope Scope, pipelineID string) ([]*PipelineStage, error) {
	if _, err := s.pipeline.get(scope, pipelineID); err != nil {
		return nil, err
	}
	stages, err := s.pipelineStage.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*PipelineStage, 0, len(stages))
	for _, stage := range stages {
		if stage.PipelineID == pipelineID {
			out = append(out, stage)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Position < out[j].Position || (out[i].Position == out[j].Position && out[i].ID < out[j].ID)
	})
	return out, nil
}

func (s *MemStore) UpdatePipelineStage(ctx context.Context, scope Scope, v *PipelineStage) (*PipelineStage, error) {
	pipeline, err := s.GetPipeline(ctx, scope, v.PipelineID)
	if err != nil {
		return nil, err
	}
	if !pipeline.ArchivedAt.IsZero() {
		return nil, fmt.Errorf("%w: cannot update a stage in an archived pipeline", ErrInvalid)
	}
	existing, err := s.pipelineStage.get(scope, v.ID)
	if err != nil {
		return nil, err
	}
	if err := checkStageArchival(ctx, s, scope, existing, v); err != nil {
		return nil, err
	}
	return s.pipelineStage.update(scope, v)
}

// ListStageHistory returns every recorded stage change for one Opportunity
// in the scope's tenant, oldest first.
func (s *MemStore) ListStageHistory(_ context.Context, scope Scope, opportunityID string) ([]*OpportunityStageChange, error) {
	all, err := s.stageHistory.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*OpportunityStageChange, 0, 4)
	for _, h := range all {
		if h.OpportunityID == opportunityID {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ChangedAt.Equal(out[j].ChangedAt) {
			return out[i].ChangedAt.Before(out[j].ChangedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// CreateActivity stores a new Activity for the scope's tenant. It rejects an
// unrecognized subject_type and a dangling/cross-tenant subject_id (B5 —
// activity_timeline.go): the polymorphic subject must resolve to a real,
// tenant-scoped Account, Contact, Lead or Opportunity.
func (s *MemStore) CreateActivity(ctx context.Context, scope Scope, v *Activity) (*Activity, error) {
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
	return s.activity.create(scope, v)
}

// GetActivity returns one Activity belonging to the scope's tenant.
func (s *MemStore) GetActivity(_ context.Context, scope Scope, id string) (*Activity, error) {
	return s.activity.get(scope, id)
}

// ListActivities returns one page of Activities, ordered by ID.
func (s *MemStore) ListActivities(_ context.Context, scope Scope, page Page) ([]*Activity, string, error) {
	return s.activity.list(scope, page)
}

// UpdateActivity is a no-op unless v repeats the existing record's content
// unchanged. An Activity is immutable once logged (B5 —
// activity_timeline.go): any real change to kind, subject_type, subject_id,
// notes, actor_id or occurred_at is ErrInvalidTransition. Record a
// correction as a new Activity instead.
func (s *MemStore) UpdateActivity(ctx context.Context, scope Scope, v *Activity) (*Activity, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	existing, err := s.activity.get(scope, v.EntityID())
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
	return s.activity.update(scope, v)
}

// DeleteActivity removes an Activity from the scope's tenant.
func (s *MemStore) DeleteActivity(_ context.Context, scope Scope, id string) error {
	return s.activity.delete(scope, id)
}

// ListActivitiesForSubject returns every Activity attached to one
// polymorphic subject in the scope's tenant, newest first by occurred_at —
// the B5 timeline query (activity_timeline.go).
func (s *MemStore) ListActivitiesForSubject(_ context.Context, scope Scope, subjectType, subjectID string) ([]*Activity, error) {
	all, err := s.activity.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*Activity, 0, 4)
	for _, a := range all {
		if a.SubjectType == subjectType && a.SubjectID == subjectID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.After(out[j].OccurredAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// Summary counts this tenant's records in every table.
func (s *MemStore) Summary(_ context.Context, scope Scope) (Summary, error) {
	if !scope.Valid() {
		return Summary{}, ErrNoTenant
	}
	counts := map[string]int64{
		"account":     s.account.count(scope),
		"contact":     s.contact.count(scope),
		"lead":        s.lead.count(scope),
		"opportunity": s.opportunity.count(scope),
		"activity":    s.activity.count(scope),
		"task":        s.task.count(scope),
		"note":        s.note.count(scope),
	}
	var total int64
	for _, c := range counts {
		total += c
	}
	return Summary{Total: total, Counts: counts}, nil
}

// Close is a no-op; the in-memory store holds no external resources.
func (s *MemStore) Close() error { return nil }
