import { CommandPalette } from "./CommandPalette";
import { UserMenu } from "./UserMenu";

export function Topbar() {
  return (
    <header className="flex h-14 items-center justify-between border-b border-slate-200 bg-white px-6">
      <CommandPalette />
      <UserMenu />
    </header>
  );
}
