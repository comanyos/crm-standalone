import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IDLE_TIMEOUT_MS, broadcastSignOut, watchForIdleness, watchForSignOutElsewhere } from "./session-policy";

describe("watchForIdleness", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("fires once the timeout elapses with no activity", () => {
    const onIdle = vi.fn();
    watchForIdleness(onIdle, 1000);

    vi.advanceTimersByTime(999);
    expect(onIdle).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("restarts the countdown on deliberate interaction", () => {
    const onIdle = vi.fn();
    watchForIdleness(onIdle, 1000);

    vi.advanceTimersByTime(900);
    window.dispatchEvent(new Event("keydown"));
    vi.advanceTimersByTime(900);
    expect(onIdle).not.toHaveBeenCalled();

    vi.advanceTimersByTime(100);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  // The distinction that decides whether the timeout means anything. A
  // trackpad under a resting palm, or a page animating beneath the pointer,
  // would otherwise keep a session alive indefinitely.
  it("is not reset by incidental pointer movement", () => {
    const onIdle = vi.fn();
    watchForIdleness(onIdle, 1000);

    for (let i = 0; i < 20; i++) {
      vi.advanceTimersByTime(45);
      window.dispatchEvent(new Event("mousemove"));
    }
    vi.advanceTimersByTime(200);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("fires at most once, even if timers keep running", () => {
    const onIdle = vi.fn();
    watchForIdleness(onIdle, 1000);

    vi.advanceTimersByTime(5000);
    window.dispatchEvent(new Event("keydown"));
    vi.advanceTimersByTime(5000);

    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("stops watching once cancelled", () => {
    const onIdle = vi.fn();
    const stop = watchForIdleness(onIdle, 1000);

    stop();
    vi.advanceTimersByTime(10_000);
    window.dispatchEvent(new Event("keydown"));
    vi.advanceTimersByTime(10_000);

    expect(onIdle).not.toHaveBeenCalled();
  });

  it("defaults to a timeout long enough not to interrupt ordinary work", () => {
    // Guards against someone "tidying" the constant down to a value that
    // would sign users out mid-task.
    expect(IDLE_TIMEOUT_MS).toBeGreaterThanOrEqual(10 * 60 * 1000);
    expect(IDLE_TIMEOUT_MS).toBeLessThanOrEqual(60 * 60 * 1000);
  });
});

describe("cross-tab sign-out", () => {
  /** Stands in for another tab: a second BroadcastChannel on the same
   * channel name, which is what a second document would hold. */
  function otherTab(): BroadcastChannel {
    return new BroadcastChannel("crm_auth_sign_out");
  }

  /** BroadcastChannel delivery is asynchronous, so assertions wait a turn. */
  const delivered = () => new Promise((resolve) => setTimeout(resolve, 0));

  it("notifies when another tab signs out", async () => {
    const onSignOut = vi.fn();
    const stop = watchForSignOutElsewhere(onSignOut);

    const other = otherTab();
    other.postMessage({ type: "signed-out", at: Date.now() });
    await delivered();

    expect(onSignOut).toHaveBeenCalledTimes(1);
    other.close();
    stop();
  });

  // Anything on the origin can post to a named channel. The message carries
  // no authority, but an unrecognised shape should be ignored rather than
  // acted on.
  it("ignores messages it does not recognise", async () => {
    const onSignOut = vi.fn();
    const stop = watchForSignOutElsewhere(onSignOut);

    const other = otherTab();
    other.postMessage({ type: "something-else" });
    other.postMessage("signed-out");
    other.postMessage(null);
    await delivered();

    expect(onSignOut).not.toHaveBeenCalled();
    other.close();
    stop();
  });

  // A tab must not sign itself out when it is the one broadcasting.
  it("does not deliver the broadcast back to the sender", async () => {
    const onSignOut = vi.fn();
    const stop = watchForSignOutElsewhere(onSignOut);

    broadcastSignOut();
    await delivered();

    expect(onSignOut).not.toHaveBeenCalled();
    stop();
  });

  it("reaches a listening tab when broadcast", async () => {
    const heard = vi.fn();
    const other = otherTab();
    other.addEventListener("message", (event: MessageEvent) => {
      heard((event.data as { type?: string }).type);
    });

    broadcastSignOut();
    await delivered();

    expect(heard).toHaveBeenCalledWith("signed-out");
    other.close();
  });

  it("stops notifying once unsubscribed", async () => {
    const onSignOut = vi.fn();
    watchForSignOutElsewhere(onSignOut)();

    const other = otherTab();
    other.postMessage({ type: "signed-out", at: Date.now() });
    await delivered();

    expect(onSignOut).not.toHaveBeenCalled();
    other.close();
  });

  // This app keeps every token out of shared storage; the cross-tab signal
  // must not reintroduce one on a channel any same-origin script can join.
  it("broadcasts no session material", async () => {
    const seen: unknown[] = [];
    const other = otherTab();
    other.addEventListener("message", (event: MessageEvent) => seen.push(event.data));

    broadcastSignOut();
    await delivered();

    expect(seen).toHaveLength(1);
    expect(Object.keys(seen[0] as object).sort()).toEqual(["at", "type"]);
    other.close();
  });
});
