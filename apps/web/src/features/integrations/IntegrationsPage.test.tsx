import { fireEvent, screen, waitFor } from "@testing-library/react";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { beforeEach, describe, expect, it, vi } from "vitest";
import IntegrationsPage from "./IntegrationsPage";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    getGoogleConnection: vi.fn(),
    startGoogleConnection: vi.fn(),
    disconnectGoogleConnection: vi.fn(),
    syncGoogleMailbox: vi.fn(),
    syncGoogleCalendar: vi.fn(),
  },
  auth: { hasScope: vi.fn(), session: { claims: { sub: "user-1" } } },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/settings/integrations", element: <IntegrationsPage /> }];

const REQUESTED_SCOPES = [
  "https://www.googleapis.com/auth/gmail.readonly",
  "https://www.googleapis.com/auth/gmail.send",
  "https://www.googleapis.com/auth/calendar.events",
];

function connectionResponse(overrides: Record<string, unknown> = {}) {
  return {
    configured: true,
    requestedScopes: REQUESTED_SCOPES,
    connection: {
      $typeName: "crm.v1.GoogleConnection",
      id: "",
      ownerId: "user-1",
      googleAccountEmail: "",
      status: "",
      grantedScopes: [],
      connectedBy: "",
      lastError: "",
      ...overrides,
    },
  };
}

function syncStatus(overrides: Record<string, unknown> = {}) {
  return {
    $typeName: "crm.v1.GmailSyncStatus",
    backfillComplete: true,
    messagesIndexed: 12n,
    activitiesCreated: 9n,
    lastError: "",
    lastSyncedAt: timestampFromDate(new Date("2026-08-02T08:00:00Z")),
    ...overrides,
  };
}

function connectedResponse() {
  return connectionResponse({
    id: "conn-1",
    googleAccountEmail: "person@example.com",
    status: "connected",
    grantedScopes: [
      "https://www.googleapis.com/auth/gmail.readonly",
      "https://www.googleapis.com/auth/calendar.events",
    ],
    connectedAt: timestampFromDate(new Date("2026-08-01T10:00:00Z")),
  });
}

function calendarStatus(overrides: Record<string, unknown> = {}) {
  return {
    $typeName: "crm.v1.CalendarSyncStatus",
    backfillComplete: true,
    eventsIndexed: 20n,
    activitiesCreated: 4n,
    lastError: "",
    lastSyncedAt: timestampFromDate(new Date("2026-08-02T08:00:00Z")),
    ...overrides,
  };
}

function connectedWithSync(
  sync: Record<string, unknown> = {},
  calendar: Record<string, unknown> = {},
) {
  return {
    ...connectedResponse(),
    gmailSync: syncStatus(sync),
    calendarSync: calendarStatus(calendar),
  };
}

// window.location.assign is how the page hands the browser to Google's
// consent screen. jsdom's implementation is not navigable, so it is replaced
// with a spy the tests can assert on.
const assign = vi.fn();

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.getGoogleConnection.mockResolvedValue(connectionResponse());
  crm.startGoogleConnection.mockResolvedValue({
    authorizationUrl: "https://accounts.google.com/o/oauth2/v2/auth?client_id=x&state=y",
  });
  crm.disconnectGoogleConnection.mockResolvedValue({ connection: connectionResponse().connection });
  crm.syncGoogleMailbox.mockResolvedValue({ messagesExamined: 40, entriesCreated: 3 });
  crm.syncGoogleCalendar.mockResolvedValue({ eventsExamined: 25, entriesCreated: 2 });
  Object.defineProperty(window, "location", {
    configurable: true,
    value: { ...window.location, assign },
  });
});

describe("IntegrationsPage", () => {
  it("offers to connect and states the permissions Google will be asked for", async () => {
    const { container } = renderRoutes(routes, ["/settings/integrations"]);

    expect(await screen.findByRole("button", { name: "Connect Google" })).toBeInTheDocument();
    // The permission list is rendered from the server's scope list, not from a
    // copy in the client that could drift from what is actually requested.
    expect(screen.getByText("Read your Gmail messages")).toBeInTheDocument();
    expect(screen.getByText("Send email on your behalf")).toBeInTheDocument();
    expect(screen.getByText("Read and write your calendar events")).toBeInTheDocument();
    await expectNoA11yViolations(container);
  });

  it("sends the browser to the consent URL the server issued", async () => {
    renderRoutes(routes, ["/settings/integrations"]);
    fireEvent.click(await screen.findByRole("button", { name: "Connect Google" }));

    await waitFor(() => expect(crm.startGoogleConnection).toHaveBeenCalledTimes(1));
    // The return path is sent to the server, which stores it with the pending
    // authorization — it does not travel through Google and back.
    expect(crm.startGoogleConnection).toHaveBeenCalledWith({ returnPath: "/settings/integrations" });
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://accounts.google.com/o/oauth2/v2/auth?client_id=x&state=y",
      ),
    );
  });

  it("shows the connected account and the permissions actually granted", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedResponse());
    const { container } = renderRoutes(routes, ["/settings/integrations"]);

    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    expect(screen.getByText("Connected")).toBeInTheDocument();
    // Granted, not requested: this account consented to two of the three.
    expect(screen.getByText("Read your Gmail messages")).toBeInTheDocument();
    expect(screen.queryByText("Send email on your behalf")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeInTheDocument();
    await expectNoA11yViolations(container);
  });

  it("confirms before disconnecting, and says what disconnecting does", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedResponse());
    renderRoutes(routes, ["/settings/integrations"]);

    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
    // A dialog rather than window.confirm: a browser told to suppress this
    // page's dialogs makes confirm() return false without prompting, so the
    // button would silently stop working.
    expect(
      await screen.findByText(/revoked at Google straight away/i),
    ).toBeInTheDocument();
    expect(crm.disconnectGoogleConnection).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Disconnect Google" }));
    await waitFor(() => expect(crm.disconnectGoogleConnection).toHaveBeenCalledWith({ purge: false }));
  });

  it("asks the user to reconnect when Google stopped honouring the credential", async () => {
    crm.getGoogleConnection.mockResolvedValue(
      connectionResponse({ id: "conn-1", status: "reauthorization_required", lastError: "refresh_rejected" }),
    );
    renderRoutes(routes, ["/settings/integrations"]);

    expect(await screen.findByText("Reconnection needed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reconnect Google" })).toBeInTheDocument();
    // The stored reason code is an operator-facing value; it must not be
    // printed at the user, who can do nothing with "refresh_rejected".
    expect(screen.queryByText(/refresh_rejected/)).not.toBeInTheDocument();
  });

  it("reports a callback failure using the reason the server sent, then clears it", async () => {
    const { router } = renderRoutes(routes, [
      "/settings/integrations?google=error&google_reason=access_denied",
    ]);

    expect(await screen.findByText(/You declined the permissions Google asked for/i)).toBeInTheDocument();
    // The outcome is announced once; a reload must not re-announce it.
    await waitFor(() => expect(router.state.location.search).toBe(""));
  });

  it("does not render an unrecognized callback reason", async () => {
    renderRoutes(routes, [
      "/settings/integrations?google=error&google_reason=%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E",
    ]);

    expect(await screen.findByText(/Google was not connected. Please try again./i)).toBeInTheDocument();
    expect(screen.queryByText(/onerror/)).not.toBeInTheDocument();
  });

  it("announces a successful connection and shows the new state", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedResponse());
    const { router } = renderRoutes(routes, ["/settings/integrations?google=connected"]);

    expect(await screen.findByText("Google account connected.")).toBeInTheDocument();
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    // Reported once: a reload must not re-announce a connection made minutes ago.
    await waitFor(() => expect(router.state.location.search).toBe(""));
  });

  it("explains when the deployment has no Google credentials rather than showing a dead button", async () => {
    crm.getGoogleConnection.mockResolvedValue({
      configured: false,
      requestedScopes: [],
      connection: connectionResponse().connection,
    });
    renderRoutes(routes, ["/settings/integrations"]);

    expect(await screen.findByText(/Google is not configured for this CRM/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Connect Google/ })).not.toBeInTheDocument();
  });

  it("explains that only email involving contacts is read into the CRM", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync());
    const { container } = renderRoutes(routes, ["/settings/integrations"]);

    // The privacy boundary is the thing a user most needs stated plainly, so
    // it is on the page rather than buried in documentation.
    expect(
      await screen.findByText(/Email with no matching contact is never read into the CRM/i),
    ).toBeInTheDocument();
    expect(screen.getByText("9")).toBeInTheDocument();
    await expectNoA11yViolations(container);
  });

  it("checks for new email on demand and reports what was added", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync());
    renderRoutes(routes, ["/settings/integrations"]);

    fireEvent.click(await screen.findByRole("button", { name: "Check for new email" }));
    await waitFor(() => expect(crm.syncGoogleMailbox).toHaveBeenCalledWith({}));
    // The number that means something is entries added to records, not
    // messages Gmail was asked about.
    expect(await screen.findByText(/Added 3 emails to your contacts' timelines/i)).toBeInTheDocument();
  });

  it("says so plainly when a pass found nothing", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync());
    crm.syncGoogleMailbox.mockResolvedValue({ messagesExamined: 40, entriesCreated: 0 });
    renderRoutes(routes, ["/settings/integrations"]);

    fireEvent.click(await screen.findByRole("button", { name: "Check for new email" }));
    expect(
      await screen.findByText(/No new email involving your contacts/i),
    ).toBeInTheDocument();
  });

  it("reports that a backfill is still in progress", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync({ backfillComplete: false }));
    renderRoutes(routes, ["/settings/integrations"]);

    expect(await screen.findByText(/Still working through your recent mail/i)).toBeInTheDocument();
  });

  it("translates a sync failure reason and never prints an unknown one", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync({ lastError: "token_unavailable" }));
    const { unmount } = renderRoutes(routes, ["/settings/integrations"]);
    expect(await screen.findByText(/Google would not renew the CRM's access/i)).toBeInTheDocument();
    unmount();

    crm.getGoogleConnection.mockResolvedValue(
      connectedWithSync({ lastError: "<img src=x onerror=alert(1)>" }),
    );
    renderRoutes(routes, ["/settings/integrations"]);
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    expect(screen.queryByText(/onerror/)).not.toBeInTheDocument();
  });

  it("does not offer sync before the account is connected", async () => {
    renderRoutes(routes, ["/settings/integrations"]);
    await screen.findByRole("button", { name: "Connect Google" });
    expect(screen.queryByRole("button", { name: "Check for new email" })).not.toBeInTheDocument();
  });

  it("states the calendar privacy boundary and reports progress", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync());
    const { container } = renderRoutes(routes, ["/settings/integrations"]);

    expect(
      await screen.findByText(/Meetings with no matching contact are never read into the CRM/i),
    ).toBeInTheDocument();
    // And it says what sync does when a meeting changes, which is the part
    // that distinguishes calendar sync from email sync.
    expect(screen.getByText(/updated when one is moved or cancelled/i)).toBeInTheDocument();
    expect(screen.getByText("4")).toBeInTheDocument();
    await expectNoA11yViolations(container);
  });

  it("checks for meeting changes on demand and reports what was recorded", async () => {
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync());
    renderRoutes(routes, ["/settings/integrations"]);

    fireEvent.click(await screen.findByRole("button", { name: "Check for meeting changes" }));
    await waitFor(() => expect(crm.syncGoogleCalendar).toHaveBeenCalledWith({}));
    expect(
      await screen.findByText(/Recorded 2 meeting updates on your contacts' timelines/i),
    ).toBeInTheDocument();
  });

  it("translates a calendar sync failure and never prints an unknown one", async () => {
    crm.getGoogleConnection.mockResolvedValue(
      connectedWithSync({}, { lastError: "sync_token_expired" }),
    );
    const { unmount } = renderRoutes(routes, ["/settings/integrations"]);
    expect(await screen.findByText(/Google reset the cursor/i)).toBeInTheDocument();
    unmount();

    crm.getGoogleConnection.mockResolvedValue(
      connectedWithSync({}, { lastError: "<script>alert(1)</script>" }),
    );
    renderRoutes(routes, ["/settings/integrations"]);
    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    expect(screen.queryByText(/alert\(1\)/)).not.toBeInTheDocument();
  });

  it("does not offer calendar sync before the account is connected", async () => {
    renderRoutes(routes, ["/settings/integrations"]);
    await screen.findByRole("button", { name: "Connect Google" });
    expect(
      screen.queryByRole("button", { name: "Check for meeting changes" }),
    ).not.toBeInTheDocument();
  });

  it("hides the controls from a user who may look but not change", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope === "crm:integration:read");
    crm.getGoogleConnection.mockResolvedValue(connectedWithSync());
    renderRoutes(routes, ["/settings/integrations"]);

    expect(await screen.findByText("person@example.com")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Check for new email" })).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Check for meeting changes" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/status but not change it/i)).toBeInTheDocument();
  });

  it("does not query at all without read permission", async () => {
    auth.hasScope.mockReturnValue(false);
    renderRoutes(routes, ["/settings/integrations"]);

    expect(
      await screen.findByText("You don't have access to connected accounts"),
    ).toBeInTheDocument();
    expect(crm.getGoogleConnection).not.toHaveBeenCalled();
  });
});
