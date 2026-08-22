package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Archiving a pipeline or a stage, and why it has to be refused rather than
// simply recorded.
//
// `activePipelineStage` resolves an Opportunity's *current* stage before any
// other rule runs, and refuses an archived pipeline or stage. So a deal left
// standing on something that gets archived becomes permanently unwritable: it
// cannot advance, cannot be closed, and cannot even be renamed. It stays
// readable and keeps its amount, so it still counts toward anything summed
// later. Nothing in the application can undo that — the only exit is SQL.
//
// The rule is therefore: archival is refused while open deals still stand on
// the thing being archived, and the administrator moves them off first
// through the ordinary validated transition path.
//
// Only *open* deals block. A closed deal cannot be moved off — every
// transition out of a terminal stage is refused — so counting it would make
// any stage that has ever closed a deal permanently unarchivable, and the
// remedy this rule points at would be impossible to follow.

// maxNamedBlockers bounds how many deals a refusal names. The point of the
// message is to make the next action obvious, not to serialize the pipeline.
const maxNamedBlockers = 5

// archivalBlockers is what a refusal needs in order to be actionable: how
// many open deals are in the way, and enough of their names to recognise
// them.
type archivalBlockers struct {
	Count int
	Names []string
}

// refuse returns the ErrConflict an occupied archival must fail with, or nil
// when nothing is in the way. subject names what was being archived, as it
// should read in the message ("pipeline \"Sales\"", "stage \"Proposal\"").
func (b archivalBlockers) refuse(subject string) error {
	if b.Count == 0 {
		return nil
	}
	deals := "open opportunities"
	if b.Count == 1 {
		deals = "open opportunity"
	}
	named := strings.Join(b.Names, ", ")
	if b.Count > len(b.Names) {
		named = fmt.Sprintf("%s and %d more", named, b.Count-len(b.Names))
	}
	return fmt.Errorf(
		"%w: %s still holds %d %s (%s) — move them to another stage before archiving it",
		ErrConflict, subject, b.Count, deals, named)
}

// occupancyReader is the one thing the guard needs from a store: the open
// deals standing on a pipeline, or on one stage of it. An empty stageID asks
// about the whole pipeline.
type occupancyReader interface {
	openOpportunitiesIn(ctx context.Context, scope Scope, pipelineID, stageID string) (archivalBlockers, error)
}

// checkPipelineArchival refuses to archive a pipeline that still holds open
// deals. It is a no-op unless this write is the transition into archived — a
// re-save of an already-archived pipeline must keep working, or an
// administrator could not so much as rename one.
func checkPipelineArchival(ctx context.Context, r occupancyReader, scope Scope, existing, next *Pipeline) error {
	if !archiving(existing.ArchivedAt.IsZero(), next.ArchivedAt.IsZero()) {
		return nil
	}
	blockers, err := r.openOpportunitiesIn(ctx, scope, next.ID, "")
	if err != nil {
		return err
	}
	return blockers.refuse(fmt.Sprintf("pipeline %q", existing.Name))
}

// checkStageArchival refuses to archive a stage that still holds open deals.
func checkStageArchival(ctx context.Context, r occupancyReader, scope Scope, existing, next *PipelineStage) error {
	if !archiving(existing.ArchivedAt.IsZero(), next.ArchivedAt.IsZero()) {
		return nil
	}
	blockers, err := r.openOpportunitiesIn(ctx, scope, next.PipelineID, next.ID)
	if err != nil {
		return err
	}
	return blockers.refuse(fmt.Sprintf("stage %q", existing.Name))
}

// archiving reports whether a write moves a record from active to archived.
func archiving(wasActive, willBeActive bool) bool {
	return wasActive && !willBeActive
}

// openOpportunitiesIn finds the open deals blocking an archival, in one
// query. The join is what decides "open": a deal is open when the stage it
// currently stands on is, so this cannot drift from the transition rules the
// way a status column copied onto the opportunity would.
func (s *PgStore) openOpportunitiesIn(
	ctx context.Context, scope Scope, pipelineID, stageID string,
) (archivalBlockers, error) {
	var out archivalBlockers
	rows, err := s.db.Query(ctx, scope,
		`SELECT o.name FROM opportunities o
		   JOIN pipeline_stages s
		     ON s.tenant_id = o.tenant_id AND s.pipeline_id = o.pipeline_id AND s.id = o.stage_id
		  WHERE o.tenant_id = $1 AND o.pipeline_id = $2
		    AND ($3 = '' OR o.stage_id = $3)
		    AND s.terminal_type = 'open'
		  ORDER BY o.name`,
		scope.TenantID, pipelineID, stageID)
	if err != nil {
		return out, fmt.Errorf("find open opportunities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return archivalBlockers{}, fmt.Errorf("scan open opportunity: %w", err)
		}
		out.Count++
		if len(out.Names) < maxNamedBlockers {
			out.Names = append(out.Names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return archivalBlockers{}, fmt.Errorf("find open opportunities: %w", err)
	}
	return out, nil
}

// openOpportunitiesIn is MemStore's equivalent, deciding "open" from the same
// stage definitions rather than from anything stored on the deal.
func (s *MemStore) openOpportunitiesIn(
	ctx context.Context, scope Scope, pipelineID, stageID string,
) (archivalBlockers, error) {
	stages, err := s.ListPipelineStages(ctx, scope, pipelineID)
	if err != nil {
		return archivalBlockers{}, err
	}
	open := make(map[string]bool, len(stages))
	for _, stage := range stages {
		open[stage.ID] = stage.TerminalType == "open"
	}
	all, err := s.opportunity.all(scope)
	if err != nil {
		return archivalBlockers{}, err
	}
	names := make([]string, 0, 8)
	for _, opportunity := range all {
		if opportunity.PipelineID != pipelineID || !open[opportunity.StageID] {
			continue
		}
		if stageID != "" && opportunity.StageID != stageID {
			continue
		}
		names = append(names, opportunity.Name)
	}
	sort.Strings(names)
	out := archivalBlockers{Count: len(names)}
	if len(names) > maxNamedBlockers {
		names = names[:maxNamedBlockers]
	}
	out.Names = names
	return out, nil
}
