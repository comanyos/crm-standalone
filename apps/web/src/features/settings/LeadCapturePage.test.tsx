import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import LeadCapturePage from "./LeadCapturePage";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listLeadCaptureEndpoints: vi.fn(),
    listLeadCaptureDeliveries: vi.fn(),
    createLeadCaptureEndpoint: vi.fn(),
    updateLeadCaptureEndpoint: vi.fn(),
    rotateLeadCaptureSecret: vi.fn(),
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

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/settings/lead-capture", element: <LeadCapturePage /> }];

const endpoint = {
  id: "ep-1",
  name: "Website form",
  token: "abc123",
  sourceKind: "generic",
  fieldMapping: { email: "contact_email" },
  defaultOwnerId: "owner-1",
  status: "active",
  createdBy: "user-1",
  lastError: "",
};

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listLeadCaptureEndpoints.mockResolvedValue({
    endpoints: [endpoint],
    baseUrl: "https://crm.example.com/integrations/lead-capture/",
  });
  crm.listLeadCaptureDeliveries.mockResolvedValue({ deliveries: [] });
  crm.createLeadCaptureEndpoint.mockResolvedValue({
    endpoint: { ...endpoint, id: "ep-2", token: "def456" },
    signingSecret: "s3cr3t",
    deliveryUrl: "https://crm.example.com/integrations/lead-capture/def456",
  });
  crm.updateLeadCaptureEndpoint.mockResolvedValue({ endpoint });
  crm.rotateLeadCaptureSecret.mockResolvedValue({ endpoint, signingSecret: "rotated" });
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/lead-capture"]);
  await screen.findByText("Website form");
  return view;
}

describe("LeadCapturePage", () => {
  it("lists endpoints with whether anything has arrived", async () => {
    await loaded();
    const table = screen.getByRole("table", { name: /Lead capture endpoints/i });
    expect(within(table).getByText("never")).toBeInTheDocument();
  });

  it("creates an endpoint with its field mapping", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New endpoint" }));
    await screen.findByRole("dialog");

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Facebook ads" } });
    fireEvent.click(screen.getByRole("button", { name: "Save endpoint" }));

    await waitFor(() => expect(crm.createLeadCaptureEndpoint).toHaveBeenCalled());
    expect(crm.createLeadCaptureEndpoint).toHaveBeenCalledWith({
      name: "Facebook ads",
      fieldMapping: { email: "contact_email" },
      defaultOwnerId: "",
    });
  });

  it("shows the secret once, and says it cannot be read back", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New endpoint" }));
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Facebook ads" } });
    fireEvent.click(screen.getByRole("button", { name: "Save endpoint" }));

    // There is no RPC that reads a secret back, so this dialog is the only
    // chance to copy it — and the copy has to say so, or somebody dismisses
    // it and has to rotate.
    expect(await screen.findByText(/shown once and cannot be read back/)).toBeInTheDocument();
    expect((screen.getByLabelText("Signing secret") as HTMLInputElement).value).toBe("s3cr3t");
    expect((screen.getByLabelText("Delivery URL") as HTMLInputElement).value).toContain("def456");
  });

  it("drops incomplete mapping rows rather than sending them", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New endpoint" }));
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Form" } });

    // A row the author added and has not filled in yet. The server refuses a
    // blank source field, so sending it would be an error about a row they
    // are still typing.
    fireEvent.click(screen.getByRole("button", { name: "Add a field" }));
    fireEvent.click(screen.getByRole("button", { name: "Save endpoint" }));

    await waitFor(() => expect(crm.createLeadCaptureEndpoint).toHaveBeenCalled());
    expect(crm.createLeadCaptureEndpoint.mock.calls[0][0].fieldMapping).toEqual({
      email: "contact_email",
    });
  });

  it("warns what a rotation breaks before doing it", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Rotate secret for Website form" }));

    // Rotating instantly breaks every configured caller. Saying so is the
    // difference between a considered action and a support ticket.
    expect(await screen.findByText(/stops working immediately/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Rotate secret" }));
    await waitFor(() => expect(crm.rotateLeadCaptureSecret).toHaveBeenCalledWith({ id: "ep-1" }));
    expect(await screen.findByText(/shown once and cannot be read back/)).toBeInTheDocument();
  });

  it("explains a rejected delivery in words rather than a code", async () => {
    crm.listLeadCaptureDeliveries.mockResolvedValue({
      deliveries: [
        {
          id: "d-1",
          endpointId: "ep-1",
          dedupKey: "body:abc",
          outcome: "rejected",
          reason: "bad_signature",
          rawPayload: '{"email":"alex@example.com"}',
          leadId: "",
        },
      ],
    });
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Delivery log for Website form" }));

    // The server sends bounded codes so nothing from a payload reaches a
    // metric label; turning them into guidance is this page's job.
    expect(await screen.findByText(/Check the signing secret in the calling system/)).toBeInTheDocument();
    // And the payload is there, because "the lead came in but the field is
    // missing" is unanswerable without it.
    expect(screen.getByText(/alex@example.com/)).toBeInTheDocument();
  });

  it("lets a reader see the log but not change anything", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:capture:write");
    await loaded();

    expect(screen.getByRole("button", { name: "Delivery log for Website form" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New endpoint" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Rotate secret for/ })).toBeNull();
  });

  it("explains an empty page to a user with no capture scope", async () => {
    auth.hasScope.mockReturnValue(false);
    renderRoutes(routes, ["/settings/lead-capture"]);
    expect(await screen.findByText("Access required")).toBeInTheDocument();
    expect(crm.listLeadCaptureEndpoints).not.toHaveBeenCalled();
  });

  it("has no accessibility violations, with the editor open", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);

    fireEvent.click(screen.getByRole("button", { name: "New endpoint" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });
});
