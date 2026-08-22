package domain

import (
	"context"
	"time"
)

// Producer half of Track C chain C3 (docs/customer-os/PLAN.md: "opportunity
// closed_won -> billing" — win a deal, create the subscription/invoice).
//
// NOTE: there is NO consumer for this event anywhere in the suite yet.
// billing does not subscribe to it. This file only makes CRM publish the
// event durably the instant a deal is won; a future reader must not assume
// anything is listening on the other end.

// EntityOpportunity identifies Opportunity envelopes on this module's event
// bus subject: crm.opportunity.<tenant> (see Envelope.Subject).
const EntityOpportunity = "opportunity"

// EventOpportunityClosedWon is published exactly once per Opportunity: the
// instant (and only the instant) its stage transitions into closed_won. No
// other stage transition — including closed_lost — publishes anything.
const EventOpportunityClosedWon = EntityOpportunity + ".closed_won"

// OpportunityClosedWonPayload is the event body. owner_id is resolved from
// the Opportunity's Account (see forecast.go's ownerOfAccount) since
// Opportunity carries no owner field of its own in this schema.
type OpportunityClosedWonPayload struct {
	OpportunityID string    `json:"opportunity_id"`
	AccountID     string    `json:"account_id"`
	Amount        float64   `json:"amount"`
	OwnerID       string    `json:"owner_id"`
	CloseDate     time.Time `json:"close_date"`
}

// closedWonDeps is what enqueueOpportunityClosedWon needs from a store: the
// account read to resolve owner_id, and the outbox write to publish
// durably. Both MemStore and PgStore already implement it.
type closedWonDeps interface {
	accountReader
	EventOutboxStore
}

// enqueueOpportunityClosedWon writes the durable opportunity.closed_won
// event. It is called from the store's write path the instant an Update
// transitions Stage into closed_won — see the "becameClosedWon" guard next
// to this call in memstore.go / pgstore.go, which is what keeps this from
// firing on closed_lost or on any unrelated save of an already-won deal.
//
// source_version is always 1: a deal can become closed_won at most once ever
// (closed stages are terminal — transitions.go), so there is no revision to
// version past the first.
func enqueueOpportunityClosedWon(ctx context.Context, deps closedWonDeps, scope Scope, o *Opportunity) error {
	owner, err := ownerOfAccount(ctx, deps, scope, map[string]string{}, o.AccountID)
	if err != nil {
		return err
	}

	env := Envelope{
		ID:            newID(),
		Type:          EventOpportunityClosedWon,
		TenantID:      scope.TenantID,
		CorrelationID: newID(),
		SourceApp:     SourceAppCRM,
		EntityType:    EntityOpportunity,
		EntityID:      o.ID,
		SourceVersion: 1,
		Actor:         "system",
		OccurredAt:    time.Now().UTC(),
	}
	env, err = env.WithPayload(OpportunityClosedWonPayload{
		OpportunityID: o.ID,
		AccountID:     o.AccountID,
		Amount:        o.Amount,
		OwnerID:       owner,
		CloseDate:     o.CloseDate,
	})
	if err != nil {
		return err
	}

	_, err = EnqueueEvent(ctx, deps, scope, env)
	return err
}
