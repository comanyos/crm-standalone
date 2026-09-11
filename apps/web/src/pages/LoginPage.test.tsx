import { screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AuthStatus } from "../auth/AuthProvider";
import { renderRoutes } from "../test/harness";
import LoginPage from "./LoginPage";

const auth = vi.hoisted(() => ({
  status: "unauthenticated" as AuthStatus,
  login: vi.fn(),
}));

vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

describe("LoginPage compatibility redirect", () => {
  beforeEach(() => {
    auth.status = "unauthenticated";
    auth.login.mockReset();
  });

  it("sends old /login bookmarks to the marketing auth dialog", async () => {
    renderRoutes([{ path: "/login", element: <LoginPage /> }], ["/login?returnTo=%2Faccounts"]);

    expect(screen.getByText("Opening secure sign in…")).toBeInTheDocument();
    await waitFor(() => expect(auth.login).toHaveBeenCalledWith("/accounts"));
  });

  it("does not forward an external return target", async () => {
    renderRoutes([{ path: "/login", element: <LoginPage /> }], ["/login?returnTo=https%3A%2F%2Fevil.example"]);

    await waitFor(() => expect(auth.login).toHaveBeenCalledWith("/"));
  });

  it("opens the requested app route when a session already exists", async () => {
    auth.status = "authenticated";
    renderRoutes(
      [
        { path: "/login", element: <LoginPage /> },
        { path: "/accounts", element: <h1>Accounts</h1> },
      ],
      ["/login?returnTo=%2Faccounts"],
    );

    expect(await screen.findByRole("heading", { name: "Accounts" })).toBeInTheDocument();
    expect(auth.login).not.toHaveBeenCalled();
  });
});
