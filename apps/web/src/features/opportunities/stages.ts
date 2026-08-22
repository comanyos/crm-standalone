// Presentation helpers for a stage identifier — labels and tones, nothing
// more. The stage *rules* live in transitions.ts, mirrored from the server.
//
// This file used to also export OPPORTUNITY_STAGES and NEXT_STAGE, a
// hardcoded copy of the six system stages and the transitions between them,
// from before pipelines were tenant-configurable. Nothing imported either any
// more, and NEXT_STAGE disagreed with the server besides — it allowed a close
// only from Negotiation. A dead, wrong copy of a rule is worse than no copy:
// the next person to need "what can this stage move to" would have found it.

const LABELS: Record<string, string> = {
  prospecting: "Prospecting",
  qualification: "Qualification",
  proposal: "Proposal",
  negotiation: "Negotiation",
  closed_won: "Closed won",
  closed_lost: "Closed lost",
};

export function stageLabel(stage: string) {
  return LABELS[stage] ?? stage;
}

export function isClosedStage(stage: string) {
  return stage === "closed_won" || stage === "closed_lost";
}

export function stageTone(stage: string): "neutral" | "brand" | "success" | "danger" {
  if (stage === "closed_won") return "success";
  if (stage === "closed_lost") return "danger";
  if (stage === "proposal" || stage === "negotiation") return "brand";
  return "neutral";
}
