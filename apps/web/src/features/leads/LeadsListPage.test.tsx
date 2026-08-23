import { screen } from "@testing-library/react";
import { beforeEach, describe, it, vi } from "vitest";
import LeadsListPage from "./LeadsListPage";
import { makeLead, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listLeads: vi.fn() },
  auth: {
    hasScope: vi.fn(), hasRole: vi.fn(), status: "authenticated",
    session: { claims: { sub: "user-1" } }, login: vi.fn(), logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/leads", element: <LeadsListPage /> },
  { path: "/leads/:id", element: <p>Lead detail</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listLeads.mockResolvedValue({
    leads: [makeLead(), makeLead({ id: "lead-2", company: "Contoso", duplicateOfContactId: "con-9" })],
    nextPageToken: "",
  });
});

describe("LeadsListPage accessibility", () => {
  it("has no violations, including the duplicate badge column", async () => {
    const { container } = renderRoutes(routes, ["/leads"]);
    await screen.findByRole("table");
    await expectNoA11yViolations(container);
  });
});
