import { useEffect } from "react";
import { useAuth } from "../auth/AuthProvider";

/** Immediately starts the Authorization Code + PKCE redirect. Reachable
 * directly (a bookmark, a "Sign in" link) as well as automatically via
 * RequireAuth — both paths go through the same beginLogin call. */
export default function LoginPage() {
  const { login, status } = useAuth();

  useEffect(() => {
    if (status === "unauthenticated" || status === "signed-out") {
      login();
    }
  }, [status, login]);

  return (
    <div className="flex h-screen items-center justify-center" role="status" aria-live="polite">
      <span className="text-sm text-slate-500">Redirecting to sign in…</span>
    </div>
  );
}
