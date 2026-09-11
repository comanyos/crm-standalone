import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../../auth/AuthProvider";

export function UserMenu() {
  const { session, logout } = useAuth();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const email = session?.claims.email ?? session?.claims.sub ?? "Signed in";
  const roleSummary = session?.claims.roles?.includes("platform_admin")
    ? "Super Admin"
    : session?.claims.roles?.join(", ");

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Account menu"
        className="flex min-h-10 items-center gap-2 rounded-full px-1.5 py-1 text-sm text-slate-700 hover:bg-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 sm:pr-3"
      >
        <span
          aria-hidden="true"
          className="flex h-8 w-8 items-center justify-center rounded-full bg-slate-950 text-xs font-semibold text-white"
        >
          {email.charAt(0).toUpperCase()}
        </span>
        <span className="hidden max-w-[10rem] truncate sm:inline">{email}</span>
      </button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 z-10 mt-2 w-56 overflow-hidden rounded-xl border border-slate-200 bg-white py-1.5 shadow-md"
        >
          {roleSummary && (
            <div className="border-b border-slate-100 px-3 py-2 text-xs text-slate-500">
              {roleSummary}
            </div>
          )}
          <button
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              // Navigate immediately. The destination keeps protected data
              // hidden, waits for server confirmation, and exposes Retry if
              // logout fails or times out.
              void logout();
              navigate("/signed-out");
            }}
            className="block w-full px-4 py-2.5 text-left text-sm font-medium text-slate-700 hover:bg-brand-50/60"
          >
            Sign out
          </button>
        </div>
      )}
    </div>
  );
}
