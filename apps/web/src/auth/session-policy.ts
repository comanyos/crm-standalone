/** Session policy: idle timeout and cross-tab sign-out (task.md §1.2).
 *
 * Both exist because of how this app holds a session, and neither is
 * cosmetic.
 *
 * The access token lives only in memory and the refresh token lives in
 * sessionStorage, which is per-tab and cleared when the tab closes. That is a
 * deliberately conservative choice, but it leaves two gaps that a shared or
 * unattended machine makes real: a tab left open renews itself indefinitely,
 * and signing out in one tab leaves every other tab of the same app still
 * signed in, each holding a live access token.
 */

/** How long without user interaction before the session is ended.
 *
 * Thirty minutes is the common default for an internal business application:
 * long enough not to interrupt someone reading a long record or on a call,
 * short enough to matter on an unlocked laptop. It bounds only this app's
 * local session — AuthinCore's own session lifetime is separate, and signing
 * out ends both. */
export const IDLE_TIMEOUT_MS = 30 * 60 * 1000;

/** Interaction that counts as the user still being here.
 *
 * Deliberately not `mousemove`: a stray cursor nudge, a trackpad resting
 * under a palm, or a page animating under the pointer would all keep a
 * session alive forever, which turns the timeout into decoration. These are
 * events that require intent. `visibilitychange` is included so returning to
 * a backgrounded tab counts — the user is demonstrably back. */
const ACTIVITY_EVENTS = ["keydown", "pointerdown", "wheel", "touchstart", "visibilitychange"] as const;

/** Calls `onIdle` once, after IDLE_TIMEOUT_MS with no qualifying activity.
 * Returns a function that stops watching and cancels any pending timer. */
export function watchForIdleness(onIdle: () => void, timeoutMs: number = IDLE_TIMEOUT_MS): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let stopped = false;

  const fire = () => {
    if (stopped) return;
    stopped = true;
    stop();
    onIdle();
  };

  const reset = () => {
    if (stopped) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(fire, timeoutMs);
  };

  const stop = () => {
    if (timer) clearTimeout(timer);
    timer = null;
    for (const event of ACTIVITY_EVENTS) {
      window.removeEventListener(event, reset);
    }
  };

  for (const event of ACTIVITY_EVENTS) {
    // Passive: these listeners only observe, and must never delay a scroll
    // or a tap to do it.
    window.addEventListener(event, reset, { passive: true });
  }
  reset();

  return () => {
    stopped = true;
    stop();
  };
}

/** Channel this app's tabs use to tell each other about a sign-out.
 *
 * BroadcastChannel rather than a localStorage key and its `storage` event.
 * Both work, but the storage trick means writing session-related state into
 * localStorage — the one storage this app has deliberately kept every token
 * out of — and then relying on other tabs to interpret a write as a message.
 * BroadcastChannel is the API for the thing actually being done: a message,
 * to same-origin tabs, that nothing persists.
 *
 * A channel only ever delivers to *other* BroadcastChannel instances, never
 * back to the sender, which is exactly the semantics wanted here. */
const SIGN_OUT_CHANNEL = "crm_auth_sign_out";

/** The message body. It is an event, not a payload: other tabs need to know
 * that a sign-out happened, and a message carrying anything session-derived
 * would put it on a channel any same-origin script can subscribe to. */
interface SignOutMessage {
  type: "signed-out";
  at: number;
}

/** The tab's single channel instance, and the callbacks listening on it.
 *
 * One shared instance per tab, not one per call, and that is load-bearing: a
 * BroadcastChannel does not deliver a message back to the instance that
 * posted it, but it does deliver to every *other* instance — including
 * another one in the same document. Posting from a throwaway channel
 * therefore signs out the very tab that just called logout, racing its own
 * sign-out and clobbering the "signing-out" state the user is looking at.
 *
 * `undefined` means "not attempted yet"; `null` means "attempted and
 * unavailable", so an unsupported browser is not retried on every call.
 * Older Safari has no BroadcastChannel: losing cross-tab propagation there
 * degrades the experience, and must not break the sign-out the user asked
 * for. */
let channel: BroadcastChannel | null | undefined;
const listeners = new Set<() => void>();

function sharedChannel(): BroadcastChannel | null {
  if (channel !== undefined) return channel;
  if (typeof BroadcastChannel === "undefined") {
    channel = null;
    return channel;
  }
  try {
    const opened = new BroadcastChannel(SIGN_OUT_CHANNEL);
    opened.addEventListener("message", (event: MessageEvent) => {
      // Anything on this origin can post to a named channel, so the shape is
      // checked rather than assumed. The message carries no authority — it
      // ends this tab's local session only — but an unrecognised message
      // should be ignored rather than acted on.
      const data = event.data as Partial<SignOutMessage> | null;
      if (!data || data.type !== "signed-out") return;
      for (const listener of listeners) listener();
    });
    channel = opened;
  } catch {
    channel = null;
  }
  return channel;
}

/** Tells this app's other tabs that the user signed out. */
export function broadcastSignOut(): void {
  const message: SignOutMessage = { type: "signed-out", at: Date.now() };
  sharedChannel()?.postMessage(message);
}

/** Runs `onSignOut` when another tab of this app signs out. Returns an
 * unsubscribe function.
 *
 * The channel itself is never closed. It is one object for the lifetime of
 * the document, and closing it when a single subscriber unsubscribes would
 * silently deafen every other one — including, in React's development
 * double-invocation of effects, the subscriber that immediately replaces it. */
export function watchForSignOutElsewhere(onSignOut: () => void): () => void {
  if (!sharedChannel()) return () => {};
  listeners.add(onSignOut);
  return () => {
    listeners.delete(onSignOut);
  };
}
