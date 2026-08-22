package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// opportunityStageEdges lists, for each stage, the stages it may move to. A
// stage with an empty set is terminal. This map is the single source of
// truth both MemStore and PgStore call through, so the two implementations
// cannot drift on the rule — only on the plumbing that reaches the row.
var opportunityStageEdges = map[string]map[string]bool{
	StageProspecting:   {StageQualification: true},
	StageQualification: {StageProposal: true},
	StageProposal:      {StageNegotiation: true},
	StageNegotiation:   {StageClosedWon: true, StageClosedLost: true},
	StageClosedWon:     {},
	StageClosedLost:    {},
}

// opportunityStageIsClosed reports whether a stage is one of the two
// terminal, closed stages.
func opportunityStageIsClosed(stage string) bool {
	return stage == StageClosedWon || stage == StageClosedLost
}

// applyOpportunityStageDefault fills the stage of a newly created
// Opportunity. Default stage on Create is prospecting.
func applyOpportunityStageDefault(v *Opportunity) {
	if v.Stage == "" {
		v.Stage = StageProspecting
	}
}

// checkOpportunityStageKnown rejects a stage value the machine has never
// heard of — distinct from a known-but-illegal transition, which
// checkOpportunityStageTransition below handles.
func checkOpportunityStageKnown(stage string) error {
	if _, ok := opportunityStageEdges[stage]; !ok {
		return fmt.Errorf("%w: %q is not a known Opportunity stage", ErrInvalidTransition, stage)
	}
	return nil
}

// checkOpportunityStageTransition validates a stage change. An unchanged
// stage is always allowed, so an update that only edits other fields (name,
// amount, ...) is never blocked by this check. A closed stage is terminal:
// nothing may move out of it, including into the other closed stage.
func checkOpportunityStageTransition(from, to string) error {
	if from == to {
		return nil
	}
	if opportunityStageIsClosed(from) {
		return fmt.Errorf(
			"%w: Opportunity is %q, a terminal stage; it cannot move to %q — reopen by creating a new Opportunity",
			ErrInvalidTransition, from, to)
	}
	edges, ok := opportunityStageEdges[from]
	if !ok {
		return fmt.Errorf("%w: %q is not a known Opportunity stage", ErrInvalidTransition, from)
	}
	if _, ok := opportunityStageEdges[to]; !ok {
		return fmt.Errorf("%w: %q is not a known Opportunity stage", ErrInvalidTransition, to)
	}
	if !edges[to] {
		return fmt.Errorf("%w: Opportunity cannot move from %q to %q", ErrInvalidTransition, from, to)
	}
	return nil
}

// applyOpportunityClosingRules enforces the win/loss rule (B2 item 3) the
// instant a stage lands on closed_won or closed_lost — whether that is a
// fresh Update transition or an Opportunity created directly in a closed
// stage. Callers must gate this so it only runs on the write that actually
// causes the move into a closed stage, not on every subsequent save of an
// already-closed record.
//
//   - close_reason must be non-empty, or the write is rejected as ErrInvalid.
//   - close_date is stamped to now if it was unset. If it was already set,
//     that is itself rejected as ErrInvalid: this closing write is the one
//     that is supposed to stamp close_date, so a caller that pre-supplies one
//     is treated as a mistake rather than silently honoured.
func applyOpportunityClosingRules(v *Opportunity) error {
	if strings.TrimSpace(v.CloseReason) == "" {
		return fmt.Errorf("%w: Opportunity.close_reason is required to close stage %q", ErrInvalid, v.Stage)
	}
	if v.CloseDate.IsZero() {
		v.CloseDate = nowStamp()
		return nil
	}
	return fmt.Errorf(
		"%w: Opportunity.close_date is already set; the closing transition stamps it and must not be pre-supplied",
		ErrInvalid)
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
