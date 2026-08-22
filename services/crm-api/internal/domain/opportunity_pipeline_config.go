package domain

import (
	"context"
	"fmt"
)

const systemSalesPipelineID = "system-sales"

var systemSalesStages = []PipelineStage{
	{ID: StageProspecting, Name: "Prospecting", Position: 0, Probability: 10, TerminalType: "open"},
	{ID: StageQualification, Name: "Qualification", Position: 1, Probability: 25, TerminalType: "open"},
	{ID: StageProposal, Name: "Proposal", Position: 2, Probability: 50, TerminalType: "open"},
	{ID: StageNegotiation, Name: "Negotiation", Position: 3, Probability: 75, TerminalType: "open"},
	{ID: StageClosedWon, Name: "Closed won", Position: 4, Probability: 100, TerminalType: "won", RequiresCloseReason: true},
	{ID: StageClosedLost, Name: "Closed lost", Position: 5, Probability: 0, TerminalType: "lost", RequiresCloseReason: true},
}

type pipelineReader interface {
	GetPipeline(context.Context, Scope, string) (*Pipeline, error)
	ListPipelineStages(context.Context, Scope, string) ([]*PipelineStage, error)
}

func activePipelineStage(ctx context.Context, r pipelineReader, scope Scope, pipelineID, stageID string) (*PipelineStage, error) {
	pipeline, err := r.GetPipeline(ctx, scope, pipelineID)
	if err != nil {
		return nil, err
	}
	if !pipeline.ArchivedAt.IsZero() {
		return nil, fmt.Errorf("%w: pipeline is archived", ErrInvalid)
	}
	stages, err := r.ListPipelineStages(ctx, scope, pipelineID)
	if err != nil {
		return nil, err
	}
	for _, stage := range stages {
		if stage.ID == stageID && stage.ArchivedAt.IsZero() {
			return stage, nil
		}
	}
	return nil, fmt.Errorf("%w: pipeline stage %q does not exist or is archived", ErrInvalid, stageID)
}

func initialPipelineStage(ctx context.Context, r pipelineReader, scope Scope, pipelineID string) (*PipelineStage, error) {
	pipeline, err := r.GetPipeline(ctx, scope, pipelineID)
	if err != nil {
		return nil, err
	}
	if !pipeline.ArchivedAt.IsZero() {
		return nil, fmt.Errorf("%w: pipeline is archived", ErrInvalid)
	}
	stages, err := r.ListPipelineStages(ctx, scope, pipelineID)
	if err != nil {
		return nil, err
	}
	for _, stage := range stages {
		if stage.ArchivedAt.IsZero() && stage.TerminalType == "open" {
			return stage, nil
		}
	}
	return nil, fmt.Errorf("%w: pipeline has no open stage", ErrInvalid)
}

// opportunityStageResolver is everything prepareOpportunityStage needs from
// a store: the pipeline reads the configured rules run against, plus the
// tenant's default pipeline for an Opportunity created without one.
//
// It is an interface rather than a concrete store so the one implementation
// below serves all three callers — MemStore, PgStore, and PgStore's
// transaction-scoped reader (pgstore_opportunity.go), which must resolve the
// stage against the same locked snapshot as the write it is deciding on.
type opportunityStageResolver interface {
	pipelineReader
	defaultOpportunityPipeline(ctx context.Context, scope Scope) (*Pipeline, error)
}

// prepareOpportunityStage resolves the pipeline and stage a write is asking
// for, fills in the defaults an omitted field implies, and returns the stage
// definition the closing/transition rules are then evaluated against. It
// mutates v.PipelineID/v.StageID/v.Stage into their resolved values.
//
// existing is nil on Create and the stored record on Update.
func prepareOpportunityStage(
	ctx context.Context, r opportunityStageResolver, scope Scope, v, existing *Opportunity,
) (*PipelineStage, error) {
	if v.PipelineID == "" {
		if existing != nil {
			v.PipelineID = existing.PipelineID
		} else {
			pipeline, err := r.defaultOpportunityPipeline(ctx, scope)
			if err != nil {
				return nil, err
			}
			v.PipelineID = pipeline.ID
		}
	}
	if existing != nil && v.PipelineID != existing.PipelineID {
		return nil, fmt.Errorf("%w: an opportunity cannot move to another pipeline", ErrInvalidTransition)
	}
	if existing != nil && v.Stage != "" && v.Stage != existing.Stage {
		// Compatibility for clients that predate stage_id: a changed legacy
		// stage value is the requested stage identifier in the Sales pipeline.
		v.StageID = v.Stage
	} else if v.StageID == "" {
		if existing != nil && v.Stage == existing.Stage {
			v.StageID = existing.StageID
		} else if v.Stage != "" {
			v.StageID = v.Stage
		} else {
			stage, err := initialPipelineStage(ctx, r, scope, v.PipelineID)
			if err != nil {
				return nil, err
			}
			v.StageID = stage.ID
		}
	}
	stage, err := activePipelineStage(ctx, r, scope, v.PipelineID, v.StageID)
	if err != nil {
		if v.PipelineID == systemSalesPipelineID {
			return nil, fmt.Errorf("%w: %q is not a known Opportunity stage", ErrInvalidTransition, v.StageID)
		}
		return nil, err
	}
	v.Stage = stageDisplay(v.PipelineID, stage)
	return stage, nil
}

func stageDisplay(pipelineID string, stage *PipelineStage) string {
	if pipelineID == systemSalesPipelineID {
		return stage.ID
	}
	return stage.Name
}

func checkConfiguredStageTransition(from, to *PipelineStage) error {
	if from.ID == to.ID {
		return nil
	}
	if from.TerminalType != "open" {
		return fmt.Errorf("%w: Opportunity is in a terminal stage and cannot be reopened", ErrInvalidTransition)
	}
	if to.TerminalType != "open" {
		return nil
	}
	if to.Position != from.Position+1 {
		return fmt.Errorf("%w: Opportunity must advance one open stage at a time", ErrInvalidTransition)
	}
	return nil
}

func applyConfiguredClosingRules(v *Opportunity, stage *PipelineStage) error {
	if stage.TerminalType == "open" {
		return nil
	}
	if stage.RequiresCloseReason && v.CloseReason == "" {
		return fmt.Errorf("%w: Opportunity.close_reason is required to close this stage", ErrInvalid)
	}
	if v.CloseDate.IsZero() {
		v.CloseDate = nowStamp()
		return nil
	}
	return fmt.Errorf("%w: Opportunity.close_date is already set; the closing transition stamps it", ErrInvalid)
}
