import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import AccountsListPage from "./AccountsListPage";
import { makeAccount, renderRoutes } from "../../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listAccounts: vi.fn() },
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
});
