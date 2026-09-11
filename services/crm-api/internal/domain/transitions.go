package domain

import (
	"context"
	"errors"
	"fmt"
)

// Opportunity pipeline state machine (B2 — docs/customer-os/PLAN.md):
//
//	prospecting -> qualification -> proposal -> negotiation -> closed_won / closed_lost
//
// Forward-only through the funnel: the plan's chain is read literally as the
// only legal path, so each open stage may move to exactly the next stage the
// chain names for it (negotiation is the one branch point, into either closed
// state). Skipping stages (prospecting -> proposal) and moving backward
// (negotiation -> proposal) are both illegal transitions — "forward-only"
// here does not mean "to any stage of higher rank".
//
// Both closed states are terminal: nothing may transition out of either one.
// A reopened deal is a new Opportunity record, never an edit of a closed one
// — the same argument that makes a rehire a second Employment elsewhere in
// the suite.

// Opportunity.Stage values.
const (
	StageProspecting   = "prospecting"
	StageQualification = "qualification"
	StageProposal      = "proposal"
	StageNegotiation   = "negotiation"
	StageClosedWon     = "closed_won"
	StageClosedLost    = "closed_lost"
)

// opportunityStageIsClosed reports whether a stage is one of the two
// terminal, closed stages.
func opportunityStageIsClosed(stage string) bool {
	return stage == StageClosedWon || stage == StageClosedLost
}

// --- Referential integrity -------------------------------------------------
//
// account_id must name an existing Account, looked up under the same
// scope.TenantID as the Opportunity itself — never a cross-tenant lookup.
// The check is read-then-write against the store's own scoped reader, which
// is consistent with the module's existing last-write-wins guarantees.

type accountReader interface {
	GetAccount(ctx context.Context, scope Scope, id string) (*Account, error)
}

// requireAccount fails when no Account names accountID in this tenant. An
// empty accountID fails the same way an unknown one does: GetAccount("")
// never matches a stored id, so this also makes account_id effectively
// required on every Opportunity write.
func requireAccount(ctx context.Context, r accountReader, scope Scope, accountID string) error {
	_, err := r.GetAccount(ctx, scope, accountID)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: Opportunity.account_id %q has no account", ErrInvalid, accountID)
	}
	return err
}
