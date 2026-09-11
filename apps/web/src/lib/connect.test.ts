import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import { createTraceParent, credentialedFetch, unauthenticatedResponseInterceptor } from "./connect";
import { watchForUnauthenticatedResponses } from "../auth/unauthenticated-response";

describe("createTraceParent", () => {
  it("creates an opaque valid W3C traceparent", () => {
    expect(createTraceParent()).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
  });
});

describe("credentialedFetch", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("includes the HttpOnly session cookie on every Connect request", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await credentialedFetch("https://api.example.com/crm.v1.CRMService/GetSummary", {
      method: "POST",
    });

    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.com/crm.v1.CRMService/GetSummary",
      expect.objectContaining({ method: "POST", credentials: "include" }),
    );
  });

  it("reports a bare HTTP 401 as an invalid server session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 401 })));
    const listener = vi.fn();
    const stopWatching = watchForUnauthenticatedResponses(listener);

    try {
      await credentialedFetch("https://api.example.com/crm.v1.CRMService/GetSummary");
      expect(listener).toHaveBeenCalledTimes(1);
    } finally {
      stopWatching();
    }
  });
});

describe("unauthenticatedResponseInterceptor", () => {
  it("reports a decoded Connect unauthenticated error and preserves the error", async () => {
    const error = new ConnectError("session expired", Code.Unauthenticated);
    const next = vi.fn(async () => {
      throw error;
    }) as unknown as Parameters<Interceptor>[0];
    const intercepted = unauthenticatedResponseInterceptor()(next);
    const request = {} as Parameters<typeof intercepted>[0];
    const listener = vi.fn();
    const stopWatching = watchForUnauthenticatedResponses(listener);

    try {
      await expect(intercepted(request)).rejects.toBe(error);
      expect(listener).toHaveBeenCalledTimes(1);
    } finally {
      stopWatching();
    }
  });

  it("does not turn authorization failures into session expiry", async () => {
    const error = new ConnectError("not allowed", Code.PermissionDenied);
    const next = vi.fn(async () => {
      throw error;
    }) as unknown as Parameters<Interceptor>[0];
    const intercepted = unauthenticatedResponseInterceptor()(next);
    const request = {} as Parameters<typeof intercepted>[0];
    const listener = vi.fn();
    const stopWatching = watchForUnauthenticatedResponses(listener);

    try {
      await expect(intercepted(request)).rejects.toBe(error);
      expect(listener).not.toHaveBeenCalled();
    } finally {
      stopWatching();
    }
  });
});
