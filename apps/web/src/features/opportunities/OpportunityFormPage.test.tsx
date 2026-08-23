import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { beforeEach, describe, expect, it, vi } from "vitest";
import OpportunityFormPage from "./OpportunityFormPage";
import { makeAccount, makeOpportunity, makeSalesStages, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getOpportunity: vi.fn(),
    updateOpportunity: vi.fn(),
    moveOpportunityToPipeline: vi.fn(),
    getAccount: vi.fn(),
    listAccounts: vi.fn(),
    listPipelines: vi.fn(),
    listPipelineStages: vi.fn(),
  },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/opportunities", element: <p>Opportunities list</p> },
  { path: "/opportunities/:id", element: <p>Opportunity detail</p> },
  { path: "/opportunities/:id/edit", element: <OpportunityFormPage mode="edit" /> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getOpportunity.mockResolvedValue({ opportunity: makeOpportunity() });
  crm.getAccount.mockResolvedValue({ account: makeAccount() });
  crm.listAccounts.mockResolvedValue({ accounts: [makeAccount()], nextPageToken: "" });
  crm.listPipelines.mockResolvedValue({
    pipelines: [
      { $typeName: "crm.v1.Pipeline", id: "system-sales", name: "Sales", isDefault: true },
      { $typeName: "crm.v1.Pipeline", id: "partner-sales", name: "Partner sales", isDefault: false },
    ],
  });
  crm.listPipelineStages.mockResolvedValue({ stages: makeSalesStages() });
  crm.updateOpportunity.mockResolvedValue({ opportunity: makeOpportunity() });
  crm.moveOpportunityToPipeline.mockResolvedValue({ opportunity: makeOpportunity({ pipelineId: "partner-sales", stageId: "discovery", stage: "Discovery" }) });
});

async function stageChoices() {
  renderRoutes(routes, ["/opportunities/opp-1/edit"]);
  await waitFor(() => expect(screen.getByLabelText(/^Opportunity name/)).toHaveValue("Acme renewal"));
  const select = await screen.findByLabelText(/^Stage/);
  return Array.from(within(select).getAllByRole("option")).map((option) => option.textContent);
}

describe("OpportunityFormPage stage options", () => {
  // The server permits closing from any open stage — deals are lost at every
  // stage of a pipeline, not only the last one. This form used to offer a
  // closing stage only from the last open stage, so a deal could not be marked
  // lost at Prospecting without first being walked through Qualification,
  // Proposal and Negotiation, inventing a history that never happened. The
  // board allowed the move all along, which made the two screens disagree
  // about the same record.
  it("offers both closing stages from the first open stage", async () => {
    expect(await stageChoices()).toEqual(["Prospecting", "Qualification", "Closed won", "Closed lost"]);
  });

  it("still refuses to skip an open stage", async () => {
    const choices = await stageChoices();
    expect(choices).not.toContain("Proposal");
    expect(choices).not.toContain("Negotiation");
  });

  it("offers nothing to a deal standing on a stage the pipeline no longer has", async () => {
    // Archived out from under it: the form falls back to the read-only stage
    // display rather than offering moves it cannot compute.
    crm.getOpportunity.mockResolvedValue({
      opportunity: makeOpportunity({ stageId: "retired", stage: "retired" }),
    });
    renderRoutes(routes, ["/opportunities/opp-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Opportunity name/)).toHaveValue("Acme renewal"));

    const stage = screen.getByLabelText(/^Stage/);
    expect(stage).toHaveAttribute("readonly");
  });

  // A form is where label association and error-to-field wiring live, and
  // both are invisible until something reads the page aloud.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/opportunities/opp-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Opportunity name/)).toHaveValue("Acme renewal"));
    await expectNoA11yViolations(container);
  });

  it("moves an open deal through the narrow pipeline-transfer RPC", async () => {
    const version = timestampFromDate(new Date("2026-08-23T06:40:00.000Z"));
    crm.getOpportunity.mockResolvedValue({ opportunity: makeOpportunity({ updatedAt: version }) });
    crm.listPipelineStages.mockImplementation(({ pipelineId }: { pipelineId: string }) => Promise.resolve({
      stages: pipelineId === "partner-sales"
        ? [{ $typeName: "crm.v1.PipelineStage", id: "discovery", pipelineId, name: "Discovery", position: 0, probability: 10, terminalType: "open", requiresCloseReason: false }]
        : makeSalesStages(),
    }));
    renderRoutes(routes, ["/opportunities/opp-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Opportunity name/)).toHaveValue("Acme renewal"));

    fireEvent.change(screen.getByLabelText("Destination pipeline"), { target: { value: "partner-sales" } });
    await waitFor(() => expect(screen.getByLabelText("Destination stage")).toBeEnabled());
    await screen.findByRole("option", { name: "Discovery" });
    fireEvent.change(screen.getByLabelText("Destination stage"), { target: { value: "discovery" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Move opportunity" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Move opportunity" }));

    await waitFor(() => expect(crm.moveOpportunityToPipeline).toHaveBeenCalledWith({
      opportunityId: "opp-1", pipelineId: "partner-sales", stageId: "discovery", updatedAt: version,
    }));
    expect(crm.updateOpportunity).not.toHaveBeenCalled();
  });

});
