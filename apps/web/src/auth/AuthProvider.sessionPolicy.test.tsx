import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "./AuthProvider";
import { ToastProvider } from "../components/ui/Toast";
import { IDLE_TIMEOUT_MS } from "./session-policy";
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
  const { status, logoutError } = useAuth();
  return (
    <>
      <span data-testid="status">{status}</span>
      <span data-testid="logout-error">{logoutError}</span>
    </>
  );
}

function renderApp() {
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
  return queryClient;
}

describe("session policy in the auth provider", () => {
  beforeEach(() => {
    loadSessionMock.mockReset();
    loadAuthCapabilitiesMock.mockReset().mockResolvedValue({ google: false });
    endSessionMock.mockReset().mockResolvedValue(undefined);
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => vi.useRealTimers());

  it("revokes the server session after the idle timeout and says why", async () => {
    loadSessionMock.mockResolvedValue(session);
    const queryClient = renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });

    act(() => vi.advanceTimersByTime(IDLE_TIMEOUT_MS));
    await act(async () => Promise.resolve());

    expect(endSessionMock).toHaveBeenCalledWith(apiUrl);
    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();
    expect(screen.getByText(/signing you out after a period of inactivity/i)).toBeInTheDocument();
  });

  it("purges tenant queries when the absolute session expiry is reached", async () => {
    loadSessionMock.mockResolvedValue({ ...session, expiresAt: Date.now() + 1_000 });
    const queryClient = renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });

    act(() => vi.advanceTimersByTime(1_000));

    expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated");
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();
    expect(endSessionMock).not.toHaveBeenCalled();
  });

  it("does not claim idle sign-out succeeded when revocation fails", async () => {
    loadSessionMock.mockResolvedValue(session);
    endSessionMock.mockRejectedValue(new Error("network down"));
    const queryClient = renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });

    act(() => vi.advanceTimersByTime(IDLE_TIMEOUT_MS));
    await act(async () => Promise.resolve());

    expect(screen.getByTestId("status")).toHaveTextContent("sign-out-error");
    expect(screen.getByTestId("status")).not.toHaveTextContent(/^signed-out$/);
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();
  });

  it("does not time out a tab that was never signed in", async () => {
    loadSessionMock.mockResolvedValue(null);
    renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated"));

    act(() => vi.advanceTimersByTime(IDLE_TIMEOUT_MS * 2));

    expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated");
    expect(endSessionMock).not.toHaveBeenCalled();
  });

  it("signs this tab out when another tab signs out", async () => {
    loadSessionMock.mockResolvedValue(session);
    const queryClient = renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });

    const otherTab = new BroadcastChannel("crm_auth_sign_out");
    await act(async () => {
      otherTab.postMessage({ type: "signed-out", at: Date.now() });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    otherTab.close();

    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();
  });

  it("mirrors another tab's pending and failed logout without claiming success", async () => {
    loadSessionMock.mockResolvedValue(session);
    const queryClient = renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });

    const otherTab = new BroadcastChannel("crm_auth_sign_out");
    await act(async () => {
      otherTab.postMessage({ type: "signing-out", at: Date.now() });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(screen.getByTestId("status")).toHaveTextContent("signing-out");
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();

    await act(async () => {
      otherTab.postMessage({ type: "sign-out-error", at: Date.now() });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    otherTab.close();

    expect(screen.getByTestId("status")).toHaveTextContent("sign-out-error");
    expect(screen.getByTestId("logout-error")).toHaveTextContent(/may still be active/i);
  });
});
