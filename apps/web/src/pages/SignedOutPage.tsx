import { useAuth } from "../auth/AuthProvider";
import { Button } from "../components/ui/Button";

/** The heading here is load-bearing: it is what tells the user (and what
 * tells auth.spec.ts) that the session is really gone, so it must not
 * appear while AuthProvider.logout is still waiting on AuthinCore. Offering
 * "Sign in" early would be worse still — that /authorize would resume the
 * session the DELETE is in the middle of ending. */
export default function SignedOutPage() {
  const { status, login } = useAuth();

  if (status === "signing-out") {
    return (
      <div
        className="flex h-screen flex-col items-center justify-center gap-3 text-center"
        role="status"
        aria-live="polite"
      >
        <h1 className="text-lg font-semibold text-slate-900">Signing out…</h1>
        <p className="max-w-sm text-sm text-slate-500">Ending your session.</p>
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col items-center justify-center gap-3 text-center" role="status">
      <h1 className="text-lg font-semibold text-slate-900">You're signed out</h1>
      <p className="max-w-sm text-sm text-slate-500">
        Your session has ended. You can sign in again any time.
      </p>
      <Button onClick={() => login("/")}>Sign in</Button>
    </div>
  );
}
