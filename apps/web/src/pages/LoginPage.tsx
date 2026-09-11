import { useEffect, useMemo } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";

function safeReturnTo(value: string | null): string {
  if (!value || !value.startsWith("/") || value.startsWith("//")) return "/";
  return value;
}

/** Compatibility route for old bookmarks. Authentication now lives in the
 * Login / Sign-up dialog on the marketing site, so this page never renders a
 * second form. */
export default function LoginPage() {
  const { status, login } = useAuth();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const returnTo = useMemo(() => safeReturnTo(searchParams.get("returnTo")), [searchParams]);

  useEffect(() => {
    if (status === "authenticated") {
      navigate(returnTo, { replace: true });
      return;
    }
    if (status === "unauthenticated") login(returnTo);
  }, [login, navigate, returnTo, status]);

  return (
    <main className="flex min-h-screen items-center justify-center bg-paper px-5" role="status" aria-live="polite">
      <div className="flex flex-col items-center gap-4 text-center">
        <span className="inline-flex items-center gap-2 rounded-xl bg-white px-4 py-3 shadow-savvy-sm" aria-label="Savvy CRM">
          <img src="/savvy-wordmark.png" alt="Savvy" className="h-5 w-auto" />
          <span aria-hidden="true" className="h-5 w-px bg-slate-300" />
          <span className="text-[11px] font-bold tracking-[0.16em] text-slate-950">CRM</span>
        </span>
        <span aria-hidden="true" className="h-5 w-5 animate-spin rounded-full border-2 border-slate-300 border-t-brand-600" />
        <p className="text-sm text-slate-600">
          {status === "authenticated" ? "Opening your workspace…" : "Opening secure sign in…"}
        </p>
      </div>
    </main>
  );
}
