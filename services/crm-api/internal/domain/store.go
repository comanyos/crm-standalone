package domain

import (
	"context"
	"time"
)

// Store persists this module's entities. Both MemStore and PgStore implement it, so
// handlers never learn which one they are talking to.
//
// Every method takes a Scope and must reject an empty tenant. Adding a method that
// skips the tenant key is a cross-tenant data leak, not a shortcut.
type Store interface {
	CreateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error)
	GetAccount(ctx context.Context, scope Scope, id string) (*Account, error)
	ListAccounts(ctx context.Context, scope Scope, page Page, filter AccountFilter, sort AccountSort) ([]*Account, string, error)
	UpdateAccount(ctx context.Context, scope Scope, v *Account) (*Account, error)
	DeleteAccount(ctx context.Context, scope Scope, id string) error
	// ListOwnershipHistory returns every recorded owner_id change for one
	// Account, oldest first. Written append-only by UpdateAccount whenever a
	// write actually changes OwnerID — see account_hierarchy.go.
	ListOwnershipHistory(ctx context.Context, scope Scope, accountID string) ([]*AccountOwnershipHistory, error)
	CreateContact(ctx context.Context, scope Scope, v *Contact) (*Contact, error)
	GetContact(ctx context.Context, scope Scope, id string) (*Contact, error)
	ListContacts(ctx context.Context, scope Scope, page Page, filter ContactFilter, sort ContactSort) ([]*Contact, string, error)
	UpdateContact(ctx context.Context, scope Scope, v *Contact) (*Contact, error)
	DeleteContact(ctx context.Context, scope Scope, id string) error
	CreateLead(ctx context.Context, scope Scope, v *Lead) (*Lead, error)
	GetLead(ctx context.Context, scope Scope, id string) (*Lead, error)
	ListLeads(ctx context.Context, scope Scope, page Page) ([]*Lead, string, error)
	UpdateLead(ctx context.Context, scope Scope, v *Lead) (*Lead, error)
	DeleteLead(ctx context.Context, scope Scope, id string) error
	// FindContactByEmail returns the tenant's Contact whose email exactly
	// matches (case-insensitive), or ErrNotFound. Used both for Lead exact-key
	// dedup flagging and by ConvertLead to reuse rather than duplicate an
	// existing Contact.
	FindContactByEmail(ctx context.Context, scope Scope, email string) (*Contact, error)
	// FindAccountByDomain returns the tenant's Account whose Website names the
	// given domain, or ErrNotFound. Used by ConvertLead to attach a newly
	// created Contact to an existing Account instead of creating a duplicate
	// one.
	FindAccountByDomain(ctx context.Context, scope Scope, domain string) (*Account, error)
	// ConvertLead is the B1 load-bearing operation: in one transaction it
	// creates (or reuses, by exact email match) a Contact, attaches it to an
	// Account (existing match by email domain, or a new one), optionally
	// creates an Opportunity, and marks the Lead converted with the linked
	// ids. Calling it again on an already-converted lead id is a no-op that
	// returns the ids the first successful call produced — see
	// lead_conversion.go.
	ConvertLead(ctx context.Context, scope Scope, in ConvertLeadInput) (*ConvertLeadResult, error)
	CreateOpportunity(ctx context.Context, scope Scope, v *Opportunity) (*Opportunity, error)
	GetOpportunity(ctx context.Context, scope Scope, id string) (*Opportunity, error)
	ListOpportunities(ctx context.Context, scope Scope, page Page) ([]*Opportunity, string, error)
	UpdateOpportunity(ctx context.Context, scope Scope, v *Opportunity) (*Opportunity, error)
	DeleteOpportunity(ctx context.Context, scope Scope, id string) error
	CreatePipeline(ctx context.Context, scope Scope, v *Pipeline) (*Pipeline, error)
	GetPipeline(ctx context.Context, scope Scope, id string) (*Pipeline, error)
	ListPipelines(ctx context.Context, scope Scope) ([]*Pipeline, error)
	UpdatePipeline(ctx context.Context, scope Scope, v *Pipeline) (*Pipeline, error)
	CreatePipelineStage(ctx context.Context, scope Scope, v *PipelineStage) (*PipelineStage, error)
	ListPipelineStages(ctx context.Context, scope Scope, pipelineID string) ([]*PipelineStage, error)
	UpdatePipelineStage(ctx context.Context, scope Scope, v *PipelineStage) (*PipelineStage, error)
	// ListStageHistory returns every recorded stage change for one
	// Opportunity, oldest first. Written append-only by UpdateOpportunity
	// whenever a write actually changes Stage — see transitions.go.
	ListStageHistory(ctx context.Context, scope Scope, opportunityID string) ([]*OpportunityStageChange, error)
	CreateActivity(ctx context.Context, scope Scope, v *Activity) (*Activity, error)
	GetActivity(ctx context.Context, scope Scope, id string) (*Activity, error)
	ListActivities(ctx context.Context, scope Scope, page Page) ([]*Activity, string, error)
	UpdateActivity(ctx context.Context, scope Scope, v *Activity) (*Activity, error)
	DeleteActivity(ctx context.Context, scope Scope, id string) error
	// ListActivitiesForSubject returns every Activity attached to one
	// polymorphic subject (subject_type + subject_id) in the scope's
	// tenant, newest first by occurred_at — the timeline query (B5 —
	// docs/customer-os/PLAN.md, internal/domain/activity_timeline.go).
	ListActivitiesForSubject(ctx context.Context, scope Scope, subjectType, subjectID string) ([]*Activity, error)

	// Summary returns per-entity counts for the calling tenant only.
	Summary(ctx context.Context, scope Scope) (Summary, error)

	// enqueueOutboxEvent writes one durable event for later delivery. It is
	// unexported: EnqueueEvent (event_outbox.go) is the public seam Track B
	// calls; callers outside this package never touch the outbox row shape
	// directly.
	enqueueOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error

	// DueOutboxTenants is the one method here allowed to cross tenants: its job
	// is to discover which tenants have work waiting, not to read any tenant's
	// data. Every other outbox call takes a real Scope. See rls.go / pgstore's
	// drain pool for how PgStore keeps that boundary under RLS.
	DueOutboxTenants(ctx context.Context, now time.Time) ([]string, error)
	DueOutboxEvents(ctx context.Context, scope Scope, now time.Time) ([]*OutboxEvent, error)
	UpdateOutboxEvent(ctx context.Context, scope Scope, ev *OutboxEvent) error
	// OutboxEventsFor returns every event ever enqueued about one entity id,
	// oldest first.
	OutboxEventsFor(ctx context.Context, scope Scope, entityID string) ([]*OutboxEvent, error)

	// Close releases any resources the store holds.
	Close() error
}
