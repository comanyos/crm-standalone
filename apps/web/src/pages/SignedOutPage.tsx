import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";
import { Button } from "../components/ui/Button";

export default function SignedOutPage() {
  const { status, logoutError, login, logout } = useAuth();
  const navigate = useNavigate();

  if (status === "loading") {
    return (
      <SessionMessage role="status" title="Checking your session…">
        Confirming whether your session is still active.
      </SessionMessage>
    );
  }

  if (status === "signing-out") {
    return (
      <SessionMessage role="status" title="Signing out…">
        Waiting for the server to confirm your session has ended.
      </SessionMessage>
    );
  }

  if (status === "sign-out-error") {
    return (
      <SessionMessage role="alert" title="Sign-out needs attention">
        <p>{logoutError ?? "We couldn't confirm that your session ended."}</p>
        <p className="font-medium text-slate-700">Retry before closing this window.</p>
        <Button onClick={() => void logout()}>Retry sign out</Button>
      </SessionMessage>
    );
  }

  if (status === "authenticated") {
    return (
      <SessionMessage role="status" title="You're still signed in">
        Your server session is active.
        <Button onClick={() => navigate("/")}>Return to CRM</Button>
      </SessionMessage>
    );
  }

  // `signed-out` follows a confirmed logout response. `unauthenticated`
  // follows a confirmed 401/403 from GET /auth/session. Both are evidence
  // that no usable server session remains.
  return (
    <SessionMessage role="status" title="You're signed out">
      <p>Your session has ended. You can log in again any time.</p>
      <Button onClick={() => login("/")}>Log in</Button>
    </SessionMessage>
  );
}

function SessionMessage({
  title,
  role,
  children,
}: {
  title: string;
  role: "alert" | "status";
  children: ReactNode;
}) {
  return (
    <main className="flex min-h-screen items-center justify-center bg-paper px-5 py-10">
      <div
        className="flex w-full max-w-md flex-col items-center gap-4 rounded-3xl border border-slate-200 bg-white p-8 text-center shadow-savvy"
        role={role}
        aria-live="polite"
      >
        <div className="mb-2 flex items-center gap-3" aria-label="Savvy CRM">
          <img src="/savvy-wordmark.png" alt="Savvy" className="h-6 w-auto" />
          <span aria-hidden="true" className="h-5 w-px bg-slate-300" />
          <span className="text-xs font-bold tracking-[0.16em] text-slate-950">CRM</span>
        </div>
        <h1 className="font-display text-3xl font-normal tracking-[-0.035em] text-slate-950">{title}</h1>
        <div className="flex flex-col items-center gap-3 text-sm leading-6 text-slate-600">{children}</div>
      </div>
    </main>
  );
}
