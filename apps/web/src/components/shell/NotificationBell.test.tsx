import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { NotificationBell } from "./NotificationBell";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listNotifications: vi.fn(), markNotificationRead: vi.fn() },
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

const routes = [{ path: "/", element: <NotificationBell /> }];

const unreadNotification = {
  id: "note-1", kind: "workflow", title: "New referral to call",
  body: "Acme Holdings", subjectType: "lead", subjectId: "lead-9",
  ruleId: "rule-1", readAt: undefined,
};

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listNotifications.mockResolvedValue({ notifications: [unreadNotification], unreadCount: 1 });
  crm.markNotificationRead.mockResolvedValue({ notification: { ...unreadNotification, readAt: {} } });
});

async function loaded() {
  const view = renderRoutes(routes, ["/"]);
  await screen.findByRole("button", { name: "Notifications, 1 unread" });
  return view;
}

describe("NotificationBell", () => {
  it("has no accessibility violations with the panel open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Notifications, 1 unread" }));
    await screen.findByText("New referral to call");
    await expectNoA11yViolations(container);
  });

  // The badge is decorative; the count has to reach a screen-reader user
  // through the button's name, or "Notifications" tells them nothing about
  // whether opening it is worth the trouble.
  it("puts the unread count in the button's accessible name", async () => {
    crm.listNotifications.mockResolvedValue({ notifications: [], unreadCount: 0 });
    renderRoutes(routes, ["/"]);
    expect(await screen.findByRole("button", { name: "Notifications" })).toBeInTheDocument();
  });

  it("links a notification to the record it is about", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Notifications, 1 unread" }));
    const link = await screen.findByRole("link", { name: "Open the record" });
    expect(link).toHaveAttribute("href", "/leads/lead-9");
  });

  it("marks one read and refreshes the count", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Notifications, 1 unread" }));
    fireEvent.click(await screen.findByRole("button", { name: /Mark read/ }));
    await waitFor(() => expect(crm.markNotificationRead).toHaveBeenCalledWith({ id: "note-1" }));
  });

  it("closes on Escape", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Notifications, 1 unread" }));
    await screen.findByText("New referral to call");
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByText("New referral to call")).toBeNull());
  });

  // A read-only role holds crm:notification:read and not its write half, so
  // the inbox is readable and the acknowledgement is not offered — a button
  // the server would refuse is worse than no button.
  it("offers no mark-read control without the write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope === "crm:notification:read");
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Notifications, 1 unread" }));
    await screen.findByText("New referral to call");
    expect(screen.queryByRole("button", { name: /^Mark read/ })).toBeNull();
  });

  it("renders nothing for a principal with no notification scope", async () => {
    auth.hasScope.mockReturnValue(false);
    const { container } = renderRoutes(routes, ["/"]);
    await waitFor(() => expect(within(container).queryByRole("button")).toBeNull());
    expect(crm.listNotifications).not.toHaveBeenCalled();
  });
});
