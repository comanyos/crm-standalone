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

const session: AuthSession = {
  user: { id: "user-1", email: "owner@savvy.example" },
  organization: { id: "org-1", name: "Savvy" },
  membership: { role: "owner" },
  claims: {
    sub: "user-1",
    email: "owner@savvy.example",
    roles: ["owner"],
    scopes: ["crm:*"],
    scope: "crm:*",
  },
  expiresAt: Date.now() + 3_600_000,
};

function Harness() {
  const { status, session: activeSession } = useAuth();
  return (
    <>
      <span data-testid="status">{status}</span>
      <span data-testid="session">{activeSession?.user.id ?? "none"}</span>
    </>
  );
}

describe("AuthProvider server session rejection", () => {
  beforeEach(() => {
    loadSessionMock.mockReset().mockResolvedValue(session);
    loadAuthCapabilitiesMock.mockReset().mockResolvedValue({ google: false });
    endSessionMock.mockReset().mockResolvedValue(undefined);
  });

  it("purges CRM query data and returns to login state without claiming logout", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(["accounts", "org-1"], { name: "Private account" });

    render(
      <ToastProvider>
        <QueryClientProvider client={queryClient}>
          <AuthProvider apiUrl="https://api.example.com" authPortalUrl="https://crm.example.com/?auth=login">
            <Harness />
          </AuthProvider>
        </QueryClientProvider>
      </ToastProvider>,
    );
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));

    act(() => {
      reportUnauthenticatedResponse();
      reportUnauthenticatedResponse();
    });

    expect(screen.getByTestId("status")).toHaveTextContent("unauthenticated");
    expect(screen.getByTestId("status")).not.toHaveTextContent(/^signed-out$/);
    expect(screen.getByTestId("session")).toHaveTextContent("none");
    expect(queryClient.getQueryData(["accounts", "org-1"])).toBeUndefined();
    expect(endSessionMock).not.toHaveBeenCalled();
    expect(screen.getAllByText(/session is no longer active/i)).toHaveLength(1);
  });
});
