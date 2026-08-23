import { screen } from "@testing-library/react";
import { beforeEach, describe, it, vi } from "vitest";
import OpportunitiesListPage from "./OpportunitiesListPage";
import { makeOpportunity, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listOpportunities: vi.fn() },
  auth: {
    hasScope: vi.fn(), hasRole: vi.fn(), status: "authenticated",
    session: { claims: { sub: "user-1" } }, login: vi.fn(), logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/opportunities", element: <OpportunitiesListPage /> },
  { path: "/opportunities/:id", element: <p>Opportunity detail</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listOpportunities.mockResolvedValue({
    opportunities: [makeOpportunity(), makeOpportunity({ id: "opp-2", name: "Contoso expansion", stageId: "closed_won", stage: "closed_won" })],
    nextPageToken: "",
  });
});

describe("OpportunitiesListPage accessibility", () => {
  it("has no violations, including the stage badges", async () => {
    const { container } = renderRoutes(routes, ["/opportunities"]);
    await screen.findByRole("table");
    await expectNoA11yViolations(container);
  });
});
