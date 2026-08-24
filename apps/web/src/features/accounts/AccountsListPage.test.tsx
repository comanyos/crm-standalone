import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import AccountsListPage from "./AccountsListPage";
import { makeAccount, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listAccounts: vi.fn(), listTags: vi.fn(), bulkAssignTag: vi.fn(), bulkRemoveTag: vi.fn(),
    listSavedViews: vi.fn(), createSavedView: vi.fn(), deleteSavedView: vi.fn(),
    exportAccounts: vi.fn(),
  },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: null,
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/accounts", element: <AccountsListPage /> },
  { path: "/accounts/new", element: <p>New account form</p> },
  { path: "/accounts/:id", element: <p>Account detail</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listAccounts.mockResolvedValue({ accounts: [makeAccount()], nextPageToken: "" });
  crm.listTags.mockResolvedValue({ tags: [{ $typeName: "crm.v1.Tag", id: "tag-1", name: "Hot Lead", color: "#ff0000" }] });
  crm.bulkAssignTag.mockResolvedValue({ results: [{ id: "acc-1", success: true, error: "" }] });
  crm.bulkRemoveTag.mockResolvedValue({ results: [{ id: "acc-1", success: true, error: "" }] });
  crm.listSavedViews.mockResolvedValue({ views: [] });
  crm.createSavedView.mockResolvedValue({
    view: { $typeName: "crm.v1.SavedView", id: "view-1", objectType: "account", name: "Gold accounts", filters: { nameContains: "", tier: "gold" }, sort: "", columns: [] },
  });
  crm.deleteSavedView.mockResolvedValue({});
  crm.exportAccounts.mockResolvedValue({
    job: { $typeName: "crm.v1.ExportJob", id: "export-1", objectType: "account", rowCount: 1, requestedBy: "user-1" },
    content: "id,name\nacc-1,Acme Corp\n",
  });
});

/** The last argument ListAccounts was called with — the request the screen
 * actually put on the wire after debouncing. */
function lastRequest() {
  const calls = crm.listAccounts.mock.calls;
  return calls[calls.length - 1][0];
}

describe("AccountsListPage", () => {
  it("lists accounts and opens one on activation", async () => {
    renderRoutes(routes, ["/accounts"]);

    expect(await screen.findByText("Acme Corp")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Acme Corp"));
    expect(await screen.findByText("Account detail")).toBeInTheDocument();
  });

  it("pushes the search text down to the server rather than filtering locally", async () => {
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.change(screen.getByLabelText("Search by name"), { target: { value: "Umbrella" } });

    await waitFor(() => expect(lastRequest()).toMatchObject({ nameContains: "Umbrella" }), { timeout: 2000 });
  });

  it("sends only sort values the server's allowlist accepts", async () => {
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    const sort = screen.getByLabelText("Sort by") as HTMLSelectElement;
    // domain.ParseAccountSort rejects anything outside this set as invalid,
    // so the UI must never be able to offer a value that isn't in it.
    const offered = Array.from(sort.options).map((o) => o.value);
    expect(offered).toEqual(["", "-created_at"]);

    fireEvent.change(sort, { target: { value: "-created_at" } });
    await waitFor(() => expect(lastRequest()).toMatchObject({ sort: "-created_at" }));
  });

  it("distinguishes an empty tenant from an empty search result", async () => {
    crm.listAccounts.mockResolvedValue({ accounts: [], nextPageToken: "" });
    renderRoutes(routes, ["/accounts"]);

    expect(await screen.findByText("No accounts yet")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Search by name"), { target: { value: "Umbrella" } });

    expect(await screen.findByText("No matching accounts")).toBeInTheDocument();
    expect(screen.queryByText("No accounts yet")).not.toBeInTheDocument();
  });

  it("pages forward with the server's cursor and back through visited pages", async () => {
    crm.listAccounts.mockResolvedValue({ accounts: [makeAccount()], nextPageToken: "cursor-2" });
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    expect(screen.getByRole("button", { name: /previous/i })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /next/i }));

    // Keyset pagination only yields a forward cursor, so Previous is served
    // from the tokens this session has already visited.
    await waitFor(() => expect(lastRequest()).toMatchObject({ pageToken: "cursor-2" }));
    await waitFor(() => expect(screen.getByRole("button", { name: /previous/i })).toBeEnabled());

    fireEvent.click(screen.getByRole("button", { name: /previous/i }));
    await waitFor(() => expect(lastRequest()).toMatchObject({ pageToken: "" }));
  });

  it("shows the forbidden panel and no create action when listing is denied", async () => {
    crm.listAccounts.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/accounts"]);

    expect(await screen.findByText("You don't have access to this")).toBeInTheDocument();
    expect(screen.getByText("You don't have permission to view accounts.")).toBeInTheDocument();
  });

  it("hides the create action from a principal without the write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:account:write");
    renderRoutes(routes, ["/accounts"]);

    expect(await screen.findByText("Acme Corp")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New account" })).not.toBeInTheDocument();
  });

  // Static a11y over the data table and its pagination — the primitives every list screen reuses.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/accounts"]);
    // Wait for real rows: the loading skeleton renders a heading too, so
    // gating on <h1> would scan a placeholder and prove nothing.
    await screen.findByRole("table");
    await expectNoA11yViolations(container);
  });


  // Keyboard coverage for the row-activation pattern. The row used to carry
  // role="button" and tabIndex, which made it reachable but broke the table's
  // semantics; the control is now a real button in the row header cell, so
  // this pins that the keyboard path survived the change.
  it("opens a record from the keyboard, not only the mouse", async () => {
    const { router } = renderRoutes(routes, ["/accounts"]);
    await screen.findByRole("table");

    const opener = screen.getByRole("button", { name: "Acme Corp" });
    opener.focus();
    expect(opener).toHaveFocus();
    fireEvent.click(opener); // what Enter and Space dispatch on a real button

    await waitFor(() => expect(router.state.location.pathname).toBe("/accounts/acc-1"));
  });

  // The row names itself, so a cell read on its own says which record it
  // belongs to rather than only which column.
  it("gives every row a row header", async () => {
    renderRoutes(routes, ["/accounts"]);
    await screen.findByRole("table");

    const rowHeader = screen.getByRole("rowheader", { name: /Acme Corp/ });
    expect(rowHeader).toBeInTheDocument();
  });

  // Bulk tagging: select a row, pick a tag, and the batch call carries the
  // selected id — the same "one bulk call, not a loop" contract the tasks
  // list's bulk actions have.
  it("bulk-tags the selected accounts", async () => {
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.click(screen.getByRole("checkbox", { name: "Select Acme Corp" }));
    expect(screen.getByText("1 account selected")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Tag"), { target: { value: "tag-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add tag to selected" }));

    await waitFor(() => expect(crm.bulkAssignTag).toHaveBeenCalledWith({
      tagId: "tag-1", objectType: "account", objectIds: ["acc-1"],
    }));
    await waitFor(() => expect(screen.queryByText(/account selected/)).not.toBeInTheDocument());
  });

  it("bulk-removes a tag from the selected accounts", async () => {
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.click(screen.getByRole("checkbox", { name: "Select Acme Corp" }));
    fireEvent.change(screen.getByLabelText("Tag"), { target: { value: "tag-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove tag from selected" }));

    await waitFor(() => expect(crm.bulkRemoveTag).toHaveBeenCalledWith({
      tagId: "tag-1", objectType: "account", objectIds: ["acc-1"],
    }));
  });

  it("offers no bulk-tag controls without the tag write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:tag:write");
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    expect(screen.queryByRole("checkbox", { name: /^Select /i })).not.toBeInTheDocument();
  });

  it("has no accessibility violations with the bulk-tag toolbar open", async () => {
    const { container } = renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Acme Corp" }));
    await screen.findByText("1 account selected");
    await expectNoA11yViolations(container);
  });

  // Saved views: the current filter/sort state, not a copy the server
  // invents — the request must carry exactly what's on screen.
  it("saves the current filters as a named view", async () => {
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.change(screen.getByLabelText("Tier"), { target: { value: "gold" } });
    fireEvent.click(screen.getByRole("button", { name: "Save current view" }));
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText("View name"), { target: { value: "Gold accounts" } });
    fireEvent.click(screen.getByRole("button", { name: "Save view" }));

    await waitFor(() => expect(crm.createSavedView).toHaveBeenCalledWith({
      objectType: "account", name: "Gold accounts", filters: { nameContains: "", tier: "gold" }, sort: "",
    }));
  });

  it("applies a saved view's filters when picked", async () => {
    crm.listSavedViews.mockResolvedValue({
      views: [{ $typeName: "crm.v1.SavedView", id: "view-1", objectType: "account", name: "Gold accounts", filters: { nameContains: "Acme", tier: "gold" }, sort: "-created_at", columns: [] }],
    });
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.change(await screen.findByLabelText("Saved view"), { target: { value: "view-1" } });

    await waitFor(() => {
      expect((screen.getByLabelText("Search by name") as HTMLInputElement).value).toBe("Acme");
      expect((screen.getByLabelText("Tier") as HTMLInputElement).value).toBe("gold");
      expect((screen.getByLabelText("Sort by") as HTMLSelectElement).value).toBe("-created_at");
    });
  });

  it("deletes the active saved view", async () => {
    crm.listSavedViews.mockResolvedValue({
      views: [{ $typeName: "crm.v1.SavedView", id: "view-1", objectType: "account", name: "Gold accounts", filters: {}, sort: "", columns: [] }],
    });
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.change(await screen.findByLabelText("Saved view"), { target: { value: "view-1" } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Delete view" })).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Delete view" }));

    await waitFor(() => expect(crm.deleteSavedView).toHaveBeenCalledWith({ id: "view-1" }));
  });

  it("exports accounts as a CSV download", async () => {
    const url = URL.createObjectURL;
    const revoke = URL.revokeObjectURL;
    URL.createObjectURL = vi.fn().mockReturnValue("blob:mock");
    URL.revokeObjectURL = vi.fn();
    try {
      renderRoutes(routes, ["/accounts"]);
      await screen.findByText("Acme Corp");

      fireEvent.click(screen.getByRole("button", { name: "Export CSV" }));

      await waitFor(() => expect(crm.exportAccounts).toHaveBeenCalledWith({}));
      expect(await screen.findByText("Exported 1 accounts.")).toBeInTheDocument();
    } finally {
      URL.createObjectURL = url;
      URL.revokeObjectURL = revoke;
    }
  });

  it("navigates to the CSV import flow", async () => {
    const { router } = renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    fireEvent.click(screen.getByRole("button", { name: "Import CSV" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/accounts/import"));
  });

  it("hides export and import actions without account scopes", async () => {
    auth.hasScope.mockReturnValue(false);
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    expect(screen.queryByRole("button", { name: "Export CSV" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Import CSV" })).not.toBeInTheDocument();
  });

  it("offers no saved-view controls without the saved_view scopes", async () => {
    auth.hasScope.mockImplementation((scope: string) => !scope.startsWith("crm:saved_view"));
    renderRoutes(routes, ["/accounts"]);
    await screen.findByText("Acme Corp");

    expect(screen.queryByRole("button", { name: "Save current view" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Saved view")).not.toBeInTheDocument();
  });

});
