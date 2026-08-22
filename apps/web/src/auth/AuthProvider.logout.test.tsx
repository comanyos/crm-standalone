import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "./AuthProvider";
import { ToastProvider } from "../components/ui/Toast";
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

/** Surfaces `status` and a click target, so the test observes exactly what a
 * user would: which screen the sign-out click leaves them looking at. */
function Harness() {
  const { status, logout } = useAuth();
  return (
    <>
      <span data-testid="status">{status}</span>
      <button type="button" onClick={() => void logout()}>
        Sign out
      </button>
    </>
  );
}

async function renderSignedIn() {
  render(
    <ToastProvider>
      <AuthProvider config={config}>
        <Harness />
      </AuthProvider>
    </ToastProvider>,
  );
  await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
}

describe("AuthProvider.logout", () => {
  beforeEach(() => {
    renewMock.mockResolvedValue(session);
  });

  it("stays in signing-out until the server session has actually ended", async () => {
    let endSession: () => void = () => {};
    revokeMock.mockResolvedValue(undefined);
    endSessionMock.mockReturnValue(
      new Promise<void>((resolve) => {
        endSession = resolve;
      }),
    );

    await renderSignedIn();
    await act(async () => {
      screen.getByRole("button", { name: "Sign out" }).click();
    });

    // The DELETE is still in flight: claiming "signed out" here is the bug
    // this waiting exists to prevent.
    expect(screen.getByTestId("status")).toHaveTextContent("signing-out");
    expect(endSessionMock).toHaveBeenCalledWith(config, "at");

    await act(async () => {
      endSession();
    });
    expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
  });

  it("gives up waiting after the grace period rather than stranding the user", async () => {
    vi.useFakeTimers();
    try {
      revokeMock.mockReturnValue(new Promise(() => {}));
      endSessionMock.mockReturnValue(new Promise(() => {}));

      render(
        <ToastProvider>
          <AuthProvider config={config}>
            <Harness />
          </AuthProvider>
        </ToastProvider>,
      );
      await act(async () => {
        await Promise.resolve();
      });
      await act(async () => {
        screen.getByRole("button", { name: "Sign out" }).click();
      });
      expect(screen.getByTestId("status")).toHaveTextContent("signing-out");

      await act(async () => {
        vi.advanceTimersByTime(3_000);
      });
      expect(screen.getByTestId("status")).toHaveTextContent("signed-out");
    } finally {
      vi.useRealTimers();
    }
  });
});
