import { screen } from "@testing-library/react";
import { beforeEach, describe, it, vi } from "vitest";
import OpportunityDetailPage from "./OpportunityDetailPage";
import { makeAccount, makeNote, makeOpportunity, makeTask, makeTimestamp, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getOpportunity: vi.fn(), deleteOpportunity: vi.fn(), getAccount: vi.fn(),
    listOpportunityStageHistory: vi.fn(),
    listActivitiesForSubject: vi.fn(), createActivity: vi.fn(),
    listTasks: vi.fn(), completeTask: vi.fn(),
    listNotes: vi.fn(), createNote: vi.fn(), updateNote: vi.fn(), deleteNote: vi.fn(),
  },
  auth: {
    hasScope: vi.fn(), hasRole: vi.fn(), status: "authenticated",
    session: { claims: { sub: "user-1" } }, login: vi.fn(), logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/opportunities", element: <p>Opportunities list</p> },
  { path: "/opportunities/:id", element: <OpportunityDetailPage /> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getOpportunity.mockResolvedValue({ opportunity: makeOpportunity() });
  crm.getAccount.mockResolvedValue({ account: makeAccount() });
  // The append-only stage history, which is this page's distinctive part.
  crm.listOpportunityStageHistory.mockResolvedValue({
    changes: [
      {
        $typeName: "crm.v1.OpportunityStageChange",
        id: "h-1",
        opportunityId: "opp-1",
        fromStage: "",
        toStage: "prospecting",
        changedAt: makeTimestamp("2026-03-01T10:00:00Z"),
      },
    ],
  });
  crm.listActivitiesForSubject.mockResolvedValue({ activities: [], nextPageToken: "" });
  crm.listTasks.mockResolvedValue({ tasks: [makeTask()], nextPageToken: "" });
  crm.listNotes.mockResolvedValue({ notes: [makeNote()], nextPageToken: "" });
});

describe("OpportunityDetailPage accessibility", () => {
  it("has no violations, stage history included", async () => {
    const { container } = renderRoutes(routes, ["/opportunities/opp-1"]);
    await screen.findByRole("heading", { name: /Acme renewal/ });
    await expectNoA11yViolations(container);
  });
});
