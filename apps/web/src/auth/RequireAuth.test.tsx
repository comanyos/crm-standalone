import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
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

  it.each(["signing-out", "sign-out-error"] as const)(
    "hides protected content while logout is %s",
    (status) => {
      useAuthMock.mockReturnValue({ status, login: vi.fn() } as unknown as ReturnType<
        typeof AuthProviderModule.useAuth
      >);
      render(
        <MemoryRouter initialEntries={["/private"]}>
          <Routes>
            <Route
              path="/private"
              element={
                <RequireAuth>
                  <p>Protected content</p>
                </RequireAuth>
              }
            />
            <Route path="/signed-out" element={<p>Safe session screen</p>} />
          </Routes>
        </MemoryRouter>,
      );

      expect(screen.queryByText("Protected content")).not.toBeInTheDocument();
      expect(screen.getByText("Safe session screen")).toBeInTheDocument();
    },
  );
});
