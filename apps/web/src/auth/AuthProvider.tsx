import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  beginLogin,
  completeCallback,
  endServerSession,
  isExpiringSoon,
  renewFromStoredRefreshToken,
  revokeStoredRefreshToken,
  type AuthSession,
  type OidcConfig,
  type ParsedCallback,
} from "@crm/auth-client";
import { useToast } from "../components/ui/Toast";
import { AUTH_CALLBACK_PATH } from "../lib/auth-config";
import { broadcastSignOut, watchForIdleness, watchForSignOutElsewhere } from "./session-policy";

export type AuthStatus = "loading" | "authenticated" | "unauthenticated" | "signing-out" | "signed-out";

interface AuthContextValue {
  status: AuthStatus;
  session: AuthSession | null;
  /** Starts the top-level PKCE redirect, remembering `redirectAfter` (the
   * in-app path to return to). */
  login: (redirectAfter?: string) => void;
  /** Resolves once the server-side sign-out calls have settled (or the
   * grace period below has elapsed), at which point `status` is
   * "signed-out". Callers that just want the user out of the UI can drop
   * the promise — the local session is gone synchronously either way. */
  logout: () => Promise<void>;
  /** Exchanges a top-level OAuth callback and activates the session. Used
   * only by the /auth/callback route. Returns where to navigate next. */
  completeLogin: (callback: ParsedCallback) => Promise<string>;
  hasScope: (scope: string) => boolean;
  hasRole: (role: string) => boolean;
}

const AuthContext = createContext<AuthContextValue | null>(null);

/** Re-renews ~60s before the access token's own `exp` claim, so a call in
 * flight never races an expiry the backend would reject. */
const RENEW_MARGIN_MS = 60_000;

/** How long sign-out waits for AuthinCore to confirm the session is gone
 * before showing "You're signed out" anyway. Long enough to cover an
 * ordinary round trip (including the DELETE's CORS preflight), short enough
 * that an unreachable identity provider cannot strand the user on a screen
 * that still looks signed in. */
const SIGN_OUT_GRACE_MS = 3_000;

/** Resolves when `work` settles or the grace period expires, whichever is
 * first. Deliberately a race and not an AbortSignal: on timeout the
 * requests must keep running (they are `keepalive`, so they outlive the
 * document too) — aborting them would defeat the entire point of waiting. */
function withinGracePeriod(work: Promise<unknown>): Promise<void> {
  return Promise.race([
    work.then(() => undefined),
    new Promise<void>((resolve) => setTimeout(resolve, SIGN_OUT_GRACE_MS)),
  ]);
}

export function AuthProvider({ config, children }: { config: OidcConfig; children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus>("loading");
  const [session, setSession] = useState<AuthSession | null>(null);
  const renewTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const toast = useToast();

  const clearRenewTimer = useCallback(() => {
    if (renewTimer.current) {
      clearTimeout(renewTimer.current);
      renewTimer.current = null;
    }
  }, []);

  const scheduleRenew = useCallback(
    (s: AuthSession) => {
      clearRenewTimer();
      const delay = Math.max(s.expiresAt - Date.now() - RENEW_MARGIN_MS, 0);
      renewTimer.current = setTimeout(() => {
        renewFromStoredRefreshToken(config)
          .then((renewed) => {
            setSession(renewed);
            setStatus("authenticated");
            scheduleRenew(renewed);
          })
          .catch(() => {
            setSession(null);
            setStatus("unauthenticated");
            toast.show({ tone: "warning", message: "Your session expired. Please sign in again." });
          });
      }, delay);
    },
    [clearRenewTimer, config, toast],
  );

  useEffect(() => {
    // The callback route owns its own exchange via completeLogin and stores
    // the resulting refresh token itself — nothing to renew here yet.
    if (window.location.pathname === AUTH_CALLBACK_PATH) return;

    let cancelled = false;
    renewFromStoredRefreshToken(config)
      .then((s) => {
        if (cancelled) return;
        setSession(s);
        setStatus("authenticated");
        scheduleRenew(s);
      })
      .catch(() => {
        if (cancelled) return;
        setStatus("unauthenticated");
      });
    return () => {
      cancelled = true;
      clearRenewTimer();
    };
    // Runs once on mount; config is static for the lifetime of the app.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const login = useCallback(
    (redirectAfter?: string) => {
      void beginLogin(config, redirectAfter ?? window.location.pathname + window.location.search);
    },
    [config],
  );

  /** Drops the local session immediately, then waits for the server side to
   * catch up before declaring the user signed out.
   *
   * The wait is the point. `DELETE /api/v1/sessions/me` is what clears
   * AuthinCore's own session cookie; until it lands, a fresh /authorize
   * silently re-authenticates the same user with no password. Firing it
   * without waiting meant "You're signed out" could render — and the user
   * could navigate or close the tab — while the request was still in
   * flight, leaving a live session behind a screen that claimed otherwise.
   * (This was not hypothetical: it made auth.spec.ts's sign-out assertion
   * flaky, because Playwright navigates the instant the heading appears.)
   *
   * Both calls swallow their own failures, so the grace period is the only
   * bound needed here. */
  const logout = useCallback(async () => {
    clearRenewTimer();
    const accessToken = session?.accessToken;
    setSession(null);
    setStatus("signing-out");
    // Told to the other tabs first, and before awaiting anything. Each one
    // holds its own live access token in memory, so any delay here is a
    // window in which the user believes they have signed out everywhere and
    // a second tab is still making authenticated calls.
    broadcastSignOut();
    await withinGracePeriod(
      Promise.all([
        revokeStoredRefreshToken(config),
        accessToken ? endServerSession(config, accessToken) : Promise.resolve(),
      ]),
    );
    setStatus("signed-out");
  }, [clearRenewTimer, config, session]);

  /** Ends this tab's session when the decision was made elsewhere (another
   * tab signed out) or by inaction (the idle timeout).
   *
   * It does not call endServerSession, and that asymmetry is deliberate: the
   * other tab already ended the AuthinCore session, and an idle background
   * tab must not be able to sign the user out of the tab they are actively
   * working in.
   *
   * It does still revoke this tab's refresh token. sessionStorage is
   * per-tab, so that token is this tab's own and survives the tab's session
   * ending — without this, a reload would silently restore a session the
   * user was just told had ended. revokeStoredRefreshToken clears local
   * storage synchronously and only then talks to the network, so dropping
   * the promise is safe: the token is gone from this browser either way. */
  const endLocalSession = useCallback(() => {
    clearRenewTimer();
    void revokeStoredRefreshToken(config);
    setSession(null);
    setStatus("signed-out");
  }, [clearRenewTimer, config]);

  // Sign-out in another tab of this app ends this one too. Without it, the
  // user closes the tab they signed out of and the app is still open, still
  // signed in, in the tab behind it.
  useEffect(() => watchForSignOutElsewhere(endLocalSession), [endLocalSession]);

  // Idle timeout. Only while there is actually a session to end — an
  // unauthenticated tab sitting on the sign-in screen has nothing to time
  // out, and starting a timer for it would flip a signed-out tab to
  // "signed-out" for no reason a user could explain.
  useEffect(() => {
    if (status !== "authenticated") return;
    return watchForIdleness(() => {
      endLocalSession();
      toast.show({
        tone: "warning",
        message: "You were signed out after a period of inactivity.",
      });
    });
  }, [status, endLocalSession, toast]);

  const completeLogin = useCallback(
    async (callback: ParsedCallback) => {
      const { session: s, redirectAfter } = await completeCallback(config, callback);
      setSession(s);
      setStatus("authenticated");
      scheduleRenew(s);
      return redirectAfter;
    },
    [config, scheduleRenew],
  );

  const hasScope = useCallback(
    (scope: string) => {
      const scopes = session?.claims.scopes ?? session?.claims.scope?.split(" ") ?? [];
      return scopes.includes(scope) || scopes.includes("crm:*");
    },
    [session],
  );

  const hasRole = useCallback((role: string) => (session?.claims.roles ?? []).includes(role), [session]);

  const value = useMemo<AuthContextValue>(
    () => ({ status, session, login, logout, completeLogin, hasScope, hasRole }),
    [status, session, login, logout, completeLogin, hasScope, hasRole],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}

/** True if the session is still valid right now — checked at call time
 * rather than derived at render, since expiry is a moving target. */
export function useIsSessionValid(): boolean {
  const { session } = useAuth();
  return !!session && !isExpiringSoon(session, 0);
}
