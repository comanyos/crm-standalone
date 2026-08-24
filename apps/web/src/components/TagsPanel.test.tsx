import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TagsPanel } from "./TagsPanel";
import { makeTag, renderRoutes } from "../test/harness";
import { expectNoA11yViolations } from "../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listTagsForRecord: vi.fn(), listTags: vi.fn(), assignTag: vi.fn(), removeTag: vi.fn() },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/", element: <TagsPanel objectType="account" objectId="acc-1" /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listTagsForRecord.mockResolvedValue({ tags: [makeTag()] });
  crm.listTags.mockResolvedValue({ tags: [makeTag(), makeTag({ id: "tag-2", name: "Needs Follow-up" })] });
  crm.assignTag.mockResolvedValue({ tags: [makeTag(), makeTag({ id: "tag-2", name: "Needs Follow-up" })] });
  crm.removeTag.mockResolvedValue({ tags: [] });
});

describe("TagsPanel", () => {
  it("shows the record's current tags", async () => {
    renderRoutes(routes);
    expect(await screen.findByText("Hot Lead")).toBeInTheDocument();
  });

  it("says so when a record has no tags rather than showing an empty box", async () => {
    crm.listTagsForRecord.mockResolvedValue({ tags: [] });
    renderRoutes(routes);
    expect(await screen.findByText("No tags yet.")).toBeInTheDocument();
  });

  it("offers only tags not already on the record", async () => {
    renderRoutes(routes);
    await screen.findByText("Hot Lead");

    // "Hot Lead" is already assigned, so the picker must not offer it again.
    const picker = screen.getByLabelText("Add tag") as HTMLSelectElement;
    const optionLabels = Array.from(picker.options).map((o) => o.textContent);
    expect(optionLabels).toContain("Needs Follow-up");
    expect(optionLabels).not.toContain("Hot Lead");
  });

  it("assigns the picked tag to the record", async () => {
    renderRoutes(routes);
    await screen.findByText("Hot Lead");

    fireEvent.change(screen.getByLabelText("Add tag"), { target: { value: "tag-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() => expect(crm.assignTag).toHaveBeenCalledWith(
      { tagId: "tag-2", objectType: "account", objectId: "acc-1" },
    ));
  });

  it("removes a tag from the record", async () => {
    renderRoutes(routes);
    await screen.findByText("Hot Lead");

    fireEvent.click(screen.getByRole("button", { name: "Remove tag Hot Lead" }));

    await waitFor(() => expect(crm.removeTag).toHaveBeenCalledWith(
      { tagId: "tag-1", objectType: "account", objectId: "acc-1" },
    ));
  });

  it("offers no write controls without the tag write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:tag:write");
    renderRoutes(routes);
    await screen.findByText("Hot Lead");

    expect(screen.queryByRole("button", { name: "Remove tag Hot Lead" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Add tag")).not.toBeInTheDocument();
  });

  it("renders nothing at all without the tag read scope", async () => {
    auth.hasScope.mockReturnValue(false);
    const { container } = renderRoutes(routes);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(container.textContent).toBe("");
  });

  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes);
    await screen.findByText("Hot Lead");
    await expectNoA11yViolations(container);
  });
});
