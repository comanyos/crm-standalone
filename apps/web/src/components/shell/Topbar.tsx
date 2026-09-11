import { useAuth } from "../../auth/AuthProvider";
import { CommandPalette } from "./CommandPalette";
import { NotificationBell } from "./NotificationBell";
import { UserMenu } from "./UserMenu";

export function Topbar({ onOpenNav }: { onOpenNav: () => void }) {
  const { hasRole } = useAuth();
  const superAdmin = hasRole("platform_admin");

  return (
    <header className="relative z-30 flex h-14 shrink-0 items-center gap-2 border-b border-slate-200 bg-white px-4 sm:px-6">
      {/* The only way to reach navigation below `lg`, where the sidebar is a
          drawer. Hidden from `lg` up, where the sidebar is always on screen
          and a control that opens what is already open would be a lie. */}
      <button
        type="button"
        onClick={onOpenNav}
        aria-label="Open navigation"
        className="-ml-1 rounded-full p-2 text-slate-600 hover:bg-slate-100 hover:text-slate-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 lg:hidden"
      >
        <svg
          aria-hidden="true"
          viewBox="0 0 24 24"
          className="h-5 w-5"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.8"
        >
          <path d="M4 7h16M4 12h16M4 17h16" strokeLinecap="round" />
        </svg>
      </button>
      <span className="flex items-center gap-2 lg:hidden" aria-label="Savvy CRM">
        <img src="/savvy-wordmark.png" alt="Savvy" className="h-4 w-auto" />
        <span aria-hidden="true" className="h-4 w-px bg-slate-300" />
        <span className="text-[10px] font-bold tracking-[0.16em] text-slate-950">
          {superAdmin ? "ADMIN" : "CRM"}
        </span>
      </span>
      {superAdmin ? (
        <div className="hidden text-xs font-semibold uppercase tracking-[0.14em] text-slate-500 sm:block">
          Platform control center
        </div>
      ) : (
        <CommandPalette />
      )}
      <div className="ml-auto flex items-center gap-1">
        {!superAdmin && <NotificationBell />}
        <UserMenu />
      </div>
    </header>
  );
}
