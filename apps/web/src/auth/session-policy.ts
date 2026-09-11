/** Browser session policy: inactivity and cross-tab coordination.
 *
 * The authoritative session is an HttpOnly cookie. Tabs coordinate only
 * non-sensitive events: deliberate activity and the progress of a logout.
 * No cookie, token, email, user ID, or organization data enters the channel.
 */

export const IDLE_TIMEOUT_MS = 30 * 60 * 1000;

/** One heartbeat per interval is ample to keep another tab's 30-minute idle
 * timer alive without broadcasting every keystroke or scroll. */
export const ACTIVITY_BROADCAST_THROTTLE_MS = 15 * 1000;

const WINDOW_ACTIVITY_EVENTS = ["keydown", "pointerdown", "wheel", "touchstart"] as const;
const AUTH_CHANNEL = "crm_auth_sign_out";

export type SignOutPhase = "signing-out" | "signed-out" | "sign-out-error";

type AuthChannelMessage =
  | { type: "activity"; at: number }
  | { type: SignOutPhase; at: number };

let channel: BroadcastChannel | null | undefined;
const signOutListeners = new Set<(phase: SignOutPhase) => void>();
const activityListeners = new Set<() => void>();

function isSignOutPhase(value: unknown): value is SignOutPhase {
  return value === "signing-out" || value === "signed-out" || value === "sign-out-error";
}

function sharedChannel(): BroadcastChannel | null {
  if (channel !== undefined) return channel;
  if (typeof BroadcastChannel === "undefined") {
    channel = null;
    return channel;
  }
  try {
    const opened = new BroadcastChannel(AUTH_CHANNEL);
    opened.addEventListener("message", (event: MessageEvent) => {
      const data = event.data as Partial<AuthChannelMessage> | null;
      if (!data || typeof data.at !== "number" || !Number.isFinite(data.at)) return;
      if (data.type === "activity") {
        for (const listener of activityListeners) listener();
        return;
      }
      if (isSignOutPhase(data.type)) {
        for (const listener of signOutListeners) listener(data.type);
      }
    });
    channel = opened;
  } catch {
    channel = null;
  }
  return channel;
}

function post(message: AuthChannelMessage): void {
  sharedChannel()?.postMessage(message);
}

/** Tells other tabs to hide protected data while logout is pending, then to
 * show either confirmed success or the retryable failure state. */
export function broadcastSignOut(phase: SignOutPhase = "signed-out"): void {
  post({ type: phase, at: Date.now() });
}

export function watchForSignOutElsewhere(
  onSignOut: (phase: SignOutPhase) => void,
): () => void {
  if (!sharedChannel()) return () => {};
  signOutListeners.add(onSignOut);
  return () => signOutListeners.delete(onSignOut);
}

/** Calls `onIdle` once after `timeoutMs` without deliberate activity in any
 * same-origin CRM tab. Local activity resets immediately and emits at most
 * one heartbeat per throttle interval; remote heartbeats reset without being
 * rebroadcast, so they cannot create a message loop. */
export function watchForIdleness(onIdle: () => void, timeoutMs: number = IDLE_TIMEOUT_MS): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let stopped = false;
  let lastHeartbeatAt = Number.NEGATIVE_INFINITY;

  const reset = () => {
    if (stopped) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(fire, timeoutMs);
  };

  const noteLocalActivity = (event: Event) => {
    // Hiding a tab is not activity. Returning to it is.
    if (event.type === "visibilitychange" && document.visibilityState !== "visible") return;
    reset();
    const now = Date.now();
    if (now >= lastHeartbeatAt && now - lastHeartbeatAt < ACTIVITY_BROADCAST_THROTTLE_MS) return;
    lastHeartbeatAt = now;
    post({ type: "activity", at: now });
  };

  const stop = () => {
    if (timer) clearTimeout(timer);
    timer = null;
    activityListeners.delete(reset);
    for (const event of WINDOW_ACTIVITY_EVENTS) window.removeEventListener(event, noteLocalActivity);
    document.removeEventListener("visibilitychange", noteLocalActivity);
  };

  function fire() {
    if (stopped) return;
    stopped = true;
    stop();
    onIdle();
  }

  activityListeners.add(reset);
  // Opens the shared channel before activity can occur. If unsupported, the
  // local idle policy continues to work; only cross-tab coordination degrades.
  sharedChannel();
  for (const event of WINDOW_ACTIVITY_EVENTS) {
    window.addEventListener(event, noteLocalActivity, { passive: true });
  }
  document.addEventListener("visibilitychange", noteLocalActivity, { passive: true });
  reset();

  return () => {
    stopped = true;
    stop();
  };
}
