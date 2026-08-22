import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import ContactFormPage from "./ContactFormPage";
import { makeAccount, makeContact, makeTimestamp, renderRoutes, sampleCustomFields } from "../../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getContact: vi.fn(),
    getAccount: vi.fn(),
    listAccounts: vi.fn(),
    createContact: vi.fn(),
    updateContact: vi.fn(),
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
  { path: "/contacts", element: <p>Contacts list</p> },
  { path: "/contacts/new", element: <ContactFormPage mode="create" /> },
  { path: "/contacts/:id", element: <p>Contact detail</p> },
  { path: "/contacts/:id/edit", element: <ContactFormPage mode="edit" /> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getContact.mockResolvedValue({ contact: makeContact() });
  crm.getAccount.mockResolvedValue({ account: makeAccount() });
  crm.listAccounts.mockResolvedValue({ accounts: [makeAccount()], nextPageToken: "" });
  crm.createContact.mockResolvedValue({ contact: makeContact({ id: "con-new" }) });
  crm.updateContact.mockResolvedValue({ contact: makeContact() });
});

/** Drives the AccountPicker combobox the way a user does: focus it, type,
 * then click a live search result. There is no free-text path — the picker
 * only ever commits an id it got back from ListAccounts. */
async function pickAccount(name: string) {
  const combobox = screen.getByRole("combobox", { name: "Account" });
  fireEvent.focus(combobox);
  fireEvent.change(combobox, { target: { value: name } });
  fireEvent.click(await screen.findByRole("button", { name }));
}

describe("ContactFormPage — create", () => {
  it("creates a contact against an account chosen from the picker", async () => {
    renderRoutes(routes, ["/contacts/new"]);

    fireEvent.change(screen.getByLabelText(/^Email/), { target: { value: "grace@acme.example" } });
    await pickAccount("Acme Corp");
    fireEvent.change(screen.getByLabelText("First name"), { target: { value: "Grace" } });
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "technical" } });
    fireEvent.click(screen.getByRole("button", { name: "Create contact" }));

    await waitFor(() => expect(crm.createContact).toHaveBeenCalledTimes(1));
    expect(crm.createContact.mock.calls[0][0].contact).toMatchObject({
      email: "grace@acme.example",
      accountId: "acc-1",
      firstName: "Grace",
      role: "technical",
    });
    expect(await screen.findByText("Contact detail")).toBeInTheDocument();
  });

  it("requires an account, since a contact must belong to a real one", async () => {
    renderRoutes(routes, ["/contacts/new"]);

    fireEvent.change(screen.getByLabelText(/^Email/), { target: { value: "grace@acme.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Create contact" }));

    expect(await screen.findByText("Select an account")).toBeInTheDocument();
    expect(crm.createContact).not.toHaveBeenCalled();
  });

  it("rejects a malformed email before any network call", async () => {
    renderRoutes(routes, ["/contacts/new"]);

    fireEvent.change(screen.getByLabelText(/^Email/), { target: { value: "not-an-email" } });
    await pickAccount("Acme Corp");
    fireEvent.click(screen.getByRole("button", { name: "Create contact" }));

    expect(await screen.findByText("Enter a valid email address")).toBeInTheDocument();
    expect(crm.createContact).not.toHaveBeenCalled();
  });

  it("prefills the account from the Add-contact shortcut's query parameters", async () => {
    renderRoutes(routes, ["/contacts/new?accountId=acc-1&accountName=Acme%20Corp"]);

    expect(await screen.findByDisplayValue("Acme Corp")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/^Email/), { target: { value: "grace@acme.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Create contact" }));

    await waitFor(() => expect(crm.createContact).toHaveBeenCalledTimes(1));
    expect(crm.createContact.mock.calls[0][0].contact).toMatchObject({ accountId: "acc-1" });
  });

  it("warns about a duplicate email after saving, without blocking or merging", async () => {
    crm.createContact.mockResolvedValue({
      contact: makeContact({ id: "con-new", duplicateOfContactId: "con-1" }),
    });
    renderRoutes(routes, ["/contacts/new"]);

    fireEvent.change(screen.getByLabelText(/^Email/), { target: { value: "ada@acme.example" } });
    await pickAccount("Acme Corp");
    fireEvent.click(screen.getByRole("button", { name: "Create contact" }));

    expect(
      await screen.findByText(
        "Saved — this email matches another contact. Review both before assuming they're the same person.",
      ),
    ).toBeInTheDocument();
    // The save still went through: warning, not rejection.
    expect(await screen.findByText("Contact detail")).toBeInTheDocument();
  });

  it("reports a dangling account rejected by the server", async () => {
    crm.createContact.mockRejectedValue(new ConnectError("account not found", Code.InvalidArgument));
    renderRoutes(routes, ["/contacts/new"]);

    fireEvent.change(screen.getByLabelText(/^Email/), { target: { value: "grace@acme.example" } });
    await pickAccount("Acme Corp");
    fireEvent.click(screen.getByRole("button", { name: "Create contact" }));

    expect(await screen.findByText(/account not found/)).toBeInTheDocument();
    expect(screen.queryByText("Contact detail")).not.toBeInTheDocument();
  });
});

describe("ContactFormPage — edit", () => {
  it("prefills the contact and its account, and sends updated_at back", async () => {
    crm.getContact.mockResolvedValue({
      contact: makeContact({ title: "Engineer", updatedAt: savedAt }),
    });
    renderRoutes(routes, ["/contacts/con-1/edit"]);

    await waitFor(() => expect(screen.getByLabelText(/^Email/)).toHaveValue("ada@acme.example"));
    expect(await screen.findByDisplayValue("Acme Corp")).toBeInTheDocument();
    expect(screen.getByLabelText("Title")).toHaveValue("Engineer");

    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Principal Engineer" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(crm.updateContact).toHaveBeenCalledTimes(1));
    const sent = crm.updateContact.mock.calls[0][0].contact;
    expect(sent).toMatchObject({ id: "con-1", title: "Principal Engineer" });
    expect(sent.updatedAt).toEqual(savedAt);
    // UpdateContact replaces the custom field document rather than merging it,
    // so a save without it deletes every custom value on the contact.
    expect(sent.customFields).toEqual(sampleCustomFields);
  });

  it("tells the user to reload when the server reports a concurrent change", async () => {
    crm.updateContact.mockRejectedValue(new ConnectError("stale", Code.Aborted));
    renderRoutes(routes, ["/contacts/con-1/edit"]);

    await waitFor(() => expect(screen.getByLabelText("Title")).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Principal Engineer" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(
      await screen.findByText("This record was changed by someone else. Reload the page and try again."),
    ).toBeInTheDocument();
  });

  it("shows the shared not-found panel for a missing or cross-tenant contact", async () => {
    crm.getContact.mockRejectedValue(new ConnectError("no such contact", Code.NotFound));
    renderRoutes(routes, ["/contacts/con-missing/edit"]);

    expect(await screen.findByText("Not found")).toBeInTheDocument();
    expect(
      screen.getByText("This record doesn't exist, or you don't have access to it."),
    ).toBeInTheDocument();
  });

  it("shows the forbidden panel rather than an empty form when the load is denied", async () => {
    crm.getContact.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    renderRoutes(routes, ["/contacts/con-1/edit"]);

    expect(await screen.findByText("You don't have access to this")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});
