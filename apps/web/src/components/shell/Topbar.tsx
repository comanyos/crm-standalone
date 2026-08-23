import { CommandPalette } from "./CommandPalette";
import { UserMenu } from "./UserMenu";

export function Topbar({ onOpenNav }: { onOpenNav: () => void }) {
  return (
    <header className="flex h-14 items-center gap-2 border-b border-slate-200 bg-white px-4 sm:px-6">
      {/* The only way to reach navigation below `lg`, where the sidebar is a
          drawer. Hidden from `lg` up, where the sidebar is always on screen
          and a control that opens what is already open would be a lie. */}
      <button
        type="button"
        onClick={onOpenNav}
        aria-label="Open navigation"
        className="-ml-1 rounded p-2 text-slate-600 hover:bg-slate-100 hover:text-slate-900 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 lg:hidden"
      >
        <span aria-hidden="true">☰</span>
      </button>
      <CommandPalette />
      <div className="ml-auto">
        <UserMenu />
      </div>
    </header>
  );
}
