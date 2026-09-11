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
import { useQueryClient } from "@tanstack/react-query";
import { useToast } from "../components/ui/Toast";
import {
  endSession,
  loadSession,
  type AuthSession,
} from "./api";
import {
  broadcastSignOut,
  watchForIdleness,
  watchForSignOutElsewhere,
  type SignOutPhase,
} from "./session-policy";
import { watchForUnauthenticatedResponses } from "./unauthenticated-response";

export type AuthStatus =
  "loading" | "authenticated" | "unauthenticated" | "signing-out" | "sign-out-error" | "signed-out";

interface AuthContextValue {
  status: AuthStatus;
  session: AuthSession | null;
  logoutError: string | null;
  /** Opens the marketing site's first-party auth dialog and remembers the
   * protected path the user was trying to reach. */
  login: (redirectAfter?: string) => void;
  logout: () => Promise<void>;
  hasScope: (scope: string) => boolean;
  hasRole: (role: string) => boolean;
}

const AuthContext = createContext<AuthContextValue | null>(null);
const SIGN_OUT_CONFIRMATION_TIMEOUT_MS = 3_000;
const SIGN_OUT_ERROR =
  "We couldn't confirm that your server session ended. Your CRM data is hidden, but the session may still be active.";

function confirmWithinTimeout(work: Promise<unknown>): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("sign-out confirmation timed out")),
      SIGN_OUT_CONFIRMATION_TIMEOUT_MS,
    );
    work.then(
      () => {
        clearTimeout(timer);
        resolve();
      },
      (error: unknown) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

function safeRedirectAfter(value?: string): string {
  if (!value || !value.startsWith("/") || value.startsWith("//")) return "/";
  return value;
}

export function AuthProvider({
  apiUrl,
  authPortalUrl,
  children,
}: {
  apiUrl: string;
  authPortalUrl: string;
  children: ReactNode;
}) {
  const [status, setStatus] = useState<AuthStatus>("loading");
  const statusRef = useRef<AuthStatus>("loading");
  const [session, setSession] = useState<AuthSession | null>(null);
  const [logoutError, setLogoutError] = useState<string | null>(null);
  const expiryTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const toast = useToast();
  const queryClient = useQueryClient();

  const transitionTo = useCallback((nextStatus: AuthStatus) => {
    statusRef.current = nextStatus;
    setStatus(nextStatus);
  }, []);

  const clearExpiryTimer = useCallback(() => {
    if (expiryTimer.current) {
      clearTimeout(expiryTimer.current);
      expiryTimer.current = null;
    }
  }, []);

  const clearLocalSession = useCallback(() => {
    clearExpiryTimer();
    setSession(null);
    // Query data is tenant-scoped. Remove it as soon as the local session is
    // gone, even while a server-side logout confirmation is still pending.
    queryClient.clear();
  }, [clearExpiryTimer, queryClient]);

  const expireSession = useCallback(() => {
    clearLocalSession();
    transitionTo("unauthenticated");
    toast.show({ tone: "warning", message: "Your session expired. Please log in again." });
  }, [clearLocalSession, toast, transitionTo]);

  const rejectActiveSession = useCallback(() => {
    // A 401 while loading is the ordinary "no session" result, and a 401
    // from the idempotent logout endpoint already satisfies logout. Only a
    // live authenticated session should take this separate expiry path.
    if (statusRef.current !== "authenticated") return;
    clearLocalSession();
    setLogoutError(null);
    transitionTo("unauthenticated");
    toast.show({
      tone: "warning",
      message: "Your session is no longer active. Please log in again.",
    });
  }, [clearLocalSession, toast, transitionTo]);

  useEffect(() => watchForUnauthenticatedResponses(rejectActiveSession), [rejectActiveSession]);

  const activateSession = useCallback(
    (nextSession: AuthSession) => {
      clearExpiryTimer();
      setSession(nextSession);
      setLogoutError(null);
      transitionTo("authenticated");
      const schedule = () => {
        const remaining = nextSession.expiresAt - Date.now();
        if (remaining <= 0) {
          expireSession();
          return;
        }
        // Browser timers accept at most a signed 32-bit delay. Recheck after
        // that bound instead of expiring a longer-lived session early.
        expiryTimer.current = setTimeout(schedule, Math.min(remaining, 2_147_000_000));
      };
      schedule();
    },
    [clearExpiryTimer, expireSession, transitionTo],
  );

  useEffect(() => {
    let cancelled = false;
    loadSession(apiUrl)
      .then((current) => {
        if (cancelled) return;
        if (current) activateSession(current);
        else {
          clearLocalSession();
          transitionTo("unauthenticated");
        }
      })
      .catch(() => {
        if (cancelled) return;
        // A temporarily unreachable API is not proof of authentication. The
        // login page can retry, and protected data never renders meanwhile.
        clearLocalSession();
        transitionTo("unauthenticated");
      });
    return () => {
      cancelled = true;
      clearExpiryTimer();
    };
  }, [activateSession, apiUrl, clearExpiryTimer, clearLocalSession, transitionTo]);

  const login = useCallback((redirectAfter?: string) => {
    const destination = new URL(authPortalUrl);
    destination.searchParams.set("auth", "login");
    destination.searchParams.set(
      "returnTo",
      safeRedirectAfter(redirectAfter ?? window.location.pathname + window.location.search),
    );
    if (!destination.hash) destination.hash = "top";
    window.location.assign(destination);
  }, [authPortalUrl]);

  const logout = useCallback(async () => {
    clearLocalSession();
    setLogoutError(null);
    transitionTo("signing-out");
    broadcastSignOut("signing-out");
    try {
      await confirmWithinTimeout(endSession(apiUrl));
      transitionTo("signed-out");
      broadcastSignOut("signed-out");
    } catch {
      setLogoutError(SIGN_OUT_ERROR);
      transitionTo("sign-out-error");
      broadcastSignOut("sign-out-error");
    }
  }, [apiUrl, clearLocalSession, transitionTo]);

  const applySignOutFromAnotherTab = useCallback(
    (phase: SignOutPhase) => {
      clearLocalSession();
      setLogoutError(phase === "sign-out-error" ? SIGN_OUT_ERROR : null);
      transitionTo(phase);
    },
    [clearLocalSession, transitionTo],
  );

  useEffect(() => watchForSignOutElsewhere(applySignOutFromAnotherTab), [applySignOutFromAnotherTab]);

  useEffect(() => {
    if (status !== "authenticated") return;
    return watchForIdleness(() => {
      toast.show({ tone: "warning", message: "Signing you out after a period of inactivity." });
      void logout();
    });
  }, [logout, status, toast]);

  const hasScope = useCallback(
    (scope: string) => {
      const scopes = session?.claims.scopes ?? session?.claims.scope?.split(" ") ?? [];
      return scopes.includes(scope) || scopes.includes("crm:*");
    },
    [session],
  );

  const hasRole = useCallback((role: string) => (session?.claims.roles ?? []).includes(role), [session]);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      session,
      logoutError,
      login,
      logout,
      hasScope,
      hasRole,
    }),
    [
      status,
      session,
      logoutError,
      login,
      logout,
      hasScope,
      hasRole,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}

export function useIsSessionValid(): boolean {
  const { session } = useAuth();
  return !!session && session.expiresAt > Date.now();
}
