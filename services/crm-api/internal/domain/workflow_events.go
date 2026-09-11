package domain

import (
	"context"
	"time"
)

// The trigger events workflow automation fires on.
//
// opportunity.closed_won already existed for Track C; these two are added
// because a rule engine with one trigger is not one anybody can use. Each is
// published from the store's write path in the same place the corresponding
// history row is written, so an event cannot exist for a change that did not
// happen, or be missed for one that did.
//
// They are ordinary outbox envelopes: the bus dispatcher publishes them and
// the workflow engine reads the same durable log from its own cursor. Nothing
// here knows automation exists, which is what keeps the trigger catalogue a
// property of the engine rather than of the write path.

// LeadCreatedPayload is what a lead.created event carries: the fields a rule
// may compare, and nothing else. A snapshot, not a record — actions re-read
// their target under the execution's own scope before they touch it.
type LeadCreatedPayload struct {
	LeadID    string `json:"lead_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	OwnerID   string `json:"owner_id"`
	Score     int64  `json:"score"`
}

// OpportunityStageChangedPayload is what a stage change carries.
type OpportunityStageChangedPayload struct {
	OpportunityID string  `json:"opportunity_id"`
	AccountID     string  `json:"account_id"`
	PipelineID    string  `json:"pipeline_id"`
	Name          string  `json:"name"`
	FromStage     string  `json:"from_stage"`
	Stage         string  `json:"stage"`
	Amount        float64 `json:"amount"`
}

// enqueueLeadCreated publishes lead.created.
//
// source_version is 1: a lead is created once, so there is no revision to
// version past the first — the same argument opportunity.closed_won makes.
func enqueueLeadCreated(ctx context.Context, store eventEnqueuer, scope Scope, lead *Lead) error {
	env := Envelope{
		ID:            newID(),
		Type:          EventLeadCreated,
		TenantID:      scope.TenantID,
		CorrelationID: newID(),
		SourceApp:     SourceAppCRM,
		EntityType:    SubjectLead,
		EntityID:      lead.ID,
		SourceVersion: 1,
		Actor:         actorOr(lead.OwnerID),
		OccurredAt:    nowStamp(),
	}
	env, err := env.WithPayload(LeadCreatedPayload{
		LeadID: lead.ID, FirstName: lead.FirstName, LastName: lead.LastName,
		Company: lead.Company, Source: lead.Source,
		Status: lead.Status, OwnerID: lead.OwnerID, Score: lead.Score,
	})
	if err != nil {
		return err
	}
	_, err = EnqueueEvent(ctx, store, scope, env)
	return err
}

// enqueueOpportunityStageChanged publishes opportunity.stage_changed.
//
// source_version is the change's own time in microseconds. A deal moves
// stages repeatedly, so a fixed 1 would collide on the outbox's uniqueness
// key and silently drop every move after the first — which for automation
// would mean a rule that fires once per deal and then never again.
func enqueueOpportunityStageChanged(
	ctx context.Context, store eventEnqueuer, scope Scope,
	opportunity *Opportunity, fromStage string, at time.Time,
) error {
	env := Envelope{
		ID:            newID(),
		Type:          EventOpportunityStageChanged,
		TenantID:      scope.TenantID,
		CorrelationID: newID(),
		SourceApp:     SourceAppCRM,
		EntityType:    EntityOpportunity,
		EntityID:      opportunity.ID,
		SourceVersion: at.UTC().UnixMicro(),
		Actor:         "system",
		OccurredAt:    at.UTC(),
	}
	env, err := env.WithPayload(OpportunityStageChangedPayload{
		OpportunityID: opportunity.ID, AccountID: opportunity.AccountID,
		PipelineID: opportunity.PipelineID, Name: opportunity.Name,
		FromStage: fromStage, Stage: opportunity.Stage, Amount: opportunity.Amount,
	})
	if err != nil {
		return err
	}
	_, err = EnqueueEvent(ctx, store, scope, env)
	return err
}

// actorOr keeps Envelope.Validate's "attributed" rule satisfied when a record
// carries no owner.
func actorOr(actor string) string {
	if actor == "" {
		return "system"
	}
	return actor
}

// WorkflowEventFields projects an event payload into the flat map a rule's
// conditions compare against.
//
// It is a projection rather than the payload itself so the condition
// allowlist (workflowFields) and the event payloads cannot drift: a field a
// rule may name must appear here, and one that does not is unknown to the
// evaluator, which treats it as unsatisfied rather than true.
func WorkflowEventFields(eventType string, payload map[string]any) map[string]string {
	out := make(map[string]string, 8)
	objectType := ""
	if trigger, ok := workflowTriggers[eventType]; ok {
		objectType = trigger.ObjectType
	}
	for field := range workflowFields[objectType] {
		if value, present := payload[field]; present {
			out[field] = scalarString(value)
		}
	}
	return out
}
