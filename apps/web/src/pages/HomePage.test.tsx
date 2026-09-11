import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderRoutes } from "../test/harness";
import HomePage from "./HomePage";

const auth = vi.hoisted(() => ({ hasRole: vi.fn() }));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));
vi.mock("./DashboardPage", () => ({ default: () => <h1>Workspace dashboard</h1> }));

describe("HomePage", () => {
  beforeEach(() => auth.hasRole.mockReset());

  it("lands platform administrators in administration", async () => {
    auth.hasRole.mockImplementation((role: string) => role === "platform_admin");
    const { router } = renderRoutes(
      [
        { path: "/", element: <HomePage /> },
        { path: "/admin", element: <h1>Platform administration</h1> },
      ],
      ["/"],
    );

    expect(await screen.findByRole("heading", { name: "Platform administration" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin");
  });

  it("keeps an ordinary CRM user on the workspace dashboard", async () => {
    auth.hasRole.mockReturnValue(false);
    renderRoutes([{ path: "/", element: <HomePage /> }], ["/"]);

    expect(await screen.findByRole("heading", { name: "Workspace dashboard" })).toBeInTheDocument();
  });
});
