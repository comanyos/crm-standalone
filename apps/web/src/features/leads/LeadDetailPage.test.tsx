import { screen } from "@testing-library/react";
import { beforeEach, describe, it, vi } from "vitest";
import LeadDetailPage from "./LeadDetailPage";
import { makeLead, makeNote, makeTask, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getLead: vi.fn(), deleteLead: vi.fn(), convertLead: vi.fn(), getContact: vi.fn(),
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
  { path: "/leads", element: <p>Leads list</p> },
  { path: "/leads/:id", element: <LeadDetailPage /> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getLead.mockResolvedValue({ lead: makeLead({ status: "qualified" }) });
  crm.listActivitiesForSubject.mockResolvedValue({ activities: [], nextPageToken: "" });
  crm.listTasks.mockResolvedValue({ tasks: [makeTask()], nextPageToken: "" });
  crm.listNotes.mockResolvedValue({ notes: [makeNote()], nextPageToken: "" });
});

/** The lead detail page composes the most parts of any screen: a definition
 * list, the lifecycle controls, the conversion action, and three embedded
 * panels each with their own controls and dialogs. Those panels are scanned
 * on their own elsewhere; this checks them assembled, which is where a
 * duplicate landmark or a repeated id would show up. */
describe("LeadDetailPage accessibility", () => {
  it("has no violations with its panels assembled", async () => {
    const { container } = renderRoutes(routes, ["/leads/lead-1"]);
    await screen.findByRole("heading", { name: /Northwind/ });
    await screen.findByText("Renewal owner is now Priya.");
    await expectNoA11yViolations(container);
  });
});
