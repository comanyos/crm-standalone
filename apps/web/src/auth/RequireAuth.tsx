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
  if (status === "signing-out" || status === "sign-out-error" || status === "signed-out") {
    return <Navigate to="/signed-out" replace />;
  }

  if (status === "loading") {
    return <AuthTransition>Loading…</AuthTransition>;
  }

  if (status !== "authenticated") {
    return <AuthTransition>Redirecting to sign in…</AuthTransition>;
  }

  return <>{children}</>;
}

function AuthTransition({ children }: { children: ReactNode }) {
  return (
    <main className="flex h-screen items-center justify-center bg-paper px-5" role="status" aria-live="polite">
      <div className="flex flex-col items-center gap-4 text-center">
        <span className="inline-flex items-center gap-2 rounded-xl bg-white px-4 py-3 shadow-savvy-sm" aria-label="Savvy CRM">
          <img src="/savvy-wordmark.png" alt="Savvy" className="h-5 w-auto" />
          <span aria-hidden="true" className="h-5 w-px bg-slate-300" />
          <span className="text-[11px] font-bold tracking-[0.16em] text-slate-950">CRM</span>
        </span>
        <span aria-hidden="true" className="h-5 w-5 animate-spin rounded-full border-2 border-slate-300 border-t-brand-600" />
        <span className="text-sm text-slate-500">{children}</span>
      </div>
    </main>
  );
}
