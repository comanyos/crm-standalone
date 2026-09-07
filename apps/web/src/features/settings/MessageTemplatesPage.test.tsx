import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import MessageTemplatesPage from "./MessageTemplatesPage";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listMessageTemplates: vi.fn(),
    describeMessageTemplateCatalog: vi.fn(),
    createMessageTemplate: vi.fn(),
    updateMessageTemplate: vi.fn(),
    deleteMessageTemplate: vi.fn(),
  },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1", email: "rep@example.com" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/settings/message-templates", element: <MessageTemplatesPage /> }];

const template = {
  id: "tpl-1",
  name: "Intro",
  channelKind: "whatsapp",
  subject: "",
  body: "Hi {{contact.first_name}}, about {{account.name}}.",
  createdBy: "user-1",
};

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listMessageTemplates.mockResolvedValue({ templates: [template] });
  crm.describeMessageTemplateCatalog.mockResolvedValue({
    channelKinds: ["email", "sms", "whatsapp"],
    mergeFields: ["account.name", "contact.first_name", "contact.full_name", "sender.email"],
  });
  crm.createMessageTemplate.mockResolvedValue({ template: { ...template, id: "tpl-2" } });
  crm.updateMessageTemplate.mockResolvedValue({ template });
  crm.deleteMessageTemplate.mockResolvedValue({});
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/message-templates"]);
  await screen.findByText("Intro");
  return view;
}

describe("MessageTemplatesPage", () => {
  it("lists the tenant's templates with their channel", async () => {
    await loaded();
    // Scoped to the table: the closed editor's channel <select> also has a
    // "WhatsApp" option in the DOM, and a bare getByText would match either.
    const table = screen.getByRole("table", { name: /Message templates/i });
    expect(within(table).getByText("WhatsApp")).toBeInTheDocument();
    expect(within(table).getByText("Intro")).toBeInTheDocument();
  });

  it("creates a template", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New template" }));
    await screen.findByRole("dialog");

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Follow-up" } });
    fireEvent.change(screen.getByLabelText("Body"), {
      target: { value: "Hi {{contact.first_name}}," },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));

    await waitFor(() => expect(crm.createMessageTemplate).toHaveBeenCalled());
    expect(crm.createMessageTemplate).toHaveBeenCalledWith({
      name: "Follow-up",
      channelKind: "whatsapp",
      subject: "",
      body: "Hi {{contact.first_name}},",
    });
  });

  it("previews the wording with the placeholders filled in", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New template" }));
    await screen.findByRole("dialog");

    fireEvent.change(screen.getByLabelText("Body"), {
      target: { value: "Hi {{contact.first_name}}, about {{account.name}}." },
    });

    // The author sees what a recipient sees, not the raw braces — that is the
    // only way to notice a template that reads wrong once substituted.
    expect(await screen.findByText("Hi Alex, about Example Ltd.")).toBeInTheDocument();
  });

  it("offers a subject only for email, and does not send a stale one", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New template" }));
    await screen.findByRole("dialog");

    // WhatsApp by default: no subject field.
    expect(screen.queryByLabelText("Subject")).toBeNull();

    fireEvent.change(screen.getByLabelText("Channel"), { target: { value: "email" } });
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "About your renewal" } });

    // Switching back hides it. The stale value must not be sent: the server
    // refuses a subject on a non-email template, and the author would get an
    // error about a field the form no longer shows.
    fireEvent.change(screen.getByLabelText("Channel"), { target: { value: "sms" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Nudge" } });
    fireEvent.change(screen.getByLabelText("Body"), { target: { value: "Quick nudge" } });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));

    await waitFor(() => expect(crm.createMessageTemplate).toHaveBeenCalled());
    expect(crm.createMessageTemplate.mock.calls[0][0].subject).toBe("");
  });

  it("inserts a placeholder at the click of its chip", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New template" }));
    await screen.findByRole("dialog");

    fireEvent.change(screen.getByLabelText("Body"), { target: { value: "Hi " } });
    fireEvent.click(screen.getByRole("button", { name: "{{contact.first_name}}" }));

    expect((screen.getByLabelText("Body") as HTMLTextAreaElement).value).toBe("Hi {{contact.first_name}}");
  });

  it("confirms before deleting, and says what a delete does not affect", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Delete template Intro" }));

    // Messages already sent are their own timeline entries; saying so stops
    // an author fearing they are erasing history.
    expect(await screen.findByText(/Messages already sent are unaffected/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Delete template" }));
    await waitFor(() => expect(crm.deleteMessageTemplate).toHaveBeenCalledWith({ id: "tpl-1" }));
  });

  it("hides the editing controls from a reader", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:template:write");
    await loaded();
    expect(screen.queryByRole("button", { name: "New template" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Delete template/ })).toBeNull();
  });

  it("explains an empty page to a user with no template scope", async () => {
    auth.hasScope.mockReturnValue(false);
    renderRoutes(routes, ["/settings/message-templates"]);
    expect(await screen.findByText("Access required")).toBeInTheDocument();
    expect(crm.listMessageTemplates).not.toHaveBeenCalled();
  });

  it("has no accessibility violations, with the editor open", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);

    fireEvent.click(screen.getByRole("button", { name: "New template" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });
});
