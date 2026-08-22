import { useEffect, type ReactNode } from "react";
import { Navigate } from "react-router-dom";
import { useAuth } from "./AuthProvider";

/** Wraps every protected route. Backend authorization remains the sole
 * authority — this only decides what to render client-side. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { status, login } = useAuth();

  useEffect(() => {
    if (status === "unauthenticated") {
      login();
    }
  }, [status, login]);

  // A session that ended somewhere else — the idle timeout, or another tab
  // signing out — leaves this tab on a protected route with no session and
  // no redirect coming: the effect above deliberately fires only for
  // "unauthenticated", because "signed-out" is a decision, not an expiry, and
  // bouncing the user straight back to the identity provider would undo it.
  //
  // Without this the tab renders "Redirecting to sign in…" indefinitely,
  // describing something that is never going to happen. Sending it to the
  // same confirmation the signing-out tab shows means every route ends in an
  // explanation the user can act on.
  if (status === "signed-out") {
    return <Navigate to="/signed-out" replace />;
  }

  if (status === "loading") {
    return (
      <div className="flex h-screen items-center justify-center" role="status" aria-live="polite">
        <span className="text-sm text-slate-500">Loading…</span>
      </div>
    );
  }

  if (status !== "authenticated") {
    return (
      <div className="flex h-screen items-center justify-center" role="status" aria-live="polite">
        <span className="text-sm text-slate-500">Redirecting to sign in…</span>
      </div>
    );
  }

  return <>{children}</>;
}
