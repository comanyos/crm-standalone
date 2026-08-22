import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RequireAuth } from "./RequireAuth";
import * as AuthProviderModule from "./AuthProvider";

vi.mock("./AuthProvider", async () => {
  const actual = await vi.importActual<typeof AuthProviderModule>("./AuthProvider");
  return { ...actual, useAuth: vi.fn() };
});

const useAuthMock = vi.mocked(AuthProviderModule.useAuth);

describe("RequireAuth", () => {
  it("shows a loading state without calling login", () => {
    const login = vi.fn();
    useAuthMock.mockReturnValue({ status: "loading", login } as unknown as ReturnType<
      typeof AuthProviderModule.useAuth
    >);
    render(
      <RequireAuth>
        <p>Protected content</p>
      </RequireAuth>,
    );
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(login).not.toHaveBeenCalled();
  });

  it("redirects (calls login) when unauthenticated, without rendering protected content", () => {
    const login = vi.fn();
    useAuthMock.mockReturnValue({ status: "unauthenticated", login } as unknown as ReturnType<
      typeof AuthProviderModule.useAuth
    >);
    render(
      <RequireAuth>
        <p>Protected content</p>
      </RequireAuth>,
    );
    expect(login).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
  });

  it("renders protected content once authenticated", () => {
    const login = vi.fn();
    useAuthMock.mockReturnValue({ status: "authenticated", login } as unknown as ReturnType<
      typeof AuthProviderModule.useAuth
    >);
    render(
      <RequireAuth>
        <p>Protected content</p>
      </RequireAuth>,
    );
    expect(screen.getByText("Protected content")).toBeInTheDocument();
    expect(login).not.toHaveBeenCalled();
  });
});
