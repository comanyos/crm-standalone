import type { PipelineStage } from "@crm/contracts";

/** The client-side mirror of the server's configured stage state machine.
 *
 * The authority is `checkConfiguredStageTransition` and
 * `applyConfiguredClosingRules` in
 * `services/crm-api/internal/domain/opportunity_pipeline_config.go`. This
 * exists so the board can grey out a drop target *before* the user drags to
 * it, not so the rules live in two places: every move still goes through
 * UpdateOpportunity, which re-decides the transition against the locked row
 * and is the only thing that can actually refuse it. A disagreement between
 * the two shows up as a rejected drop, never as an illegal write.
 *
 * Keep the four rules below in the same order as the Go function, so a change
 * on either side is easy to mirror.
 */

export type StageMove =
  | { allowed: false; reason: string }
  | { allowed: true; noop: boolean; closes: boolean; needsCloseReason: boolean };

/** checkStageMove answers whether `to` is a legal destination for a deal
 * currently on `from`, and what the move implies if it is. */
export function checkStageMove(from: PipelineStage, to: PipelineStage): StageMove {
  // Rule 0 (not in the Go function — it runs earlier, in activePipelineStage):
  // an archived stage is not a destination at all.
  if (to.archivedAt) {
    return { allowed: false, reason: `${to.name} is archived.` };
  }
  // Rule 1: a move to where the deal already is changes nothing.
  if (from.id === to.id) {
    return { allowed: true, noop: true, closes: false, needsCloseReason: false };
  }
  // Rule 2: a closed deal is closed. Nothing leaves a terminal stage.
  if (from.terminalType !== "open") {
    return { allowed: false, reason: `${from.name} is a closing stage, and a closed deal cannot be reopened.` };
  }
  // Rule 3: any open stage may close, to either outcome, without first
  // walking to the end of the pipeline. Deals are lost at every stage.
  if (to.terminalType !== "open") {
    return { allowed: true, noop: false, closes: true, needsCloseReason: to.requiresCloseReason };
  }
  // Rule 4: forward one open stage at a time — no skipping, no going back.
  if (to.position !== from.position + 1) {
    return {
      allowed: false,
      reason:
        to.position <= from.position
          ? `A deal cannot move back to ${to.name}.`
          : `A deal advances one stage at a time — ${to.name} is not the next stage.`,
    };
  }
  return { allowed: true, noop: false, closes: false, needsCloseReason: false };
}

/** legalTargets is the set of stages a deal on `from` may be dropped onto,
 * excluding the no-op of its own stage. Used to render drop targets and to
 * build the keyboard move menu from one source. */
export function legalTargets(from: PipelineStage, stages: readonly PipelineStage[]): PipelineStage[] {
  return stages.filter((stage) => {
    if (stage.id === from.id) return false;
    return checkStageMove(from, stage).allowed;
  });
}
