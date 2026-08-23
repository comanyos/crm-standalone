import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import ContactsListPage from "./ContactsListPage";
import { makeContact, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listContacts: vi.fn() },
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
  { path: "/contacts", element: <ContactsListPage /> },
  { path: "/contacts/new", element: <p>New contact form</p> },
  { path: "/contacts/:id", element: <p>Contact detail</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listContacts.mockResolvedValue({ contacts: [makeContact()], nextPageToken: "" });
});

function lastRequest() {
  const calls = crm.listContacts.mock.calls;
  return calls[calls.length - 1][0];
}

describe("ContactsListPage", () => {
  it("lists contacts and opens one on activation", async () => {
    renderRoutes(routes, ["/contacts"]);

    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Ada Lovelace"));
    expect(await screen.findByText("Contact detail")).toBeInTheDocument();
  });

  it("pushes name and email search down to the server", async () => {
    renderRoutes(routes, ["/contacts"]);
    await screen.findByText("Ada Lovelace");

    fireEvent.change(screen.getByLabelText("Search by name"), { target: { value: "Grace" } });
    await waitFor(() => expect(lastRequest()).toMatchObject({ nameContains: "Grace" }), { timeout: 2000 });

    fireEvent.change(screen.getByLabelText("Search by email"), { target: { value: "@umbrella" } });
    await waitFor(() => expect(lastRequest()).toMatchObject({ emailContains: "@umbrella" }), {
      timeout: 2000,
    });
  });

  it("sends only sort values the server's allowlist accepts", async () => {
    renderRoutes(routes, ["/contacts"]);
    await screen.findByText("Ada Lovelace");

    const sort = screen.getByLabelText("Sort by") as HTMLSelectElement;
    // Mirrors domain.ParseContactSort's allowlist.
    expect(Array.from(sort.options).map((o) => o.value)).toEqual(["", "email"]);

    fireEvent.change(sort, { target: { value: "email" } });
    await waitFor(() => expect(lastRequest()).toMatchObject({ sort: "email" }));
  });

  it("shows the forbidden panel when listing is denied", async () => {
    crm.listContacts.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/contacts"]);

    expect(await screen.findByText("You don't have access to this")).toBeInTheDocument();
  });

  it("hides the create action from a principal without the write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:contact:write");
    renderRoutes(routes, ["/contacts"]);

    expect(await screen.findByText("Ada Lovelace")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New contact" })).not.toBeInTheDocument();
  });

  // Static a11y over another table, to catch a per-page column definition rather than a shared one.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/contacts"]);
    await screen.findByRole("table");
    await expectNoA11yViolations(container);
  });

});
