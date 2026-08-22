import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  beginLogin,
  completeCallback,
  endServerSession,
  parseCallback,
  renewFromStoredRefreshToken,
  revokeStoredRefreshToken,
} from "../src/client.js";
import { loadRefreshToken, storeRefreshToken } from "../src/session-store.js";
import { OidcError } from "../src/types.js";
import type { OidcConfig } from "../src/types.js";

const config: OidcConfig = {
  issuer: "https://auth.example.com",
  orgId: "org-1",
  clientId: "crm-web",
  redirectUri: "https://app.example.com/auth/callback",
  scope: "openid email crm:account:read",
};

function fakeJwt(payload: Record<string, unknown>): string {
  const b64url = (obj: unknown) => {
    const bytes = new TextEncoder().encode(JSON.stringify(obj));
    let binary = "";
    for (const b of bytes) binary += String.fromCharCode(b);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };
  return `${b64url({ alg: "RS256" })}.${b64url(payload)}.sig`;
}

const validClaims = { sub: "id-1", org_id: "org-1", exp: Math.floor(Date.now() / 1000) + 3600, iat: 0 };

beforeEach(() => {
  sessionStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("parseCallback", () => {
  it("extracts code and state", () => {
    expect(parseCallback("?code=abc&state=xyz")).toEqual({
      code: "abc",
      state: "xyz",
      error: undefined,
      errorDescription: undefined,
    });
  });

  it("extracts an error response", () => {
    expect(parseCallback("?error=access_denied&error_description=user+said+no")).toMatchObject({
      error: "access_denied",
      errorDescription: "user said no",
    });
  });
});

describe("beginLogin", () => {
  it("stores a pending request and navigates to the authorize endpoint with PKCE params", async () => {
    const assign = vi.fn();
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: Object.assign(Object.create(Object.getPrototypeOf(originalLocation)), originalLocation, { assign }),
    });

    await beginLogin(config, "/accounts");

    expect(assign).toHaveBeenCalledTimes(1);
    const url = new URL(assign.mock.calls[0][0] as string);
    expect(url.origin).toBe(config.issuer);
    expect(url.pathname).toBe("/oauth2/authorize");
    expect(url.searchParams.get("response_type")).toBe("code");
    expect(url.searchParams.get("client_id")).toBe(config.clientId);
    expect(url.searchParams.get("redirect_uri")).toBe(config.redirectUri);
    expect(url.searchParams.get("org_id")).toBe(config.orgId);
    expect(url.searchParams.get("code_challenge_method")).toBe("S256");
    expect(url.searchParams.get("code_challenge")).toBeTruthy();
    const state = url.searchParams.get("state");
    expect(state).toBeTruthy();
    expect(sessionStorage.getItem(`crm_auth_pending:${state}`)).toBeTruthy();

    Object.defineProperty(window, "location", { configurable: true, value: originalLocation });
  });
});

describe("completeCallback", () => {
  it("rejects a denied/errored callback without touching the network", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      completeCallback(config, { error: "access_denied", errorDescription: "user declined" }),
    ).rejects.toThrow(OidcError);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects a callback whose state was never issued (forged or replayed)", async () => {
    await expect(completeCallback(config, { code: "abc", state: "never-issued" })).rejects.toThrow(
      /no matching login attempt/,
    );
  });

  it("rejects an expired pending request", async () => {
    sessionStorage.setItem(
      "crm_auth_pending:stale",
      JSON.stringify({ codeVerifier: "v", redirectAfter: "/", createdAt: Date.now() - 11 * 60 * 1000 }),
    );
    await expect(completeCallback(config, { code: "abc", state: "stale" })).rejects.toThrow(
      /no matching login attempt/,
    );
  });

  it("exchanges the code for tokens using the stored verifier and the correct request shape", async () => {
    sessionStorage.setItem(
      "crm_auth_pending:s1",
      JSON.stringify({ codeVerifier: "verifier-123", redirectAfter: "/accounts", createdAt: Date.now() }),
    );
    const accessToken = fakeJwt(validClaims);
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          access_token: accessToken,
          token_type: "Bearer",
          expires_in: 3600,
          refresh_token: "refresh-abc",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { session, redirectAfter } = await completeCallback(config, { code: "auth-code", state: "s1" });

    expect(redirectAfter).toBe("/accounts");
    expect(session.accessToken).toBe(accessToken);
    expect(session.claims.sub).toBe("id-1");
    // the refresh token from the response must be persisted so a later
    // reload can renew without a full redirect.
    expect(loadRefreshToken()).toBe("refresh-abc");

    const [url, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    expect(url.toString()).toBe("https://auth.example.com/oauth2/token");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["X-Org-ID"]).toBe("org-1");
    const body = new URLSearchParams(init.body as string);
    expect(body.get("grant_type")).toBe("authorization_code");
    expect(body.get("code")).toBe("auth-code");
    expect(body.get("code_verifier")).toBe("verifier-123");
    expect(body.get("redirect_uri")).toBe(config.redirectUri);

    // the pending entry must be consumed — a second exchange of the same
    // state (e.g. a replayed callback) must fail rather than resend tokens.
    await expect(completeCallback(config, { code: "auth-code", state: "s1" })).rejects.toThrow(
      /no matching login attempt/,
    );
  });

  it("surfaces the authorization server's error on a failed exchange", async () => {
    sessionStorage.setItem(
      "crm_auth_pending:s2",
      JSON.stringify({ codeVerifier: "v", redirectAfter: "/", createdAt: Date.now() }),
    );
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "invalid_grant", error_description: "code already used" }), {
          status: 400,
        }),
      ),
    );
    await expect(completeCallback(config, { code: "c", state: "s2" })).rejects.toThrow(/code already used/);
  });
});

describe("renewFromStoredRefreshToken", () => {
  it("rejects immediately when no refresh token is stored, without touching the network", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(renewFromStoredRefreshToken(config)).rejects.toThrow(/no stored refresh token/);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("exchanges the stored refresh token and rotates it on success", async () => {
    storeRefreshToken("refresh-old");
    const accessToken = fakeJwt(validClaims);
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          access_token: accessToken,
          token_type: "Bearer",
          expires_in: 3600,
          refresh_token: "refresh-new",
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const session = await renewFromStoredRefreshToken(config);

    expect(session.accessToken).toBe(accessToken);
    expect(loadRefreshToken()).toBe("refresh-new");

    const [, init] = fetchMock.mock.calls[0] as [URL, RequestInit];
    const body = new URLSearchParams(init.body as string);
    expect(body.get("grant_type")).toBe("refresh_token");
    expect(body.get("refresh_token")).toBe("refresh-old");
  });

  it("clears the stored token when the refresh is rejected (revoked/expired)", async () => {
    storeRefreshToken("refresh-dead");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "invalid_grant", error_description: "already used" }), {
          status: 400,
        }),
      ),
    );
    await expect(renewFromStoredRefreshToken(config)).rejects.toThrow(/already used/);
    expect(loadRefreshToken()).toBeNull();
  });

  it("de-dupes concurrent calls into a single exchange (React StrictMode double-invokes effects)", async () => {
    storeRefreshToken("refresh-old");
    const accessToken = fakeJwt(validClaims);
    const fetchMock = vi.fn().mockImplementation(
      () =>
        new Response(
          JSON.stringify({
            access_token: accessToken,
            token_type: "Bearer",
            expires_in: 3600,
            refresh_token: "refresh-new",
          }),
          { status: 200 },
        ),
    );
    vi.stubGlobal("fetch", fetchMock);

    // Two calls fired back-to-back, before either has resolved — exactly
    // what StrictMode's double effect invocation does.
    const [a, b] = await Promise.all([renewFromStoredRefreshToken(config), renewFromStoredRefreshToken(config)]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(a).toBe(b);

    // A later, non-overlapping call starts a fresh exchange.
    await renewFromStoredRefreshToken(config);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});

describe("revokeStoredRefreshToken / endServerSession", () => {
  it("swallow network failures instead of throwing", async () => {
    storeRefreshToken("rt");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network down")));
    await expect(revokeStoredRefreshToken(config)).resolves.toBeUndefined();
    await expect(endServerSession(config, "at")).resolves.toBeUndefined();
  });

  it("clears the stored token locally even though the network call is best-effort", async () => {
    storeRefreshToken("rt");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network down")));
    await revokeStoredRefreshToken(config);
    expect(loadRefreshToken()).toBeNull();
  });

  it("is a no-op when there is nothing stored", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await revokeStoredRefreshToken(config);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("sends both calls with keepalive so a closing tab cannot cancel them", async () => {
    storeRefreshToken("rt");
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await revokeStoredRefreshToken(config);
    await endServerSession(config, "at");

    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [, init] of fetchMock.mock.calls) {
      expect(init.keepalive).toBe(true);
    }
  });
});
