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
	MoveOpportunityToPipeline(ctx context.Context, scope Scope, in MoveOpportunityToPipelineInput) (*Opportunity, error)
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

	// Task and Note are first-class entities rather than Activity kinds
	// (task.md §5.1): an Activity is immutable because it records what
	// happened, while a Task is edited until it is done and a Note is
	// corrected as understanding improves. See task.go and note.go.
	CreateTask(ctx context.Context, scope Scope, v *Task) (*Task, error)
	GetTask(ctx context.Context, scope Scope, id string) (*Task, error)
	ListTasks(ctx context.Context, scope Scope, page Page, filter TaskFilter, sort TaskSort) ([]*Task, string, error)
	UpdateTask(ctx context.Context, scope Scope, v *Task) (*Task, error)
	DeleteTask(ctx context.Context, scope Scope, id string) error
	// CompleteTask and ReopenTask are separate from UpdateTask because
	// ticking a checkbox should not be a read-modify-write of the whole
	// record — under optimistic concurrency that either fails against a
	// concurrent edit or clobbers one. Both are idempotent.
	CompleteTask(ctx context.Context, scope Scope, id, actor string) (*Task, error)
	ReopenTask(ctx context.Context, scope Scope, id string) (*Task, error)

	CreateNote(ctx context.Context, scope Scope, v *Note) (*Note, error)
	GetNote(ctx context.Context, scope Scope, id string) (*Note, error)
	ListNotes(ctx context.Context, scope Scope, page Page, filter NoteFilter) ([]*Note, string, error)
	UpdateNote(ctx context.Context, scope Scope, v *Note) (*Note, error)
	DeleteNote(ctx context.Context, scope Scope, id string) error

	// The metadata layer's definitions (task_v2 Phase 1.1). These describe
	// tenant-authored fields; the values they describe live in a jsonb
	// document on the row they belong to, not behind these methods. See
	// custom_field.go and docs/DESIGN_METADATA_LAYER.md.
	CreateCustomFieldDefinition(ctx context.Context, scope Scope, v *CustomFieldDefinition) (*CustomFieldDefinition, error)
	GetCustomFieldDefinition(ctx context.Context, scope Scope, id string) (*CustomFieldDefinition, error)
	// ListCustomFieldDefinitions returns one object's fields, ordered by
	// field key. includeArchived is explicit rather than a filter struct
	// because every write path wants live fields only and must not be able to
	// pick up an archived one by forgetting to set something.
	ListCustomFieldDefinitions(ctx context.Context, scope Scope, objectType string, includeArchived bool) ([]*CustomFieldDefinition, error)
	// UpdateCustomFieldDefinition changes a field's label, requiredness, and
	// type-specific config. ObjectType, FieldKey and DataType are immutable
	// after creation — see UpdateCustomFieldDefinition's implementation for
	// why each one is.
	UpdateCustomFieldDefinition(ctx context.Context, scope Scope, v *CustomFieldDefinition) (*CustomFieldDefinition, error)
	// ArchiveCustomFieldDefinition retires a field. There is deliberately no
	// delete: removing the definition would orphan values already written
	// into rows under its key. Idempotent.
	ArchiveCustomFieldDefinition(ctx context.Context, scope Scope, id string) (*CustomFieldDefinition, error)

	CreateCustomObjectDefinition(ctx context.Context, scope Scope, v *CustomObjectDefinition) (*CustomObjectDefinition, error)
	GetCustomObjectDefinition(ctx context.Context, scope Scope, apiName string) (*CustomObjectDefinition, error)
	ListCustomObjectDefinitions(ctx context.Context, scope Scope, includeArchived bool) ([]*CustomObjectDefinition, error)
	ArchiveCustomObjectDefinition(ctx context.Context, scope Scope, apiName string) (*CustomObjectDefinition, error)
	CreateCustomRecord(ctx context.Context, scope Scope, v *CustomRecord) (*CustomRecord, error)
	GetCustomRecord(ctx context.Context, scope Scope, objectType, id string) (*CustomRecord, error)
	ListCustomRecords(ctx context.Context, scope Scope, objectType string, page Page) ([]*CustomRecord, string, error)
	UpdateCustomRecord(ctx context.Context, scope Scope, v *CustomRecord) (*CustomRecord, error)
	DeleteCustomRecord(ctx context.Context, scope Scope, objectType, id string) error

	// The Google communication integration's durable state
	// (docs/GOOGLE_INTEGRATION.md, migrations/0019_google_connections.sql).
	// The store persists sealed bytes: every credential and PKCE verifier is
	// encrypted by internal/integration/googleoauth before it gets here, so
	// no implementation of this interface can open one.
	CreateGoogleOAuthState(ctx context.Context, scope Scope, v *GoogleOAuthState) (*GoogleOAuthState, error)
	// ConsumeGoogleOAuthState redeems a pending authorization exactly once
	// and atomically. A second call for the same state — which is what a
	// replayed OAuth redirect is — returns ErrNotFound, as does an expired
	// one: the caller is unauthenticated, so it must not learn which.
	ConsumeGoogleOAuthState(ctx context.Context, scope Scope, state string, now time.Time) (*GoogleOAuthState, error)
	PurgeExpiredGoogleOAuthStates(ctx context.Context, scope Scope, now time.Time) (int, error)
	// ResolveGoogleOAuthStateTenant is the second method here allowed to
	// cross tenants, and it is held to the same rule as DueOutboxTenants: it
	// answers only "which tenant does this opaque, unguessable state belong
	// to". Every read and write that follows takes a real Scope built from
	// the answer.
	ResolveGoogleOAuthStateTenant(ctx context.Context, state string) (string, error)
	// SaveGoogleConnection upserts the connection for v.OwnerID. There is at
	// most one per owner per tenant: a second row would mean a second live
	// refresh token for the same mailbox that no disconnect would revoke.
	SaveGoogleConnection(ctx context.Context, scope Scope, v *GoogleConnection) (*GoogleConnection, error)
	GetGoogleConnection(ctx context.Context, scope Scope, ownerID string) (*GoogleConnection, error)
	ListGoogleConnections(ctx context.Context, scope Scope) ([]*GoogleConnection, error)
	DeleteGoogleConnection(ctx context.Context, scope Scope, ownerID string) error

	// Gmail synchronisation (migrations/0020_gmail_sync.sql).
	GetGmailSyncState(ctx context.Context, scope Scope, ownerID string) (*GmailSyncState, error)
	SaveGmailSyncState(ctx context.Context, scope Scope, v *GmailSyncState) (*GmailSyncState, error)
	// ClaimGmailSync leases one mailbox for one sync pass, so two replicas
	// polling the same tenant do not both walk it and race to insert the same
	// timeline entries. It returns ErrConflict when the lease is already
	// held. The lease expires rather than needing release: a replica that
	// dies mid-pass must not make a mailbox permanently unsyncable.
	ClaimGmailSync(ctx context.Context, scope Scope, ownerID string, now time.Time, lease time.Duration) (*GmailSyncState, error)
	// DueGmailMailboxes is the third method allowed to cross tenants, held to
	// the same rule as DueOutboxTenants: it answers only "which mailboxes are
	// unclaimed and least recently synced", returning identifiers. Every read
	// and write that follows takes a real Scope built from them.
	DueGmailMailboxes(ctx context.Context, now time.Time, limit int) ([]GmailMailboxRef, error)
	// RecordGmailActivity writes one timeline entry: the Activity and the
	// index row that makes it idempotent, in one transaction. It reports
	// false when the entry already existed, which is the normal outcome of
	// re-syncing a message. The two writes are one fact — an Activity with no
	// index row is a duplicate waiting to be created on the next pass, and an
	// index row with no Activity is a timeline entry that silently never
	// appeared.
	RecordGmailActivity(ctx context.Context, scope Scope, msg *GmailMessage, activity *Activity) (bool, error)
	ListGmailMessagesForSubject(ctx context.Context, scope Scope, subjectType, subjectID string, limit int) ([]*GmailMessage, error)
	// PurgeGmailDataForOwner drops one mailbox's sync cursor, and optionally
	// the timeline index rows as well.
	//
	// Disconnecting passes false, which is the surprising half and the
	// important one. The index is what remembers which messages have already
	// been posted to a timeline; deleting it would mean a user who
	// disconnects and reconnects gets a second copy of every email in their
	// CRM. Keeping it costs a few rows and makes reconnection safe.
	//
	// The Activities are never touched either way. They are CRM records the
	// organization has since worked from, and deleting a customer's
	// correspondence history because somebody unlinked a mailbox would be a
	// surprise, not a privacy win. Erasing synced content on request is a
	// separate operation this module does not have yet.
	PurgeGmailDataForOwner(ctx context.Context, scope Scope, ownerID string, includeIndexedMessages bool) (int, error)

	// Google Calendar synchronisation (migrations/0021_calendar_sync.sql).
	GetCalendarSyncState(ctx context.Context, scope Scope, ownerID string) (*CalendarSyncState, error)
	SaveCalendarSyncState(ctx context.Context, scope Scope, v *CalendarSyncState) (*CalendarSyncState, error)
	ClaimCalendarSync(ctx context.Context, scope Scope, ownerID string, now time.Time, lease time.Duration) (*CalendarSyncState, error)
	// DueCalendarMailboxes is the fourth and last cross-tenant read, held to
	// the same rule as the others: identifiers only.
	DueCalendarMailboxes(ctx context.Context, now time.Time, limit int) ([]GmailMailboxRef, error)
	// RecordCalendarEvent mirrors one event and appends a timeline entry when
	// the material state changed.
	//
	// It takes a factory rather than a prepared Activity because whether an
	// Activity is needed at all — and what it should say — depends on a
	// comparison with the stored row that only this call can make. Building
	// one eagerly would mean either writing it when nothing changed, or
	// discarding one on every unchanged event, which is most of them.
	RecordCalendarEvent(ctx context.Context, scope Scope, event *CalendarEvent, activityFor func(CalendarChange) *Activity) (CalendarChange, error)
	ListCalendarEventsForSubject(ctx context.Context, scope Scope, subjectType, subjectID string, limit int) ([]*CalendarEvent, error)
	// PurgeCalendarDataForOwner mirrors PurgeGmailDataForOwner, including why
	// disconnecting passes false: the mirror is what remembers which events
	// have already been posted, so dropping it would repost every meeting on
	// reconnection.
	PurgeCalendarDataForOwner(ctx context.Context, scope Scope, ownerID string, includeIndexedEvents bool) (int, error)

	// Summary returns per-entity counts for the calling tenant only.
	Summary(ctx context.Context, scope Scope) (Summary, error)
	// SearchRecords is the bounded cross-object quick-search surface. Results
	// are always restricted to scope.TenantID; it intentionally searches only
	// core identifying fields, leaving exhaustive per-entity filtering to the
	// entity list APIs.
	SearchRecords(ctx context.Context, scope Scope, query string, limit int) ([]SearchRecord, error)

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
