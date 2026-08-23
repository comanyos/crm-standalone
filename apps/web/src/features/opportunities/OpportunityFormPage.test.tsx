import { screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import OpportunityFormPage from "./OpportunityFormPage";
import { makeAccount, makeOpportunity, makeSalesStages, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getOpportunity: vi.fn(),
    updateOpportunity: vi.fn(),
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
    pipelines: [{ $typeName: "crm.v1.Pipeline", id: "system-sales", name: "Sales", isDefault: true }],
  });
  crm.listPipelineStages.mockResolvedValue({ stages: makeSalesStages() });
  crm.updateOpportunity.mockResolvedValue({ opportunity: makeOpportunity() });
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

});
