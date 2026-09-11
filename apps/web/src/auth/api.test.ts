import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  endSession,
  googleLoginUrl,
  loadAuthCapabilities,
  loadSession,
  requestLoginCode,
  verifyLoginCode,
} from "./api";
import { watchForUnauthenticatedResponses } from "./unauthenticated-response";

const apiUrl = "https://api.example.com/";
const expiresAt = "2030-01-01T00:00:00Z";
const serverSession = {
  sessionToken: "must-not-be-kept-in-browser-state",
  user: { id: "user-1", email: "owner@savvy.example", firstName: "Ada", lastName: "Lovelace" },
  organization: { id: "org-1", name: "Savvy" },
  membership: { role: "owner" },
  roles: ["owner", "crm_admin"],
  scopes: ["crm:account:read", "crm:lead:write"],
  expiresAt,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("simple auth API", () => {
  beforeEach(() => vi.stubGlobal("fetch", vi.fn()));
  afterEach(() => vi.unstubAllGlobals());

  it("builds the canonical Google login endpoint", () => {
    expect(googleLoginUrl(apiUrl)).toBe("https://api.example.com/auth/google/start?flow=login");
  });

  it("enables Google only when the server explicitly advertises it", async () => {
    vi.mocked(fetch).mockResolvedValue(json({ google: true }));
    await expect(loadAuthCapabilities(apiUrl)).resolves.toEqual({ google: true });
    expect(fetch).toHaveBeenCalledWith(
      "https://api.example.com/auth/capabilities",
      expect.objectContaining({ credentials: "include", cache: "no-store" }),
    );
  });

  it("fails closed when capability discovery is missing or unreachable", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ error: "not found" }, 404));
    await expect(loadAuthCapabilities(apiUrl)).resolves.toEqual({ google: false });

    vi.mocked(fetch).mockRejectedValueOnce(new TypeError("network unavailable"));
    await expect(loadAuthCapabilities(apiUrl)).resolves.toEqual({ google: false });
  });

  it("loads the HttpOnly-cookie session and maps it to compatible claims", async () => {
    vi.mocked(fetch).mockResolvedValue(json(serverSession));

    const session = await loadSession(apiUrl);

    expect(fetch).toHaveBeenCalledWith(
      "https://api.example.com/auth/session",
      expect.objectContaining({ credentials: "include" }),
    );
    expect(session?.claims).toMatchObject({
      sub: "user-1",
      email: "owner@savvy.example",
      roles: ["owner", "crm_admin"],
      scopes: ["crm:account:read", "crm:lead:write"],
    });
    expect(session).not.toHaveProperty("sessionToken");
  });

  it("treats an unauthorized session check as signed out", async () => {
    vi.mocked(fetch).mockResolvedValue(json({ error: "unauthorized" }, 401));
    const listener = vi.fn();
    const stopWatching = watchForUnauthenticatedResponses(listener);

    try {
      await expect(loadSession(apiUrl)).resolves.toBeNull();
      expect(listener).toHaveBeenCalledTimes(1);
    } finally {
      stopWatching();
    }
  });

  it("requests a login challenge with the canonical flow contract", async () => {
    vi.mocked(fetch).mockResolvedValue(json({ challengeId: "challenge-1", expiresInSeconds: 600 }, 202));

    await expect(requestLoginCode(apiUrl, "owner@savvy.example")).resolves.toEqual({
      challengeId: "challenge-1",
      expiresInSeconds: 600,
    });
    expect(fetch).toHaveBeenCalledWith(
      "https://api.example.com/auth/email/request",
      expect.objectContaining({
        method: "POST",
        credentials: "include",
        body: JSON.stringify({ email: "owner@savvy.example", flow: "login" }),
      }),
    );
  });

  it("verifies the challenge without persisting the returned opaque token", async () => {
    vi.mocked(fetch).mockResolvedValue(json(serverSession));

    const session = await verifyLoginCode(apiUrl, "challenge-1", "123456");

    expect(fetch).toHaveBeenCalledWith(
      "https://api.example.com/auth/email/verify",
      expect.objectContaining({
        credentials: "include",
        body: JSON.stringify({ challengeId: "challenge-1", code: "123456" }),
      }),
    );
    expect(session).not.toHaveProperty("sessionToken");
  });

  it("uses the same cookie contract for logout", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 204 }));
    await endSession(apiUrl);
    expect(fetch).toHaveBeenCalledWith(
      "https://api.example.com/auth/logout",
      expect.objectContaining({ method: "POST", credentials: "include", keepalive: true }),
    );
  });
});
