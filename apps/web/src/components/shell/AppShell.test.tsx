import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppShell } from "./AppShell";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { auth } = vi.hoisted(() => ({
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1", email: "ada@acme.example" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <p>Dashboard body</p> },
      { path: "accounts", element: <p>Accounts body</p> },
    ],
  },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
});

function nav() {
  return screen.getByRole("navigation", { name: "Primary" });
}

/** Below `lg` the sidebar is an off-canvas drawer.
 *
 * jsdom loads no stylesheet and computes no layout, so `toBeVisible()` cannot
 * see a Tailwind `hidden` class and these tests do not pretend otherwise.
 * They assert the drawer's behaviour — where focus goes, what dismisses it,
 * how many navigations exist — and, for the closed state, that the element
 * carries the class that hides it. Whether `display: none` then removes it
 * from the tab order and the accessibility tree is the browser's contract,
 * not this tier's; the visual half belongs to the browser-based tests the
 * roadmap still owes (Lighthouse/axe, visual regression). */
describe("AppShell navigation drawer", () => {
  it("keeps the navigation out of the tree until it is opened", async () => {
    renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");

    // `hidden`, not merely off-screen: a nav that is only translated away is
    // still tabbable and still read aloud, which is an invisible focus trap.
    expect(nav()).toHaveClass("hidden");
    expect(screen.getByRole("button", { name: "Open navigation" })).toBeInTheDocument();
  });

  it("opens on the menu button and moves focus into the drawer", async () => {
    renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");

    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));

    expect(nav()).not.toHaveClass("hidden");
    // Focus has to land inside, or the next Tab continues from a button that
    // is now behind the overlay.
    await waitFor(() => expect(nav()).toHaveFocus());
    expect(within(nav()).getByRole("link", { name: "Accounts" })).toBeInTheDocument();
  });

  it("closes on Escape", async () => {
    renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
    expect(nav()).not.toHaveClass("hidden");

    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(nav()).toHaveClass("hidden"));
  });

  it("closes on its own Close button", async () => {
    renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));

    fireEvent.click(screen.getByRole("button", { name: "Close navigation" }));
    await waitFor(() => expect(nav()).toHaveClass("hidden"));
  });

  // Following a link navigates the page behind the drawer. Without this the
  // reader arrives at the new page underneath an overlay they must dismiss.
  it("closes itself when a link navigates", async () => {
    renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));

    fireEvent.click(within(nav()).getByRole("link", { name: "Accounts" }));

    await screen.findByText("Accounts body");
    await waitFor(() => expect(nav()).toHaveClass("hidden"));
  });

  // The drawer is the one piece of this shell with two states, so both get
  // checked: an overlay is a common place to leave an unlabelled control or a
  // second landmark behind.
  it("has no accessibility violations in either state", async () => {
    const { container } = renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");
    await expectNoA11yViolations(container);

    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
    await waitFor(() => expect(nav()).not.toHaveClass("hidden"));
    await expectNoA11yViolations(container);
  });

  // One <nav>, shown or hidden — not a second copy for small screens. Two
  // would put every destination in the accessibility tree twice.
  it("has exactly one primary navigation", async () => {
    renderRoutes(routes, ["/"]);
    await screen.findByText("Dashboard body");
    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));

    expect(screen.getAllByRole("navigation", { name: "Primary" })).toHaveLength(1);
    expect(within(nav()).getAllByRole("link", { name: "Accounts" })).toHaveLength(1);
  });
});
