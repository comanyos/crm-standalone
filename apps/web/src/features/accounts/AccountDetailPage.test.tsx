import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import AccountDetailPage from "./AccountDetailPage";
import { makeAccount, makeContact, renderRoutes } from "../../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getAccount: vi.fn(),
    listAccounts: vi.fn(),
    listContacts: vi.fn(),
    listActivitiesForSubject: vi.fn(),
    deleteAccount: vi.fn(),
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
  { path: "/accounts", element: <p>Accounts list</p> },
  { path: "/accounts/:id", element: <AccountDetailPage /> },
  { path: "/accounts/:id/edit", element: <p>Edit form</p> },
  { path: "/contacts/new", element: <p>New contact form</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getAccount.mockResolvedValue({ account: makeAccount() });
  crm.listAccounts.mockResolvedValue({ accounts: [], nextPageToken: "" });
  crm.listContacts.mockResolvedValue({ contacts: [], nextPageToken: "" });
  crm.listActivitiesForSubject.mockResolvedValue({ activities: [] });
  crm.deleteAccount.mockResolvedValue({});
});

describe("AccountDetailPage", () => {
  it("renders the account with its children, contacts and activity", async () => {
    crm.listAccounts.mockResolvedValue({
      accounts: [makeAccount({ id: "acc-child", name: "Acme Subsidiary" })],
      nextPageToken: "",
    });
    crm.listContacts.mockResolvedValue({ contacts: [makeContact()], nextPageToken: "" });

    renderRoutes(routes, ["/accounts/acc-1"]);

    expect(await screen.findByRole("heading", { name: "Acme Corp" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Acme Subsidiary" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Ada Lovelace" })).toBeInTheDocument();

    // Child accounts must come from a server-side parent_id filter, not a
    // client-side scan of every account in the tenant.
    expect(crm.listAccounts).toHaveBeenCalledWith(expect.objectContaining({ parentId: "acc-1" }));
    expect(crm.listContacts).toHaveBeenCalledWith(expect.objectContaining({ accountId: "acc-1" }));
  });

  it("shows the same not-found panel for a missing and a cross-tenant account", async () => {
    crm.getAccount.mockRejectedValue(new ConnectError("no such account", Code.NotFound));
    renderRoutes(routes, ["/accounts/acc-other-tenant"]);

    expect(await screen.findByText("Not found")).toBeInTheDocument();
    expect(
      screen.getByText("This record doesn't exist, or you don't have access to it."),
    ).toBeInTheDocument();
    // Nothing about the record may leak — not even its name in a heading.
    expect(screen.queryByText("Acme Corp")).not.toBeInTheDocument();
  });

  it("shows the forbidden panel when the read is denied", async () => {
    crm.getAccount.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/accounts/acc-1"]);

    expect(await screen.findByText("You don't have access to this")).toBeInTheDocument();
    expect(screen.getByText("You don't have permission to view this account.")).toBeInTheDocument();
  });

  it("hides write actions from a principal without the write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:account:write");
    renderRoutes(routes, ["/accounts/acc-1"]);

    expect(await screen.findByRole("heading", { name: "Acme Corp" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("deletes only after the confirmation is accepted", async () => {
    renderRoutes(routes, ["/accounts/acc-1"]);

    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Delete Acme Corp? This cannot be undone.")).toBeInTheDocument();
    expect(crm.deleteAccount).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(crm.deleteAccount).toHaveBeenCalledWith({ id: "acc-1" }));
    expect(await screen.findByText("Accounts list")).toBeInTheDocument();
  });

  it("keeps the account and explains why when a delete is denied", async () => {
    crm.deleteAccount.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/accounts/acc-1"]);

    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete" }));

    expect(await screen.findByText("You don't have permission to do that.")).toBeInTheDocument();
    expect(screen.queryByText("Accounts list")).not.toBeInTheDocument();
  });

  it("flags a possible duplicate without offering to merge it", async () => {
    crm.getAccount.mockResolvedValue({ account: makeAccount({ duplicateOfAccountId: "acc-2" }) });
    renderRoutes(routes, ["/accounts/acc-1"]);

    expect(await screen.findByRole("link", { name: "another account" })).toHaveAttribute(
      "href",
      "/accounts/acc-2",
    );
    expect(screen.queryByRole("button", { name: /merge/i })).not.toBeInTheDocument();
  });

  it("carries the account into the contact form from the Add contact shortcut", async () => {
    const { router } = renderRoutes(routes, ["/accounts/acc-1"]);

    fireEvent.click(await screen.findByRole("button", { name: "Add contact" }));

    expect(await screen.findByText("New contact form")).toBeInTheDocument();
    const params = new URLSearchParams(router.state.location.search);
    expect(params.get("accountId")).toBe("acc-1");
    expect(params.get("accountName")).toBe("Acme Corp");
  });
});

describe("AccountDetailPage — partial failures", () => {
  it("reports a failed child-accounts load instead of showing an empty section", async () => {
    crm.listAccounts.mockRejectedValue(new ConnectError("boom", Code.Unavailable));
    renderRoutes(routes, ["/accounts/acc-1"]);

    expect(await screen.findByRole("heading", { name: "Acme Corp" })).toBeInTheDocument();
    expect(await screen.findByText("The service is temporarily unavailable")).toBeInTheDocument();
    // "No child accounts" would be a lie: we don't know that.
    expect(screen.queryByText("No child accounts.")).not.toBeInTheDocument();
  });

  it("reports a failed activity load instead of claiming there is no activity", async () => {
    crm.listActivitiesForSubject.mockRejectedValue(new ConnectError("boom", Code.Unavailable));
    renderRoutes(routes, ["/accounts/acc-1"]);

    expect(await screen.findByRole("heading", { name: "Acme Corp" })).toBeInTheDocument();
    expect(await screen.findByText("The service is temporarily unavailable")).toBeInTheDocument();
    expect(screen.queryByText("No activity recorded.")).not.toBeInTheDocument();
  });
});
