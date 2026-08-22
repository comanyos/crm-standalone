import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import ContactDetailPage from "./ContactDetailPage";
import { makeAccount, makeContact, renderRoutes } from "../../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: { getContact: vi.fn(), getAccount: vi.fn(), deleteContact: vi.fn() },
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
  { path: "/contacts", element: <p>Contacts list</p> },
  { path: "/contacts/:id", element: <ContactDetailPage /> },
  { path: "/contacts/:id/edit", element: <p>Edit form</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getContact.mockResolvedValue({ contact: makeContact() });
  crm.getAccount.mockResolvedValue({ account: makeAccount() });
  crm.deleteContact.mockResolvedValue({});
});

describe("ContactDetailPage", () => {
  it("renders the contact and links to its account by name", async () => {
    crm.getContact.mockResolvedValue({ contact: makeContact({ title: "Engineer", role: "technical" }) });
    renderRoutes(routes, ["/contacts/con-1"]);

    expect(await screen.findByRole("heading", { name: "Ada Lovelace" })).toBeInTheDocument();
    expect(screen.getByText("ada@acme.example")).toBeInTheDocument();
    expect(screen.getByText("Engineer")).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Acme Corp" })).toHaveAttribute("href", "/accounts/acc-1");
  });

  it("shows the same not-found panel for a missing and a cross-tenant contact", async () => {
    crm.getContact.mockRejectedValue(new ConnectError("no such contact", Code.NotFound));
    renderRoutes(routes, ["/contacts/con-other-tenant"]);

    expect(await screen.findByText("Not found")).toBeInTheDocument();
    expect(
      screen.getByText("This record doesn't exist, or you don't have access to it."),
    ).toBeInTheDocument();
    expect(screen.queryByText("ada@acme.example")).not.toBeInTheDocument();
  });

  it("shows the forbidden panel when the read is denied", async () => {
    crm.getContact.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/contacts/con-1"]);

    expect(await screen.findByText("You don't have access to this")).toBeInTheDocument();
    expect(screen.getByText("You don't have permission to view this contact.")).toBeInTheDocument();
  });

  it("hides write actions from a principal without the write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:contact:write");
    renderRoutes(routes, ["/contacts/con-1"]);

    expect(await screen.findByRole("heading", { name: "Ada Lovelace" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("deletes only after the confirmation is accepted", async () => {
    renderRoutes(routes, ["/contacts/con-1"]);

    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(crm.deleteContact).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(crm.deleteContact).toHaveBeenCalledWith({ id: "con-1" }));
    expect(await screen.findByText("Contacts list")).toBeInTheDocument();
  });

  it("flags a duplicate email without offering to merge", async () => {
    crm.getContact.mockImplementation(({ id }: { id: string }) =>
      Promise.resolve({
        contact:
          id === "con-1"
            ? makeContact({ duplicateOfContactId: "con-2" })
            : makeContact({ id: "con-2", firstName: "Grace", lastName: "Hopper" }),
      }),
    );
    renderRoutes(routes, ["/contacts/con-1"]);

    expect(await screen.findByRole("link", { name: "Grace Hopper" })).toHaveAttribute(
      "href",
      "/contacts/con-2",
    );
    expect(screen.queryByRole("button", { name: /merge/i })).not.toBeInTheDocument();
  });
});
