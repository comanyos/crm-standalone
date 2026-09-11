import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "./AuthProvider";
import { ToastProvider } from "../components/ui/Toast";
import { reportUnauthenticatedResponse } from "./unauthenticated-response";
import type { AuthSession } from "./api";

const { loadSessionMock, loadAuthCapabilitiesMock, endSessionMock } = vi.hoisted(() => ({
  loadSessionMock: vi.fn(),
  loadAuthCapabilitiesMock: vi.fn(),
  endSessionMock: vi.fn(),
}));

vi.mock("./api", async () => {
  const actual = await vi.importActual<typeof import("./api")>("./api");
  return {
    ...actual,
    loadSession: loadSessionMock,
    loadAuthCapabilities: loadAuthCapabilitiesMock,
    endSession: endSessionMock,
  };
});

const apiUrl = "https://api.example.com";
const session: AuthSession = {
  user: { id: "id-1", email: "admin@example.com" },
  organization: { id: "org-1", name: "Savvy" },
  membership: { role: "owner" },
  claims: {
    sub: "id-1",
    email: "admin@example.com",
    roles: ["owner"],
    scopes: ["crm:*"],
    scope: "crm:*",
  },
  expiresAt: Date.now() + 3_600_000,
};

function Harness() {
  const { status, logoutError, logout } = useAuth();
  return (
    <>
      <span data-testid="status">{status}</span>
      <span data-testid="logout-error">{logoutError}</span>
      <button type="button" onClick={() => void logout()}>
        Sign out
      </button>
    </>
  );
}

async function renderSignedIn() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <ToastProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider apiUrl={apiUrl} authPortalUrl="https://crm.example.com/?auth=login">
          <Harness />
        </AuthProvider>
      </QueryClientProvider>
    </ToastProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
  return queryClient;
}

describe("AuthProvider.logout", () => {
  beforeEach(() => {
    loadSessionMock.mockReset().mockResolvedValue(session);
    loadAuthCapabilitiesMock.mockReset().mockResolvedValue({ google: false });
    endSessionMock.mockReset().mockResolvedValue(undefined);
  });

  it("stays in signing-out until the cookie session has been revoked", async () => {
    let finishLogout: () => void = () => {};
    endSessionMock.mockReturnValue(
      new Promise<void>((resolve) => {
        finishLogout = resolve;
      }),
    );

    const queryClient = await renderSignedIn();
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });
    await act(async () => screen.getByRole("button", { name: "Sign out" }).click());

    expect(screen.getByTestId("status")).toHaveTextContent("signing-out");
    expect(endSessionMock).toHaveBeenCalledWith(apiUrl);
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();

    act(() => reportUnauthenticatedResponse());
    expect(screen.getByTestId("status")).toHaveTextContent("signing-out");

    await act(async () => finishLogout());
    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
  });

  it("shows a retryable error instead of claiming success when logout fails", async () => {
    endSessionMock.mockRejectedValue(new Error("network down"));

    const queryClient = await renderSignedIn();
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });
    await act(async () => screen.getByRole("button", { name: "Sign out" }).click());

    expect(screen.getByTestId("status")).toHaveTextContent("sign-out-error");
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();
    expect(screen.getByTestId("logout-error")).toHaveTextContent(/server session ended/i);
    expect(screen.getByTestId("status")).not.toHaveTextContent(/^signed-out$/);
  });

  it("shows a retryable error after the confirmation timeout", async () => {
    vi.useFakeTimers();
    try {
      endSessionMock.mockReturnValue(new Promise(() => {}));
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(
        <ToastProvider>
          <QueryClientProvider client={queryClient}>
            <AuthProvider apiUrl={apiUrl} authPortalUrl="https://crm.example.com/?auth=login">
              <Harness />
            </AuthProvider>
          </QueryClientProvider>
        </ToastProvider>,
      );
      await act(async () => Promise.resolve());
      await act(async () => screen.getByRole("button", { name: "Sign out" }).click());
      expect(screen.getByTestId("status")).toHaveTextContent("signing-out");

      await act(async () => vi.advanceTimersByTime(3_000));
      expect(screen.getByTestId("status")).toHaveTextContent("sign-out-error");
      expect(screen.getByTestId("logout-error")).toHaveTextContent(/may still be active/i);
    } finally {
      vi.useRealTimers();
    }
  });

  it("confirms sign-out after a failed attempt is retried", async () => {
    endSessionMock.mockRejectedValueOnce(new Error("temporary failure")).mockResolvedValueOnce(undefined);

    await renderSignedIn();
    await act(async () => screen.getByRole("button", { name: "Sign out" }).click());
    expect(screen.getByTestId("status")).toHaveTextContent("sign-out-error");

    await act(async () => screen.getByRole("button", { name: "Sign out" }).click());

    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
    expect(screen.getByTestId("logout-error")).toBeEmptyDOMElement();
    expect(endSessionMock).toHaveBeenCalledTimes(2);
  });
});
