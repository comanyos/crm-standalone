import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import CustomFieldsPage from "./CustomFieldsPage";
import { makeCustomField, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listCustomFieldDefinitions: vi.fn(),
    createCustomFieldDefinition: vi.fn(),
    updateCustomFieldDefinition: vi.fn(),
    archiveCustomFieldDefinition: vi.fn(),
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

const routes = [{ path: "/settings/custom-fields", element: <CustomFieldsPage /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listCustomFieldDefinitions.mockResolvedValue({
    definitions: [makeCustomField(), makeCustomField({ id: "cf-2", fieldKey: "tier_note", label: "Tier note" })],
  });
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/custom-fields"]);
  await screen.findByText("Deal source");
  return view;
}

describe("CustomFieldsPage accessibility", () => {
  it("has no violations on the loaded page", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);
  });

  // The editor is a dialog full of controls whose labels are the whole
  // point — a field definition screen that cannot be operated by name is
  // worse than most.
  it("has no violations with the field editor open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]);
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("has no violations with the new-field dialog open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New field" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("has no violations for a reader who cannot write", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:custom_field:write");
    const { container } = await loaded();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    await expectNoA11yViolations(container);
  });
});
