import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderRoutes } from "../test/harness";
import SignedOutPage from "./SignedOutPage";

type Status =
  | "loading"
  | "authenticated"
  | "unauthenticated"
  | "signing-out"
  | "sign-out-error"
  | "signed-out";

const auth = vi.hoisted(() => ({
  status: "signed-out" as Status,
  logoutError: null as string | null,
  login: vi.fn(),
  logout: vi.fn(),
}));

vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

describe("SignedOutPage", () => {
  beforeEach(() => {
    auth.status = "signed-out";
    auth.logoutError = null;
    auth.login.mockReset();
    auth.logout.mockReset().mockResolvedValue(undefined);
  });

  it("claims signed-out only after a confirmed absent session", () => {
    renderRoutes([{ path: "/signed-out", element: <SignedOutPage /> }], ["/signed-out"]);
    expect(screen.getByRole("heading", { name: "You're signed out" })).toBeInTheDocument();
  });

  it("shows an explicit retry without claiming success after logout fails", () => {
    auth.status = "sign-out-error";
    auth.logoutError = "The server session may still be active.";
    renderRoutes([{ path: "/signed-out", element: <SignedOutPage /> }], ["/signed-out"]);

    expect(screen.getByRole("heading", { name: "Sign-out needs attention" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "You're signed out" })).not.toBeInTheDocument();
    expect(screen.getByText(/may still be active/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Retry sign out" }));
    expect(auth.logout).toHaveBeenCalledOnce();
  });

  it("does not claim success while confirmation is pending", () => {
    auth.status = "signing-out";
    renderRoutes([{ path: "/signed-out", element: <SignedOutPage /> }], ["/signed-out"]);
    expect(screen.getByRole("heading", { name: "Signing out…" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "You're signed out" })).not.toBeInTheDocument();
  });

  it("does not claim success if an authenticated user opens the route directly", () => {
    auth.status = "authenticated";
    renderRoutes([{ path: "/signed-out", element: <SignedOutPage /> }], ["/signed-out"]);
    expect(screen.getByRole("heading", { name: "You're still signed in" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "You're signed out" })).not.toBeInTheDocument();
  });
});
