import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ContactChannelsPanel } from "./ContactChannelsPanel";
import { renderRoutes } from "../test/harness";
import { expectNoA11yViolations } from "../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listContactChannels: vi.fn(),
    recordMessagingConsent: vi.fn(),
    createActivity: vi.fn(),
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

const routes = [{ path: "/", element: <ContactChannelsPanel contactId="c-1" firstName="Bob" /> }];

/** The shape ListContactChannels returns: an answer, not ingredients. */
function channel(over: Partial<Record<string, unknown>> = {}) {
  return {
    kind: "whatsapp",
    address: "+441632960001",
    reachable: true,
    unreachableReason: "",
    mayMessage: true,
    refusalReason: "",
    consentState: "",
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listContactChannels.mockResolvedValue({
    channels: [
      channel({ kind: "email", address: "bob@example.com" }),
      channel({ kind: "sms" }),
      channel({ kind: "voice" }),
      channel({ kind: "whatsapp" }),
    ],
  });
  crm.recordMessagingConsent.mockResolvedValue({ consent: { id: "cons-1", state: "withdrawn" } });
  crm.createActivity.mockResolvedValue({ activity: { id: "act-1" } });
});

describe("ContactChannelsPanel", () => {
  it("offers a real link per channel, addressed the way each app expects", async () => {
    renderRoutes(routes);

    const whatsapp = (await screen.findByRole("link", { name: /WhatsApp/ })) as HTMLAnchorElement;
    // Bare digits, no "+": wa.me resolves a "+" to a different number or
    // rejects it, and it fails at the recipient's end rather than ours.
    expect(whatsapp.href).toContain("https://wa.me/441632960001");
    expect(whatsapp.href).toContain(encodeURIComponent("Hi Bob,"));

    const call = screen.getByRole("link", { name: /Call/ }) as HTMLAnchorElement;
    // The "+" stays here — a dialer needs it to reach an international number.
    expect(call.href).toBe("tel:+441632960001");
    // And a dialer has nowhere to put a message, so none is attached.
    expect(call.href).not.toContain("Hi%20Bob");
  });

  it("puts the edited message into the link, not the default", async () => {
    renderRoutes(routes);
    await screen.findByRole("link", { name: /WhatsApp/ });

    fireEvent.change(screen.getByLabelText("Message"), {
      target: { value: "Following up on the quote" },
    });

    const whatsapp = screen.getByRole("link", { name: /WhatsApp/ }) as HTMLAnchorElement;
    expect(decodeURIComponent(whatsapp.href.split("?text=")[1])).toBe("Following up on the quote");
  });

  it("renders the server's refusal instead of a link it could have built anyway", async () => {
    // The address is present and perfectly well-formed. Only the server knows
    // it must not be used, and a client that decided for itself would send.
    crm.listContactChannels.mockResolvedValue({
      channels: [
        channel({
          kind: "email",
          address: "bob@example.com",
          mayMessage: false,
          refusalReason: "consent_withdrawn",
          consentState: "withdrawn",
        }),
      ],
    });
    renderRoutes(routes);

    expect(await screen.findByText("Do not contact")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Email/ })).not.toBeInTheDocument();
  });

  it("says why a number is unusable rather than hiding the channel", async () => {
    crm.listContactChannels.mockResolvedValue({
      channels: [
        channel({
          kind: "whatsapp",
          address: "5550100000",
          reachable: false,
          unreachableReason: "needs a country code (start the number with +)",
          mayMessage: false,
          refusalReason: "unreachable_address",
        }),
      ],
    });
    renderRoutes(routes);

    // The user can fix this — but only if they are told what is wrong.
    expect(await screen.findByText(/needs a country code/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /WhatsApp/ })).not.toBeInTheDocument();
  });

  it("logs the hand-off as an attempt, never as a sent message", async () => {
    renderRoutes(routes);
    const whatsapp = await screen.findByRole("link", { name: /WhatsApp/ });

    fireEvent.click(whatsapp);

    await waitFor(() => expect(crm.createActivity).toHaveBeenCalled());
    const activity = crm.createActivity.mock.calls[0][0].activity;
    expect(activity.kind).toBe("outreach_attempt");
    expect(activity.subjectType).toBe("contact");
    expect(activity.subjectId).toBe("c-1");
    // We know what we handed to the operating system, not that the user
    // pressed send. The timeline must not claim otherwise.
    expect(activity.notes).toContain("opened");
    expect(activity.notes).not.toContain("sent");
  });

  it("records an opt-out against the channel that was refused", async () => {
    renderRoutes(routes);
    await screen.findByRole("link", { name: /WhatsApp/ });

    fireEvent.click(screen.getByRole("button", { name: /Opt out of SMS/ }));

    await waitFor(() => expect(crm.recordMessagingConsent).toHaveBeenCalled());
    expect(crm.recordMessagingConsent).toHaveBeenCalledWith({
      channelKind: "sms",
      address: "+441632960001",
      state: "withdrawn",
      source: "manual",
    });
  });

  it("hides consent controls from a reader who cannot record one", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:consent:write");
    renderRoutes(routes);
    await screen.findByRole("link", { name: /WhatsApp/ });

    expect(screen.queryByRole("button", { name: /Opt out/ })).not.toBeInTheDocument();
  });

  it("renders nothing at all without crm:consent:read", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:consent:read");
    renderRoutes(routes);

    await waitFor(() => expect(screen.queryByText("Reach out")).not.toBeInTheDocument());
    expect(crm.listContactChannels).not.toHaveBeenCalled();
  });

  it("says so when a contact has no address rather than showing an empty box", async () => {
    crm.listContactChannels.mockResolvedValue({ channels: [] });
    renderRoutes(routes);

    expect(
      await screen.findByText("No email address or phone number on this contact yet."),
    ).toBeInTheDocument();
  });

  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes);
    await screen.findByRole("link", { name: /WhatsApp/ });
    await expectNoA11yViolations(container);
  });
});
