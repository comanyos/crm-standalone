import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

interface Command {
  label: string;
  to: string;
}

const COMMANDS: Command[] = [
  { label: "Go to Dashboard", to: "/" },
  { label: "Go to Accounts", to: "/accounts" },
  { label: "Go to Contacts", to: "/contacts" },
  { label: "Go to Leads", to: "/leads" },
  { label: "Go to Opportunities", to: "/opportunities" },
  { label: "Go to Pipelines", to: "/settings/pipelines" },
];

/** Cmd/Ctrl+K navigation palette. Global record search (Phase 6) will
 * extend `COMMANDS` with live results — this is navigation-only today. */
export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const navigate = useNavigate();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v) => !v);
      }
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (open) {
      setQuery("");
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  const results = useMemo(
    () => COMMANDS.filter((c) => c.label.toLowerCase().includes(query.toLowerCase())),
    [query],
  );

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex items-center gap-2 rounded-md border border-slate-300 px-3 py-1.5 text-xs text-slate-500 hover:bg-slate-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
      >
        <span>Search or jump to…</span>
        <kbd className="rounded border border-slate-300 bg-slate-50 px-1 font-sans">⌘K</kbd>
      </button>
    );
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Command palette"
      className="fixed inset-0 z-50 flex items-start justify-center pt-24"
    >
      <button
        type="button"
        aria-label="Close command palette"
        onClick={() => setOpen(false)}
        className="absolute inset-0 h-full w-full cursor-default bg-slate-900/40"
      />
      <div className="relative w-full max-w-lg rounded-lg bg-white shadow-xl">
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Jump to a section…"
          aria-label="Jump to a section"
          className="w-full rounded-t-lg border-b border-slate-200 px-4 py-3 text-sm focus:outline-none"
        />
        <ul role="listbox" className="max-h-72 overflow-y-auto py-1">
          {results.length === 0 && <li className="px-4 py-3 text-sm text-slate-500">No matches.</li>}
          {results.map((c) => (
            <li key={c.to}>
              <button
                type="button"
                role="option"
                aria-selected={false}
                onClick={() => {
                  setOpen(false);
                  navigate(c.to);
                }}
                className="block w-full px-4 py-2 text-left text-sm text-slate-700 hover:bg-slate-50"
              >
                {c.label}
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
