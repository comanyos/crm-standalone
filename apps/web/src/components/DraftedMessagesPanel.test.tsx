import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DraftedMessagesPanel } from "./DraftedMessagesPanel";
import { renderRoutes } from "../test/harness";
import { expectNoA11yViolations } from "../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listOutboundMessages: vi.fn(),
    markOutboundMessageSent: vi.fn(),
    discardOutboundMessage: vi.fn(),
    sendOutboundMessage: vi.fn(),
    getGoogleConnection: vi.fn(),
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

vi.mock("../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/", element: <DraftedMessagesPanel subjectType="lead" subjectId="l-1" /> }];

function draft(over: Record<string, unknown> = {}) {
  return {
    id: "msg-1",
    subjectType: "lead",
    subjectId: "l-1",
    channelKind: "email",
    address: "alex@example.com",
    subject: "About Example Ltd",
    body: "Hi, about your enquiry.",
    status: "pending",
    assigneeId: "user-1",
    mayStillSend: true,
    refusalReason: "",
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listOutboundMessages.mockResolvedValue({ messages: [draft()] });
  crm.markOutboundMessageSent.mockResolvedValue({ message: draft({ status: "sent" }) });
  crm.discardOutboundMessage.mockResolvedValue({ message: draft({ status: "discarded" }) });
  crm.sendOutboundMessage.mockResolvedValue({
    message: draft({ status: "sent" }),
    providerMessageId: "gmail-1",
  });
  // No connected mailbox by default: sending from the CRM is the exception,
  // not the norm, and most deployments have no Google client at all.
  crm.getGoogleConnection.mockResolvedValue({ connection: undefined });
});

describe("DraftedMessagesPanel", () => {
  it("shows the drafted wording and opens it in the right app", async () => {
    renderRoutes(routes);

    expect(await screen.findByText("Hi, about your enquiry.")).toBeInTheDocument();
    const open = screen.getByRole("link", { name: /Open in Email/ }) as HTMLAnchorElement;
    expect(open.href.startsWith("mailto:alex%40example.com")).toBe(true);
    // The rendered body and subject travel into the composer, so the rep does
    // not retype what the automation already wrote.
    expect(decodeURIComponent(open.href)).toContain("Hi, about your enquiry.");
    expect(decodeURIComponent(open.href)).toContain("About Example Ltd");
  });

  it("marks sent only when the user says so, not when they open the app", async () => {
    renderRoutes(routes);
    const open = await screen.findByRole("link", { name: /Open in Email/ });

    fireEvent.click(open);
    // Opening is not sending: the user may close the composer, edit the text,
    // or change their mind. The CRM records what it actually knows.
    expect(crm.markOutboundMessageSent).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Mark as sent" }));
    await waitFor(() => expect(crm.markOutboundMessageSent).toHaveBeenCalledWith({ id: "msg-1" }));
  });

  it("refuses a draft written before the contact opted out", async () => {
    crm.listOutboundMessages.mockResolvedValue({
      messages: [draft({ mayStillSend: false, refusalReason: "consent_withdrawn" })],
    });
    renderRoutes(routes);

    expect(await screen.findByText("Do not contact")).toBeInTheDocument();
    // No link at all — a refused draft must not be one tap from being sent.
    expect(screen.queryByRole("link", { name: /Open in/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mark as sent" })).toBeDisabled();
    // Kept rather than hidden: a draft that vanished would look like the CRM
    // lost it, and the rep would rewrite it by hand.
    expect(screen.getByText("Hi, about your enquiry.")).toBeInTheDocument();
    expect(screen.getByText(/drafted before they opted out/)).toBeInTheDocument();
  });

  it("discards a draft nobody is going to send", async () => {
    renderRoutes(routes);
    await screen.findByText("Hi, about your enquiry.");

    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(crm.discardOutboundMessage).toHaveBeenCalledWith({ id: "msg-1" }));
  });

  it("renders nothing when there is nothing waiting", async () => {
    crm.listOutboundMessages.mockResolvedValue({ messages: [] });
    renderRoutes(routes);

    // An empty queue is the normal state; a "no drafts" panel on every record
    // would be noise on every page.
    await waitFor(() => expect(screen.queryByText("Drafted for you")).not.toBeInTheDocument());
  });

  it("renders nothing without crm:message:read", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:message:read");
    renderRoutes(routes);

    await waitFor(() => expect(screen.queryByText("Drafted for you")).not.toBeInTheDocument());
    expect(crm.listOutboundMessages).not.toHaveBeenCalled();
  });

  it("hides the send controls from a reader who cannot claim a send", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:message:write");
    renderRoutes(routes);

    expect(await screen.findByText("Hi, about your enquiry.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mark as sent" })).not.toBeInTheDocument();
  });

  it("offers no send button without a connected mailbox", async () => {
    renderRoutes(routes);
    await screen.findByText("Hi, about your enquiry.");

    // Sending needs the user's own Google account. Showing a button that
    // fails is worse than not showing one — the rep has a working one-tap
    // path already.
    expect(screen.queryByRole("button", { name: "Send from my email" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mark as sent" })).toBeInTheDocument();
  });

  it("sends from the CRM when the user has connected a mailbox", async () => {
    crm.getGoogleConnection.mockResolvedValue({ connection: { status: "connected" } });
    renderRoutes(routes);

    const sendButton = await screen.findByRole("button", { name: "Send from my email" });
    fireEvent.click(sendButton);

    await waitFor(() => expect(crm.sendOutboundMessage).toHaveBeenCalledWith({ id: "msg-1" }));
    // Marking sent stays available beside it: a rep may have sent it another
    // way, and the two are different claims.
    expect(crm.markOutboundMessageSent).not.toHaveBeenCalled();
  });

  it("will not send a draft the contact opted out of", async () => {
    crm.getGoogleConnection.mockResolvedValue({ connection: { status: "connected" } });
    crm.listOutboundMessages.mockResolvedValue({
      messages: [draft({ mayStillSend: false, refusalReason: "consent_withdrawn" })],
    });
    renderRoutes(routes);

    const sendButton = await screen.findByRole("button", { name: "Send from my email" });
    expect(sendButton).toBeDisabled();
  });

  it("offers no send button for a channel the CRM cannot deliver", async () => {
    crm.getGoogleConnection.mockResolvedValue({ connection: { status: "connected" } });
    crm.listOutboundMessages.mockResolvedValue({
      messages: [draft({ channelKind: "whatsapp", address: "+441632960001" })],
    });
    renderRoutes(routes);
    await screen.findByText("Hi, about your enquiry.");

    // Only email has a provider here; WhatsApp stays one tap by hand.
    expect(screen.queryByRole("button", { name: "Send from my email" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open in WhatsApp/ })).toBeInTheDocument();
  });

  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes);
    await screen.findByText("Hi, about your enquiry.");
    await expectNoA11yViolations(container);
  });
});
