import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ACTIVITY_BROADCAST_THROTTLE_MS,
  IDLE_TIMEOUT_MS,
  broadcastSignOut,
  watchForIdleness,
  watchForSignOutElsewhere,
} from "./session-policy";

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
    expect(ACTIVITY_BROADCAST_THROTTLE_MS).toBeLessThan(IDLE_TIMEOUT_MS);
  });
});

describe("cross-tab sign-out", () => {
  /** Stands in for another tab: a second BroadcastChannel on the same
   * channel name, which is what a second document would hold. */
  function otherTab(): BroadcastChannel {
    return new BroadcastChannel(CHANNEL);
  }

  const CHANNEL = "crm_auth_sign_out";
  /** A type the module's listener does not recognise, so it is inert. */
  const SETTLED = "__test-settled__";

  /** Waits until everything `via` has posted so far has been delivered, by
   * posting a sentinel through **the same channel instance** and waiting for
   * it to arrive.
   *
   * The instance matters. BroadcastChannel guarantees ordering for messages
   * posted from one instance; between two instances it guarantees nothing, so
   * a sentinel sent down a second channel can overtake the message it was
   * meant to follow. Posting it through the same instance makes the sentinel
   * a real happens-before rather than a second guess.
   *
   * What this replaced was a single `setTimeout(0)`. Delivery takes exactly
   * one macrotask, so that had no margin: whenever the runtime scheduled the
   * test's timer ahead of the channel's dispatch, the assertion ran early —
   * failing that test and leaving the message in flight to arrive during the
   * next one, where a negative assertion was waiting. That is why these
   * failed in pairs, and only under the load of a full run. */
  async function settled(via: BroadcastChannel) {
    const probe = new BroadcastChannel(CHANNEL);
    try {
      await new Promise<void>((resolve) => {
        probe.addEventListener("message", (event: MessageEvent) => {
          if ((event.data as { type?: string } | null)?.type === SETTLED) resolve();
        });
        via.postMessage({ type: SETTLED });
      });
    } finally {
      probe.close();
    }
  }

  it("notifies when another tab signs out", async () => {
    const onSignOut = vi.fn();
    const stop = watchForSignOutElsewhere(onSignOut);

    const other = otherTab();
    other.postMessage({ type: "signed-out", at: Date.now() });
    await settled(other);

    expect(onSignOut).toHaveBeenCalledOnce();
    expect(onSignOut).toHaveBeenCalledWith("signed-out");
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
    await settled(other);

    expect(onSignOut).not.toHaveBeenCalled();
    other.close();
    stop();
  });

  // A tab must not sign itself out when it is the one broadcasting.
  it("does not deliver the broadcast back to the sender", async () => {
    const onSignOut = vi.fn();
    const stop = watchForSignOutElsewhere(onSignOut);

    // The message comes from the module's channel, so there is no instance
    // here to send a sentinel through. Wait for another tab to receive it —
    // one dispatch reaches every instance, so once a witness has it, this
    // tab's listener has had its chance too.
    const witness = otherTab();
    const heard = new Promise<void>((resolve) => {
      witness.addEventListener("message", (event: MessageEvent) => {
        if ((event.data as { type?: string } | null)?.type === "signed-out") resolve();
      });
    });
    broadcastSignOut();
    await heard;

    expect(onSignOut).not.toHaveBeenCalled();
    witness.close();
    stop();
  });

  it("reaches a listening tab when broadcast", async () => {
    const heard = vi.fn();
    const other = otherTab();
    other.addEventListener("message", (event: MessageEvent) => {
      heard((event.data as { type?: string }).type);
    });

    broadcastSignOut();
    await vi.waitFor(() => expect(heard).toHaveBeenCalledWith("signed-out"));
    other.close();
  });

  it("propagates pending, failed, and confirmed logout phases", async () => {
    const onSignOut = vi.fn();
    const stop = watchForSignOutElsewhere(onSignOut);
    const other = otherTab();

    other.postMessage({ type: "signing-out", at: Date.now() });
    other.postMessage({ type: "sign-out-error", at: Date.now() });
    other.postMessage({ type: "signed-out", at: Date.now() });
    await settled(other);

    expect(onSignOut.mock.calls.map(([phase]) => phase)).toEqual([
      "signing-out",
      "sign-out-error",
      "signed-out",
    ]);
    other.close();
    stop();
  });

  it("stops notifying once unsubscribed", async () => {
    const onSignOut = vi.fn();
    watchForSignOutElsewhere(onSignOut)();

    const other = otherTab();
    other.postMessage({ type: "signed-out", at: Date.now() });
    await settled(other);

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
    await vi.waitFor(() => expect(seen).not.toHaveLength(0));

    expect(seen).toHaveLength(1);
    expect(Object.keys(seen[0] as object).sort()).toEqual(["at", "type"]);
    other.close();
  });

  it("a heartbeat from an active tab resets this tab's idle timer", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onIdle = vi.fn();
    const stop = watchForIdleness(onIdle, 1_000);
    const other = otherTab();
    try {
      vi.advanceTimersByTime(900);
      other.postMessage({ type: "activity", at: Date.now() });
      await settled(other);

      vi.advanceTimersByTime(900);
      expect(onIdle).not.toHaveBeenCalled();
      vi.advanceTimersByTime(100);
      expect(onIdle).toHaveBeenCalledOnce();
    } finally {
      other.close();
      stop();
      vi.useRealTimers();
    }
  });

  it("throttles a burst of local activity to one cross-tab heartbeat", async () => {
    const seen: string[] = [];
    const other = otherTab();
    other.addEventListener("message", (event: MessageEvent) => {
      const type = (event.data as { type?: string } | null)?.type;
      if (type) seen.push(type);
    });
    const stop = watchForIdleness(vi.fn(), 10_000);

    window.dispatchEvent(new Event("keydown"));
    window.dispatchEvent(new Event("pointerdown"));
    window.dispatchEvent(new Event("wheel"));
    await vi.waitFor(() => expect(seen.filter((type) => type === "activity")).toHaveLength(1));

    stop();
    other.close();
  });
});
