import { useEffect, type ReactNode } from "react";
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
