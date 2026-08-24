import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import TagsPage from "./TagsPage";
import { makeTag, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listTags: vi.fn(),
    createTag: vi.fn(),
    updateTag: vi.fn(),
    deleteTag: vi.fn(),
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

const routes = [{ path: "/settings/tags", element: <TagsPage /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listTags.mockResolvedValue({ tags: [makeTag(), makeTag({ id: "tag-2", name: "Needs Follow-up", color: "#0000ff" })] });
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/tags"]);
  await screen.findByText("Hot Lead");
  return view;
}

describe("TagsPage", () => {
  it("has no accessibility violations on the loaded page", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);
  });

  it("has no accessibility violations with the new-tag dialog open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New tag" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("hides write controls for a reader who cannot write", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:tag:write");
    const { container } = await loaded();
    expect(screen.queryByRole("button", { name: "New tag" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Edit tag/ })).not.toBeInTheDocument();
    await expectNoA11yViolations(container);
  });

  it("creates a tag from the dialog", async () => {
    crm.createTag.mockResolvedValue({ tag: makeTag({ id: "tag-3", name: "New Tag" }) });
    await loaded();

    fireEvent.click(screen.getByRole("button", { name: "New tag" }));
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "New Tag" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tag" }));

    await waitFor(() => expect(crm.createTag).toHaveBeenCalledWith(
      expect.objectContaining({ name: "New Tag" }),
    ));
  });

  // Deleting used to be the kind of destructive action window.confirm
  // handles elsewhere in this app; TagsPage uses the same ConfirmDialog
  // convention as CustomFieldsPage's archive flow.
  it("asks before deleting a tag, and deletes only once confirmed", async () => {
    crm.deleteTag.mockResolvedValue({});
    await loaded();

    fireEvent.click(screen.getByRole("button", { name: "Delete tag Hot Lead" }));
    await screen.findByRole("dialog");
    expect(crm.deleteTag).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete tag" }));
    await waitFor(() => expect(crm.deleteTag).toHaveBeenCalledWith({ id: "tag-1" }));
  });

  it("names each row's controls for the tag they act on", async () => {
    await loaded();
    expect(screen.getByRole("button", { name: "Edit tag Hot Lead" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit tag Needs Follow-up" })).toBeInTheDocument();
  });
});
