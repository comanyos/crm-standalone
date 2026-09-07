import { afterEach, describe, expect, it, vi } from "vitest";
import { registerServiceWorker } from "./registerServiceWorker";

/** The only thing worth testing here is that it never breaks the app. A
 * service worker is a pure enhancement: every failure path has to be silent
 * and non-throwing, or a browser that dislikes it takes the CRM down with
 * it. */

const originalSW = Object.getOwnPropertyDescriptor(navigator, "serviceWorker");

afterEach(() => {
  vi.unstubAllEnvs();
  if (originalSW) Object.defineProperty(navigator, "serviceWorker", originalSW);
  else delete (navigator as unknown as Record<string, unknown>).serviceWorker;
});

function stubServiceWorker(register: () => Promise<unknown>) {
  Object.defineProperty(navigator, "serviceWorker", {
    value: { register },
    configurable: true,
  });
}

describe("registerServiceWorker", () => {
  it("does nothing in development, so a cached shell cannot mask the dev server", () => {
    vi.stubEnv("PROD", false);
    const register = vi.fn().mockResolvedValue({});
    stubServiceWorker(register);

    registerServiceWorker();
    expect(register).not.toHaveBeenCalled();
  });

  it("registers immediately rather than waiting for window.load", () => {
    // The app redirects an unauthenticated visitor to the identity provider
    // as soon as React mounts, and a first visit is unauthenticated by
    // definition — so waiting for `load` races that redirect, and losing
    // means the first visit installs nothing.
    vi.stubEnv("PROD", true);
    const register = vi.fn().mockResolvedValue({});
    stubServiceWorker(register);

    registerServiceWorker();
    expect(register).toHaveBeenCalledWith("/sw.js");
  });

  it("swallows a rejected registration", async () => {
    vi.stubEnv("PROD", true);
    const register = vi.fn().mockRejectedValue(new Error("blocked by policy"));
    stubServiceWorker(register);

    expect(() => registerServiceWorker()).not.toThrow();
    // An unhandled rejection here would surface as a console error on every
    // load in any browser that refuses workers.
    await Promise.resolve();
    await Promise.resolve();
  });

  it("does nothing where the API is absent", () => {
    vi.stubEnv("PROD", true);
    delete (navigator as unknown as Record<string, unknown>).serviceWorker;
    expect(() => registerServiceWorker()).not.toThrow();
  });
});
