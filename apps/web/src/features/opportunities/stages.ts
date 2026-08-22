export const OPPORTUNITY_STAGES = [
  { value: "prospecting", label: "Prospecting" },
  { value: "qualification", label: "Qualification" },
  { value: "proposal", label: "Proposal" },
  { value: "negotiation", label: "Negotiation" },
  { value: "closed_won", label: "Closed won" },
  { value: "closed_lost", label: "Closed lost" },
] as const;

export const NEXT_STAGE: Record<string, string[]> = {
  prospecting: ["qualification"],
  qualification: ["proposal"],
  proposal: ["negotiation"],
  negotiation: ["closed_won", "closed_lost"],
  closed_won: [],
  closed_lost: [],
};

export function stageLabel(stage: string) {
  return OPPORTUNITY_STAGES.find((item) => item.value === stage)?.label ?? stage;
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
