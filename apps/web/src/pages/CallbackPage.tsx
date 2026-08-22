import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { parseCallback } from "@crm/auth-client";
import { useAuth } from "../auth/AuthProvider";
import { Button } from "../components/ui/Button";

/** The OAuth2 redirect_uri target. Runs at the top level after a real login
 * — the silent-renew iframe case is intercepted earlier in main.tsx and
 * never mounts the full app, let alone this route. */
export default function CallbackPage() {
  const { completeLogin } = useAuth();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const ran = useRef(false);

  useEffect(() => {
    // Effects run twice under StrictMode in dev; the exchange consumes a
    // single-use code, so a second attempt must not re-trigger it.
    if (ran.current) return;
    ran.current = true;

    const callback = parseCallback(window.location.search);
    completeLogin(callback)
      .then((redirectAfter) => navigate(redirectAfter, { replace: true }))
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)));
  }, [completeLogin, navigate]);

  if (error) {
    return (
      <div className="flex h-screen flex-col items-center justify-center gap-3 text-center" role="alert">
        <h1 className="text-lg font-semibold text-slate-900">Sign-in failed</h1>
        <p className="max-w-sm text-sm text-slate-500">{error}</p>
        <Button onClick={() => navigate("/login", { replace: true })}>Try again</Button>
      </div>
    );
  }

  return (
    <div className="flex h-screen items-center justify-center" role="status" aria-live="polite">
      <span className="text-sm text-slate-500">Finishing sign in…</span>
    </div>
  );
}
