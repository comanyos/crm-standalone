import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import AccountFormPage from "./AccountFormPage";
import { makeAccount, makeTimestamp, renderRoutes } from "../../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getAccount: vi.fn(),
    createAccount: vi.fn(),
    updateAccount: vi.fn(),
    listAccounts: vi.fn(),
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

const savedAt = makeTimestamp("2026-08-21T10:00:00Z");

const routes = [
  { path: "/accounts", element: <p>Accounts list</p> },
  { path: "/accounts/new", element: <AccountFormPage mode="create" /> },
  { path: "/accounts/:id", element: <p>Account detail</p> },
  { path: "/accounts/:id/edit", element: <AccountFormPage mode="edit" /> },
];

beforeEach(() => {
  // vitest's `restoreMocks` only restores vi.spyOn spies, not standalone
  // vi.fn()s, so call history would otherwise leak between tests.
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getAccount.mockResolvedValue({ account: makeAccount() });
  crm.createAccount.mockResolvedValue({ account: makeAccount({ id: "acc-new", name: "New Co" }) });
  crm.updateAccount.mockResolvedValue({ account: makeAccount() });
  crm.listAccounts.mockResolvedValue({ accounts: [], nextPageToken: "" });
});

function fill(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("AccountFormPage — create", () => {
  it("sends the typed values and navigates to the new account", async () => {
    renderRoutes(routes, ["/accounts/new"]);

    fill(/^Name/, "New Co");
    fill("Owner ID", "user-9");
    fill("Industry", "Mining");
    fireEvent.click(screen.getByRole("button", { name: "Create account" }));

    await waitFor(() => expect(crm.createAccount).toHaveBeenCalledTimes(1));
    expect(crm.createAccount.mock.calls[0][0].account).toMatchObject({
      name: "New Co",
      ownerId: "user-9",
      industry: "Mining",
    });
    expect(await screen.findByText("Account detail")).toBeInTheDocument();
  });

  it("blocks submission and shows a field error when a required field is empty", async () => {
    renderRoutes(routes, ["/accounts/new"]);

    fireEvent.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByText("Name is required")).toBeInTheDocument();
    expect(crm.createAccount).not.toHaveBeenCalled();
  });

  it("rejects a website that isn't an http(s) URL before any network call", async () => {
    renderRoutes(routes, ["/accounts/new"]);

    fill(/^Name/, "New Co");
    fill("Owner ID", "user-9");
    fill("Website", "acme.example");
    fireEvent.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByText("Website must start with http:// or https://")).toBeInTheDocument();
    expect(crm.createAccount).not.toHaveBeenCalled();
  });

  it("surfaces a permission-denied create as a message instead of navigating", async () => {
    crm.createAccount.mockRejectedValue(new ConnectError("nope", Code.PermissionDenied));
    renderRoutes(routes, ["/accounts/new"]);

    fill(/^Name/, "New Co");
    fill("Owner ID", "user-9");
    fireEvent.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByText("You don't have permission to do that.")).toBeInTheDocument();
    expect(screen.queryByText("Account detail")).not.toBeInTheDocument();
  });
});

describe("AccountFormPage — edit", () => {
  it("prefills from the loaded account and round-trips fields the form doesn't expose", async () => {
    crm.getAccount.mockResolvedValue({
      account: makeAccount({ id: "acc-1", name: "Acme Corp", parentId: "acc-parent", updatedAt: savedAt }),
    });
    renderRoutes(routes, ["/accounts/acc-1/edit"]);

    await waitFor(() => expect(screen.getByLabelText(/^Name/)).toHaveValue("Acme Corp"));

    fill(/^Name/, "Acme Corporation");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(crm.updateAccount).toHaveBeenCalledTimes(1));
    const sent = crm.updateAccount.mock.calls[0][0].account;
    expect(sent).toMatchObject({ id: "acc-1", name: "Acme Corporation", parentId: "acc-parent" });
    // Optimistic concurrency: the loaded record's updated_at must go back so
    // the server can detect a concurrent write (internal/domain UpdateAccount).
    expect(sent.updatedAt).toEqual(savedAt);
  });

  it("tells the user to reload when the server reports a concurrent change", async () => {
    crm.updateAccount.mockRejectedValue(new ConnectError("stale", Code.Aborted));
    renderRoutes(routes, ["/accounts/acc-1/edit"]);

    await waitFor(() => expect(screen.getByLabelText(/^Name/)).toHaveValue("Acme Corp"));
    fill(/^Name/, "Acme Corporation");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(
      await screen.findByText("This record was changed by someone else. Reload the page and try again."),
    ).toBeInTheDocument();
  });

  it("shows the shared not-found panel for a missing or cross-tenant account", async () => {
    crm.getAccount.mockRejectedValue(new ConnectError("no such account", Code.NotFound));
    renderRoutes(routes, ["/accounts/acc-missing/edit"]);

    expect(await screen.findByText("Not found")).toBeInTheDocument();
    // The wording must not distinguish "absent" from "another tenant's".
    expect(
      screen.getByText("This record doesn't exist, or you don't have access to it."),
    ).toBeInTheDocument();
  });

  it("shows the forbidden panel rather than an empty form when the load is denied", async () => {
    crm.getAccount.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/accounts/acc-1/edit"]);

    expect(await screen.findByText("You don't have access to this")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});

describe("AccountFormPage — unsaved changes", () => {
  it("asks before discarding edits, and stays put when the user cancels", async () => {
    const { router } = renderRoutes(routes, ["/accounts/new"]);

    fill(/^Name/, "Half typed");
    await act(async () => {
      router.navigate("/accounts");
    });

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveAttribute("open");
    expect(within(dialog).getByText("Discard unsaved changes?")).toBeInTheDocument();

    // Scoped to the dialog: the form underneath has its own Cancel button.
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    // <dialog> stays mounted when closed — the browser hides it via the
    // `open` attribute, so that, not unmounting, is what to assert on.
    await waitFor(() => expect(dialog).not.toHaveAttribute("open"));
    expect(screen.getByLabelText(/^Name/)).toHaveValue("Half typed");
    expect(screen.queryByText("Accounts list")).not.toBeInTheDocument();
  });

  it("leaves the page when the user confirms the discard", async () => {
    const { router } = renderRoutes(routes, ["/accounts/new"]);

    fill(/^Name/, "Half typed");
    await act(async () => {
      router.navigate("/accounts");
    });
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));

    expect(await screen.findByText("Accounts list")).toBeInTheDocument();
  });

  it("does not prompt on the navigation that follows a successful save", async () => {
    renderRoutes(routes, ["/accounts/new"]);

    fill(/^Name/, "New Co");
    fill("Owner ID", "user-9");
    fireEvent.click(screen.getByRole("button", { name: "Create account" }));

    // The form is still dirty when onSuccess navigates, so the guard has to
    // be disarmed explicitly — otherwise saving prompts to discard the very
    // changes that were just saved.
    expect(await screen.findByText("Account detail")).toBeInTheDocument();
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });
});
