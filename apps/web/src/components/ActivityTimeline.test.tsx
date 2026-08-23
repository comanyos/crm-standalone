import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, it, vi } from "vitest";
import { ActivityTimeline } from "./ActivityTimeline";
import { makeTimestamp, renderRoutes } from "../test/harness";
import { expectNoA11yViolations } from "../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listActivitiesForSubject: vi.fn(), createActivity: vi.fn() },
  auth: {
    hasScope: vi.fn(), hasRole: vi.fn(), status: "authenticated",
    session: { claims: { sub: "user-1" } }, login: vi.fn(), logout: vi.fn(),
  },
}));

vi.mock("../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/", element: <ActivityTimeline subjectType="account" subjectId="acc-1" /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listActivitiesForSubject.mockResolvedValue({
    activities: [
      {
        $typeName: "crm.v1.Activity",
        id: "act-1",
        kind: "call",
        notes: "Walked through the renewal terms.",
        subjectType: "account",
        subjectId: "acc-1",
        occurredAt: makeTimestamp("2026-03-01T10:00:00Z"),
      },
    ],
    nextPageToken: "",
  });
});

/** The detail pages render this with an empty activity list, so the populated
 * timeline and its composer dialog were never actually scanned — an empty
 * state is not evidence about the state people spend their time in. */
describe("ActivityTimeline accessibility", () => {
  it("has no violations with activities listed", async () => {
    const { container } = renderRoutes(routes);
    await screen.findByText("Walked through the renewal terms.");
    await expectNoA11yViolations(container);
  });

  it("has no violations with the composer open", async () => {
    const { container } = renderRoutes(routes);
    await screen.findByText("Walked through the renewal terms.");

    fireEvent.click(screen.getByRole("button", { name: "Log activity" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });
});
