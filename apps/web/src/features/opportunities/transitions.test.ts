import { describe, expect, it } from "vitest";
import type { PipelineStage } from "@crm/contracts";
import { checkStageMove, legalTargets } from "./transitions";

/** The stage set these tests reason about is the shape of the system Sales
 * pipeline the backend provisions (systemSalesStages in
 * opportunity_pipeline_config.go): four open stages in position order, then a
 * won and a lost stage that both require a close reason. */
function stage(overrides: Partial<PipelineStage> & { id: string; position: number }): PipelineStage {
  return {
    $typeName: "crm.v1.PipelineStage",
    pipelineId: "system-sales",
    name: overrides.id,
    probability: 0,
    terminalType: "open",
    requiresCloseReason: false,
    ...overrides,
  } as PipelineStage;
}

const prospecting = stage({ id: "prospecting", name: "Prospecting", position: 0 });
const qualification = stage({ id: "qualification", name: "Qualification", position: 1 });
const proposal = stage({ id: "proposal", name: "Proposal", position: 2 });
const negotiation = stage({ id: "negotiation", name: "Negotiation", position: 3 });
const won = stage({ id: "closed_won", name: "Closed won", position: 4, terminalType: "won", requiresCloseReason: true });
const lost = stage({ id: "closed_lost", name: "Closed lost", position: 5, terminalType: "lost", requiresCloseReason: true });
const all = [prospecting, qualification, proposal, negotiation, won, lost];

describe("checkStageMove", () => {
  it("advances one open stage at a time", () => {
    expect(checkStageMove(prospecting, qualification)).toEqual({
      allowed: true,
      noop: false,
      closes: false,
      needsCloseReason: false,
    });
  });

  it("refuses a skipped stage", () => {
    const move = checkStageMove(prospecting, proposal);
    expect(move.allowed).toBe(false);
    expect(move.allowed === false && move.reason).toMatch(/one stage at a time/);
  });

  it("refuses a move backwards", () => {
    const move = checkStageMove(proposal, qualification);
    expect(move.allowed).toBe(false);
    expect(move.allowed === false && move.reason).toMatch(/cannot move back/);
  });

  // The rule the board reads differently from the edit form: the server
  // permits closing from any open stage, so a deal lost at Prospecting does
  // not have to be walked to Negotiation first.
  it("allows closing from any open stage, not only the last one", () => {
    expect(checkStageMove(prospecting, lost)).toEqual({
      allowed: true,
      noop: false,
      closes: true,
      needsCloseReason: true,
    });
    expect(checkStageMove(negotiation, won)).toEqual({
      allowed: true,
      noop: false,
      closes: true,
      needsCloseReason: true,
    });
  });

  it("reports a close reason as unnecessary when the stage does not require one", () => {
    const quietLoss = stage({ id: "lapsed", name: "Lapsed", position: 6, terminalType: "lost" });
    const move = checkStageMove(prospecting, quietLoss);
    expect(move).toEqual({ allowed: true, noop: false, closes: true, needsCloseReason: false });
  });

  it("refuses every move out of a terminal stage, in both directions", () => {
    for (const target of [prospecting, qualification, proposal, negotiation, lost]) {
      const move = checkStageMove(won, target);
      expect(move.allowed, `won -> ${target.id}`).toBe(false);
      expect(move.allowed === false && move.reason).toMatch(/cannot be reopened/);
    }
    expect(checkStageMove(lost, won).allowed).toBe(false);
  });

  it("treats a move to the deal's own stage as a no-op rather than a refusal", () => {
    expect(checkStageMove(proposal, proposal)).toEqual({
      allowed: true,
      noop: true,
      closes: false,
      needsCloseReason: false,
    });
    // Including a terminal stage: re-dropping a closed deal where it already
    // is must not read as an attempted reopen.
    expect(checkStageMove(won, won)).toEqual({ allowed: true, noop: true, closes: false, needsCloseReason: false });
  });

  it("refuses an archived stage as a destination", () => {
    const retired = stage({
      id: "retired",
      name: "Retired",
      position: 1,
      archivedAt: { $typeName: "google.protobuf.Timestamp", seconds: 1n, nanos: 0 },
    } as Partial<PipelineStage> & { id: string; position: number });
    const move = checkStageMove(prospecting, retired);
    expect(move.allowed).toBe(false);
    expect(move.allowed === false && move.reason).toMatch(/archived/);
  });
});

describe("legalTargets", () => {
  it("offers the next open stage and both closing stages from an open stage", () => {
    expect(legalTargets(prospecting, all).map((s) => s.id)).toEqual(["qualification", "closed_won", "closed_lost"]);
    expect(legalTargets(negotiation, all).map((s) => s.id)).toEqual(["closed_won", "closed_lost"]);
  });

  it("offers nothing from a terminal stage", () => {
    expect(legalTargets(won, all)).toEqual([]);
    expect(legalTargets(lost, all)).toEqual([]);
  });

  it("never offers the stage the deal is already on", () => {
    for (const from of all) {
      expect(legalTargets(from, all).map((s) => s.id)).not.toContain(from.id);
    }
  });
});
