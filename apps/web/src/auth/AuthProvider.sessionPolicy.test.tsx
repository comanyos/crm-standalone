import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "./AuthProvider";
import { ToastProvider } from "../components/ui/Toast";
import { IDLE_TIMEOUT_MS } from "./session-policy";
import type { OidcConfig } from "@crm/auth-client";

const { renewMock, revokeMock, endSessionMock } = vi.hoisted(() => ({
  renewMock: vi.fn(),
  revokeMock: vi.fn(),
  endSessionMock: vi.fn(),
}));

vi.mock("@crm/auth-client", () => ({
  beginLogin: vi.fn(),
  completeCallback: vi.fn(),
  endServerSession: endSessionMock,
  isExpiringSoon: () => false,
  renewFromStoredRefreshToken: renewMock,
  revokeStoredRefreshToken: revokeMock,
}));

const config = {
  issuer: "https://auth.example.com",
  orgId: "org-1",
  clientId: "crm-web",
  redirectUri: "https://app.example.com/auth/callback",
  scope: "openid",
} as OidcConfig;

const session = {
  accessToken: "at",
  claims: { sub: "id-1", email: "admin@localhost" },
  expiresAt: Date.now() + 3_600_000,
};

function Harness() {
  const { status } = useAuth();
  return <span data-testid="status">{status}</span>;
}

function renderApp() {
  render(
    <ToastProvider>
      <AuthProvider config={config}>
        <Harness />
      </AuthProvider>
    </ToastProvider>,
  );
}

describe("session policy in the auth provider", () => {
  beforeEach(() => {
    renewMock.mockReset();
    revokeMock.mockReset().mockResolvedValue(undefined);
    endSessionMock.mockReset().mockResolvedValue(undefined);
    // Fake timers must be installed before the provider mounts, or the idle
    // timer it schedules on mount is a real one that advanceTimersByTime
    // cannot reach — and every assertion below would pass vacuously.
    // shouldAdvanceTime keeps promise resolution working so waitFor still
    // settles.
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => vi.useRealTimers());

  it("signs the user out after the idle timeout and says why", async () => {
    renewMock.mockResolvedValue(session);
    renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));

    act(() => {
      vi.advanceTimersByTime(IDLE_TIMEOUT_MS);
    });

    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
    expect(screen.getByText(/signed out after a period of inactivity/i)).toBeInTheDocument();
  });

  // An idle timeout that leaves the refresh token in place is not a timeout:
  // reloading the page would restore the session the user was just told had
  // ended.
  it("revokes the stored refresh token when the session times out", async () => {
    renewMock.mockResolvedValue(session);
    renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));

    act(() => {
      vi.advanceTimersByTime(IDLE_TIMEOUT_MS);
    });

    expect(revokeMock).toHaveBeenCalled();
  });

  // The idle timeout must not end a session on this tab's behalf elsewhere:
  // an unattended background tab timing out cannot be allowed to sign the
  // user out of the tab they are actively working in.
  it("does not end the AuthinCore session on an idle timeout", async () => {
    renewMock.mockResolvedValue(session);
    renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));

    act(() => {
      vi.advanceTimersByTime(IDLE_TIMEOUT_MS);
    });

    expect(endSessionMock).not.toHaveBeenCalled();
  });

  it("does not time out a tab that was never signed in", async () => {
    renewMock.mockRejectedValue(new Error("no refresh token"));
    renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated"));

    act(() => {
      vi.advanceTimersByTime(IDLE_TIMEOUT_MS * 2);
    });

    // Still "unauthenticated" — flipping it to "signed-out" would show a
    // sign-out confirmation to somebody who never signed in.
    expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated");
  });

  it("signs this tab out when another tab signs out", async () => {
    renewMock.mockResolvedValue(session);
    renderApp();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));

    const otherTab = new BroadcastChannel("crm_auth_sign_out");
    await act(async () => {
      otherTab.postMessage({ type: "signed-out", at: Date.now() });
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    otherTab.close();

    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
  });
});
