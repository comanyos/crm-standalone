package domain

import (
	"context"
	"fmt"
	"time"
)

// pipelineMoveStamp returns a version strictly newer than previous. A normal
// nowStamp is sufficient almost always, but two writes can land in the same
// microsecond — the precision PostgreSQL retains. In that case returning the
// old version would let a replay pass the optimistic-concurrency check.
func pipelineMoveStamp(previous time.Time) time.Time {
	now := nowStamp()
	if !now.After(previous) {
		return previous.Add(storedTimePrecision)
	}
	return now
}

// validateOpportunityPipelineMove checks the parts of a cross-pipeline move
// that are independent of storage. A move is not a configured stage
// transition: the two pipelines may have unrelated stage graphs. It is an
// administrative relocation of an *open* deal, so both the source and the
// explicit destination must be open. The caller still needs to perform this
// beneath its row lock, because the opportunity can change stages while a
// destination is being selected.
func validateOpportunityPipelineMove(
	ctx context.Context, r pipelineReader, scope Scope, existing *Opportunity, pipelineID, stageID string,
) (*PipelineStage, error) {
	if pipelineID == "" {
		return nil, fmt.Errorf("%w: destination pipeline_id is required", ErrInvalid)
	}
	if stageID == "" {
		return nil, fmt.Errorf("%w: destination stage_id is required", ErrInvalid)
	}
	if pipelineID == existing.PipelineID {
		return nil, fmt.Errorf("%w: destination pipeline must differ from the current pipeline", ErrInvalid)
	}
	from, err := currentPipelineStage(ctx, r, scope, existing.PipelineID, existing.StageID)
	if err != nil {
		return nil, err
	}
	if from.TerminalType != "open" {
		return nil, fmt.Errorf("%w: a closed opportunity cannot move to another pipeline", ErrInvalidTransition)
	}
	to, err := activePipelineStage(ctx, r, scope, pipelineID, stageID)
	if err != nil {
		return nil, err
	}
	if to.TerminalType != "open" {
		return nil, fmt.Errorf("%w: destination stage must be open", ErrInvalidTransition)
	}
	return to, nil
}
