// Package domain holds this module's entities and the tenant-scoped store that
// persists them.
//
// Every operation takes a Scope. The tenant in that Scope comes from the
// caller's live authenticated membership, and every lookup is keyed by it
// first — there is no code path that reaches a record without naming its
// tenant. See ../../.claude/rules/.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors. The API layer maps these onto Connect status codes.
var (
	ErrNotFound = errors.New("domain: not found")
	ErrInvalid  = errors.New("domain: invalid record")
	ErrNoTenant = errors.New("domain: operation requires a tenant scope")

	// ErrInvalidTransition is returned when a stage change is not a legal move
	// in a state machine (transitions.go). Mapped to
	// connect.CodeFailedPrecondition in internal/api/convert.go — the request
	// itself is well-formed, it just cannot be applied from the record's
	// current state.
	ErrInvalidTransition = errors.New("domain: invalid state transition")
)

// Scope confines an operation to one tenant. An empty TenantID is always an error;
// it is never treated as "all tenants".
type Scope struct {
	TenantID string
	OrgID    string
}

// Valid reports whether the scope can be used for a store operation.
func (s Scope) Valid() bool { return strings.TrimSpace(s.TenantID) != "" }

// Page requests one page of results. Token is the ID the previous page ended on.
type Page struct {
	Size  int
	Token string
}

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

func (p Page) size() int {
	if p.Size <= 0 {
		return defaultPageSize
	}
	if p.Size > maxPageSize {
		return maxPageSize
	}
	return p.Size
}

// Entity is implemented by every record in this package.
type Entity interface {
	EntityID() string
	SetEntityID(string)
	SetTimestamps(created, updated time.Time)
	Validate() error
}

// Summary is a per-entity record count for the module dashboard.
type Summary struct {
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
}

// Account is a account record.
type Account struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Industry      string    `json:"industry"`
	Tier          string    `json:"tier"`
	OwnerID       string    `json:"owner_id"`
	Website       string    `json:"website"`
	AnnualRevenue float64   `json:"annual_revenue"`
	ParentID      string    `json:"parent_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	// DuplicateOfAccountID is set (by CreateAccount/UpdateAccount — see
	// account_hierarchy.go) when Website's domain exactly matches another
	// tenant-scoped Account's Website at write time. It flags the account;
	// it never blocks or silently merges it (B3 —
	// docs/customer-os/PLAN.md), the same convention Lead uses for
	// DuplicateOfContactID.
	DuplicateOfAccountID string       `json:"duplicate_of_account_id"`
	CustomFields         CustomValues `json:"custom_fields"`
}

// EntityID returns the record's identifier.
func (e *Account) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Account) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Account) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Field limits enforced by Account.Validate() / Contact.Validate(). These
// exist to give the web form and the RPC boundary the same, single source
// of truth for "too long to store" — not to encode a business rule beyond
// "must fit reasonably in the UI and the column."
const (
	maxNameLen     = 200
	maxIndustryLen = 100
	maxTierLen     = 50
	maxWebsiteLen  = 2048
	maxEmailLen    = 320 // RFC 5321 §4.5.3.1.3 upper bound
	maxPhoneLen    = 32
	maxTitleLen    = 200
)

// Validate rejects records that cannot be stored.
func (e *Account) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: Account.name is required", ErrInvalid)
	}
	if len(e.Name) > maxNameLen {
		return fmt.Errorf("%w: Account.name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if len(e.Industry) > maxIndustryLen {
		return fmt.Errorf("%w: Account.industry exceeds %d characters", ErrInvalid, maxIndustryLen)
	}
	if len(e.Tier) > maxTierLen {
		return fmt.Errorf("%w: Account.tier exceeds %d characters", ErrInvalid, maxTierLen)
	}
	if len(e.Website) > maxWebsiteLen {
		return fmt.Errorf("%w: Account.website exceeds %d characters", ErrInvalid, maxWebsiteLen)
	}
	if e.AnnualRevenue < 0 {
		return fmt.Errorf("%w: Account.annual_revenue cannot be negative", ErrInvalid)
	}
	return nil
}

func cloneAccount(v *Account) *Account {
	if v == nil {
		return nil
	}
	c := *v
	c.CustomFields = v.CustomFields.Clone()
	return &c
}

// Contact is a contact record.
type Contact struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	AccountID string    `json:"account_id"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	Phone     string    `json:"phone"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Role is this contact's role at the account — one of the values in
	// contactRoles (contact_integrity.go): billing, technical,
	// decision_maker, other. Empty is allowed (role not yet known); a
	// non-empty value outside that set is ErrInvalid. A plain validated
	// field, not a workflow.
	Role string `json:"role"`

	// DuplicateOfContactID is set (by CreateContact/UpdateContact — see
	// contact_integrity.go) when Email exactly matches another
	// tenant-scoped Contact's email at write time. It flags the contact; it
	// never blocks or silently merges it (B4 — docs/customer-os/PLAN.md),
	// the same convention Lead uses for its own DuplicateOfContactID and
	// Account uses for DuplicateOfAccountID.
	DuplicateOfContactID string       `json:"duplicate_of_contact_id"`
	CustomFields         CustomValues `json:"custom_fields"`
}

// EntityID returns the record's identifier.
func (e *Contact) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Contact) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Contact) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Validate rejects records that cannot be stored.
func (e *Contact) Validate() error {
	email := strings.TrimSpace(e.Email)
	phone := strings.TrimSpace(e.Phone)
	if email == "" && phone == "" {
		return fmt.Errorf("%w: Contact.email or Contact.phone is required", ErrInvalid)
	}
	if len(e.Email) > maxEmailLen {
		return fmt.Errorf("%w: Contact.email exceeds %d characters", ErrInvalid, maxEmailLen)
	}
	if email != "" && !looksLikeEmail(email) {
		return fmt.Errorf("%w: Contact.email is not a valid email address", ErrInvalid)
	}
	// A phone-only contact has no second identity key to fall back to, so its
	// number must be globally unambiguous. Existing email-addressed contacts
	// may retain a local-format phone and the messaging UI will explain why it
	// cannot be used until corrected.
	if email == "" {
		if _, err := NormalizeE164Phone(phone); err != nil {
			return fmt.Errorf("%w: Contact.phone %v", ErrInvalid, err)
		}
	}
	if len(e.FirstName) > maxNameLen {
		return fmt.Errorf("%w: Contact.first_name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if len(e.LastName) > maxNameLen {
		return fmt.Errorf("%w: Contact.last_name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if len(e.Phone) > maxPhoneLen {
		return fmt.Errorf("%w: Contact.phone exceeds %d characters", ErrInvalid, maxPhoneLen)
	}
	if len(e.Title) > maxTitleLen {
		return fmt.Errorf("%w: Contact.title exceeds %d characters", ErrInvalid, maxTitleLen)
	}
	return nil
}

// looksLikeEmail is a deliberately light check — "user@domain", one "@",
// something on both sides, a dot somewhere in the domain part. It exists to
// catch obvious typos and empty-ish input, not to fully validate RFC 5321 —
// the only real verification of an email address is sending mail to it.
func looksLikeEmail(email string) bool {
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if strings.ContainsAny(local, " \t\n") || strings.ContainsAny(domain, " \t\n") {
		return false
	}
	return strings.Contains(domain, ".")
}

func cloneContact(v *Contact) *Contact {
	if v == nil {
		return nil
	}
	c := *v
	c.CustomFields = v.CustomFields.Clone()
	return &c
}

// Lead is a lead record.
type Lead struct {
	ID           string    `json:"id"`
	FirstName    string    `json:"first_name"`
	LastName     string    `json:"last_name"`
	Phone        string    `json:"phone"`
	Company      string    `json:"company"`
	Source       string    `json:"source"`
	ContactEmail string    `json:"contact_email"`
	Status       string    `json:"status"`
	Score        int64     `json:"score"`
	OwnerID      string    `json:"owner_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// DuplicateOfContactID is set (by CreateLead/UpdateLead — see
	// lead_lifecycle.go) when ContactEmail exactly matches an existing
	// Contact's email in this tenant at write time. It flags the lead; it
	// never blocks or silently merges it (B1 — docs/customer-os/PLAN.md).
	DuplicateOfContactID string `json:"duplicate_of_contact_id"`

	// ConvertedContactID, ConvertedAccountID and ConvertedOpportunityID are
	// populated exactly once, by ConvertLead (lead_conversion.go), the
	// instant this lead's Status becomes converted. Empty until then.
	// UpdateLead cannot set Status to converted directly, and cannot set
	// these — only ConvertLead may.
	ConvertedContactID     string       `json:"converted_contact_id"`
	ConvertedAccountID     string       `json:"converted_account_id"`
	ConvertedOpportunityID string       `json:"converted_opportunity_id"`
	CustomFields           CustomValues `json:"custom_fields"`
}

// EntityID returns the record's identifier.
func (e *Lead) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Lead) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Lead) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Validate rejects records that cannot be stored.
func (e *Lead) Validate() error {
	if e == nil {
		return fmt.Errorf("%w: no Lead", ErrInvalid)
	}
	if strings.TrimSpace(e.FirstName) == "" && strings.TrimSpace(e.LastName) == "" &&
		strings.TrimSpace(e.Company) == "" && strings.TrimSpace(e.ContactEmail) == "" &&
		strings.TrimSpace(e.Phone) == "" {
		return fmt.Errorf("%w: Lead needs a name, company, email, or phone", ErrInvalid)
	}
	if len(e.FirstName) > maxNameLen {
		return fmt.Errorf("%w: Lead.first_name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if len(e.LastName) > maxNameLen {
		return fmt.Errorf("%w: Lead.last_name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if len(e.Company) > maxNameLen {
		return fmt.Errorf("%w: Lead.company exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if len(e.ContactEmail) > maxEmailLen {
		return fmt.Errorf("%w: Lead.contact_email exceeds %d characters", ErrInvalid, maxEmailLen)
	}
	if strings.TrimSpace(e.ContactEmail) != "" && !looksLikeEmail(e.ContactEmail) {
		return fmt.Errorf("%w: Lead.contact_email is not a valid email address", ErrInvalid)
	}
	if len(e.Phone) > maxPhoneLen {
		return fmt.Errorf("%w: Lead.phone exceeds %d characters", ErrInvalid, maxPhoneLen)
	}
	if strings.TrimSpace(e.Phone) != "" {
		if _, err := NormalizeE164Phone(e.Phone); err != nil {
			return fmt.Errorf("%w: Lead.phone %v", ErrInvalid, err)
		}
	}
	return nil
}

func cloneLead(v *Lead) *Lead {
	if v == nil {
		return nil
	}
	c := *v
	c.CustomFields = v.CustomFields.Clone()
	return &c
}

// Opportunity is a opportunity record.
type Opportunity struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	AccountID    string       `json:"account_id"`
	Stage        string       `json:"stage"`
	PipelineID   string       `json:"pipeline_id"`
	StageID      string       `json:"stage_id"`
	Amount       float64      `json:"amount"`
	Probability  float64      `json:"probability"`
	CloseDate    time.Time    `json:"close_date"`
	CloseReason  string       `json:"close_reason"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	CustomFields CustomValues `json:"custom_fields"`
}

// MoveOpportunityToPipelineInput names every value needed to relocate an
// open opportunity without accepting a full, stale replacement document.
// Destination selection is explicit: inferring a stage from its position or
// name can put a deal in the wrong part of a differently configured pipeline.
type MoveOpportunityToPipelineInput struct {
	OpportunityID string
	PipelineID    string
	StageID       string
	UpdatedAt     time.Time
}

// EntityID returns the record's identifier.
func (e *Opportunity) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Opportunity) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Opportunity) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Validate rejects records that cannot be stored.
func (e *Opportunity) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: Opportunity.name is required", ErrInvalid)
	}
	return nil
}

func cloneOpportunity(v *Opportunity) *Opportunity {
	if v == nil {
		return nil
	}
	c := *v
	c.CustomFields = v.CustomFields.Clone()
	return &c
}

// Pipeline is a tenant-owned sales-process definition. Archived pipelines are
// retained for reporting and cannot be removed from history.
type Pipeline struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	IsDefault  bool      `json:"is_default"`
	ArchivedAt time.Time `json:"archived_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (e *Pipeline) EntityID() string      { return e.ID }
func (e *Pipeline) SetEntityID(id string) { e.ID = id }
func (e *Pipeline) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}
func (e *Pipeline) Created() time.Time { return e.CreatedAt }
func (e *Pipeline) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: Pipeline.name is required", ErrInvalid)
	}
	if len(e.Name) > maxNameLen {
		return fmt.Errorf("%w: Pipeline.name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	return nil
}
func clonePipeline(v *Pipeline) *Pipeline {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// PipelineStage is an ordered stage in one Pipeline. TerminalType is kept as
// an explicit classification instead of inferred from position, so pipelines
// can branch from open stages while reports retain a stable won/lost meaning.
type PipelineStage struct {
	ID                  string    `json:"id"`
	PipelineID          string    `json:"pipeline_id"`
	Name                string    `json:"name"`
	Position            int       `json:"position"`
	Probability         float64   `json:"probability"`
	TerminalType        string    `json:"terminal_type"`
	RequiresCloseReason bool      `json:"requires_close_reason"`
	ArchivedAt          time.Time `json:"archived_at"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (e *PipelineStage) EntityID() string      { return e.ID }
func (e *PipelineStage) SetEntityID(id string) { e.ID = id }
func (e *PipelineStage) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}
func (e *PipelineStage) Created() time.Time { return e.CreatedAt }
func (e *PipelineStage) Validate() error {
	if strings.TrimSpace(e.PipelineID) == "" {
		return fmt.Errorf("%w: PipelineStage.pipeline_id is required", ErrInvalid)
	}
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%w: PipelineStage.name is required", ErrInvalid)
	}
	if len(e.Name) > maxNameLen {
		return fmt.Errorf("%w: PipelineStage.name exceeds %d characters", ErrInvalid, maxNameLen)
	}
	if e.Position < 0 {
		return fmt.Errorf("%w: PipelineStage.position cannot be negative", ErrInvalid)
	}
	if e.Probability < 0 || e.Probability > 100 {
		return fmt.Errorf("%w: PipelineStage.probability must be between 0 and 100", ErrInvalid)
	}
	if e.TerminalType == "" {
		e.TerminalType = "open"
	}
	if e.TerminalType != "open" && e.TerminalType != "won" && e.TerminalType != "lost" {
		return fmt.Errorf("%w: PipelineStage.terminal_type is invalid", ErrInvalid)
	}
	if e.TerminalType == "won" && e.Probability != 100 {
		return fmt.Errorf("%w: won PipelineStage probability must be 100", ErrInvalid)
	}
	if e.TerminalType == "lost" && e.Probability != 0 {
		return fmt.Errorf("%w: lost PipelineStage probability must be 0", ErrInvalid)
	}
	return nil
}
func clonePipelineStage(v *PipelineStage) *PipelineStage {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// OpportunityStageChange is one append-only record of an Opportunity moving
// from one pipeline stage to another (B2 — see transitions.go). Unlike the
// entities above, this is never last-write-wins: the pipeline needs to answer
// "where was this deal on date X" and "how long did it sit in each stage",
// which only a durable log of every change — not the current row — can
// answer.
type OpportunityStageChange struct {
	ID            string    `json:"id"`
	OpportunityID string    `json:"opportunity_id"`
	FromStage     string    `json:"from_stage"`
	ToStage       string    `json:"to_stage"`
	ChangedAt     time.Time `json:"changed_at"`
}

// EntityID returns the record's identifier.
func (e *OpportunityStageChange) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *OpportunityStageChange) SetEntityID(id string) { e.ID = id }

// SetTimestamps is a no-op beyond ChangedAt, which the writer stamps
// explicitly: a stage-history row records when the transition happened, not
// when the row happened to be created or touched again.
func (e *OpportunityStageChange) SetTimestamps(_, _ time.Time) {}

// Created satisfies memTable's ordering helper. Stage-history rows are
// immutable, so "created" and "changed" are the same instant.
func (e *OpportunityStageChange) Created() time.Time { return e.ChangedAt }

// Validate rejects a stage-history record that cannot be stored.
func (e *OpportunityStageChange) Validate() error {
	if strings.TrimSpace(e.OpportunityID) == "" {
		return fmt.Errorf("%w: OpportunityStageChange.opportunity_id is required", ErrInvalid)
	}
	if strings.TrimSpace(e.ToStage) == "" {
		return fmt.Errorf("%w: OpportunityStageChange.to_stage is required", ErrInvalid)
	}
	return nil
}

func cloneOpportunityStageChange(v *OpportunityStageChange) *OpportunityStageChange {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// AccountOwnershipHistory is one append-only record of an Account's owner_id
// changing (B3 — see account_hierarchy.go). Like OpportunityStageChange
// above, this is never last-write-wins: territory reporting needs to answer
// "who owned this account, and when", which only a durable log of every
// change — not the current row — can answer. Written only when
// UpdateAccount actually changes OwnerID; a no-op update appends nothing.
type AccountOwnershipHistory struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	FromOwnerID string    `json:"from_owner_id"`
	ToOwnerID   string    `json:"to_owner_id"`
	ChangedAt   time.Time `json:"changed_at"`
}

// EntityID returns the record's identifier.
func (e *AccountOwnershipHistory) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *AccountOwnershipHistory) SetEntityID(id string) { e.ID = id }

// SetTimestamps is a no-op beyond ChangedAt, which the writer stamps
// explicitly: an ownership-history row records when the change happened,
// not when the row happened to be created or touched again.
func (e *AccountOwnershipHistory) SetTimestamps(_, _ time.Time) {}

// Created satisfies memTable's ordering helper. Ownership-history rows are
// immutable, so "created" and "changed" are the same instant.
func (e *AccountOwnershipHistory) Created() time.Time { return e.ChangedAt }

// Validate rejects an ownership-history record that cannot be stored.
func (e *AccountOwnershipHistory) Validate() error {
	if strings.TrimSpace(e.AccountID) == "" {
		return fmt.Errorf("%w: AccountOwnershipHistory.account_id is required", ErrInvalid)
	}
	if strings.TrimSpace(e.ToOwnerID) == "" {
		return fmt.Errorf("%w: AccountOwnershipHistory.to_owner_id is required", ErrInvalid)
	}
	return nil
}

func cloneAccountOwnershipHistory(v *AccountOwnershipHistory) *AccountOwnershipHistory {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// Activity is a activity record.
type Activity struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	SubjectType  string       `json:"subject_type"`
	SubjectID    string       `json:"subject_id"`
	Notes        string       `json:"notes"`
	ActorID      string       `json:"actor_id"`
	OccurredAt   time.Time    `json:"occurred_at"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	CustomFields CustomValues `json:"custom_fields"`
}

// EntityID returns the record's identifier.
func (e *Activity) EntityID() string { return e.ID }

// SetEntityID assigns the record's identifier.
func (e *Activity) SetEntityID(id string) { e.ID = id }

// SetTimestamps stamps the record. A zero created keeps the existing value.
func (e *Activity) SetTimestamps(created, updated time.Time) {
	if !created.IsZero() {
		e.CreatedAt = created
	}
	e.UpdatedAt = updated
}

// Validate rejects records that cannot be stored.
func (e *Activity) Validate() error {
	if strings.TrimSpace(e.Kind) == "" {
		return fmt.Errorf("%w: Activity.kind is required", ErrInvalid)
	}
	return nil
}

func cloneActivity(v *Activity) *Activity {
	if v == nil {
		return nil
	}
	c := *v
	c.CustomFields = v.CustomFields.Clone()
	return &c
}
