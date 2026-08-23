import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import CustomObjectsPage from "./CustomObjectsPage";
import { makeCustomObject, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listCustomObjectDefinitions: vi.fn(),
    createCustomObjectDefinition: vi.fn(),
    archiveCustomObjectDefinition: vi.fn(),
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

const routes = [{ path: "/settings/custom-objects", element: <CustomObjectsPage /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listCustomObjectDefinitions.mockResolvedValue({ definitions: [makeCustomObject()] });
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/custom-objects"]);
  await screen.findByText("Shipment");
  return view;
}

describe("CustomObjectsPage accessibility", () => {
  it("has no violations on the loaded page", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);
  });

  it("has no violations with the new-object dialog open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New object" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("has no violations for a reader who cannot write", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:custom_object:write");
    const { container } = await loaded();
    expect(screen.queryByRole("button", { name: "Archive" })).not.toBeInTheDocument();
    await expectNoA11yViolations(container);
  });
});
