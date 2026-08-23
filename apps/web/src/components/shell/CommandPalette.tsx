import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useCrmClients } from "../../lib/useCrmClients";
import { useDebouncedValue } from "../../lib/useDebouncedValue";

interface Command {
  label: string;
  to: string;
}

interface RecordResult {
  key: string;
  label: string;
  detail: string;
  to: string;
  score: number;
}

const COMMANDS: Command[] = [
  { label: "Go to Dashboard", to: "/" },
  { label: "Go to Accounts", to: "/accounts" },
  { label: "Go to Contacts", to: "/contacts" },
  { label: "Go to Leads", to: "/leads" },
  { label: "Go to Opportunities", to: "/opportunities" },
  { label: "Go to Pipelines", to: "/settings/pipelines" },
];

function RecordSearchResults({ query, onSelect }: { query: string; onSelect: (to: string) => void }) {
  const { crm } = useCrmClients();
  const debouncedQuery = useDebouncedValue(query.trim(), 250);
  const recordSearch = useQuery({
    queryKey: ["crm.globalSearch", debouncedQuery],
    enabled: debouncedQuery.length >= 2,
    queryFn: async (): Promise<RecordResult[]> => {
      const response = await crm.searchRecords({ query: debouncedQuery, pageSize: 20 });
      const destinations: Record<string, string> = {
        account: "/accounts",
        contact: "/contacts",
        lead: "/leads",
        opportunity: "/opportunities",
        task: "/tasks",
      };
      return response.records.map((record) => ({
        key: `${record.objectType}-${record.id}`,
        label: record.title,
        detail: `${record.objectType[0].toUpperCase()}${record.objectType.slice(1)}${record.subtitle ? ` · ${record.subtitle}` : ""}`,
        to: record.objectType === "task" ? `/tasks/${record.id}/edit` : `${destinations[record.objectType]}/${record.id}`,
        score: 0,
      }));
    },
  });
  if (recordSearch.isPending) return <li className="px-4 py-3 text-sm text-slate-500">Searching CRM…</li>;
  if ((recordSearch.data?.length ?? 0) === 0)
    return <li className="px-4 py-3 text-sm text-slate-500">No CRM records match “{query.trim()}”.</li>;
  return (
    <>
      <li className="px-4 pb-1 pt-2 text-xs font-semibold uppercase tracking-wide text-slate-500">
        CRM records
      </li>
      {recordSearch.data?.map((record) => (
        <li key={record.key}>
          <button
            type="button"
            role="option"
            aria-selected={false}
            onClick={() => onSelect(record.to)}
            className="block w-full px-4 py-2 text-left text-sm text-slate-700 hover:bg-slate-50"
          >
            <span className="block font-medium text-slate-900">{record.label}</span>
            <span className="block text-xs text-slate-500">{record.detail}</span>
          </button>
        </li>
      ))}
    </>
  );
}

/** Cmd/Ctrl+K brings navigation and the first cross-object search together.
 * Each source remains its normal tenant-scoped, permission-checked list RPC;
 * a failed source is omitted rather than exposing an authorization failure as
 * a side channel. A dedicated indexed SearchRecords RPC can replace this
 * bounded first version when customer data grows beyond list-sized results. */
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

  const navigationResults = useMemo(
    () => COMMANDS.filter((c) => c.label.toLowerCase().includes(query.toLowerCase())),
    [query],
  );
  const hasRecordQuery = query.trim().length >= 2;

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
          placeholder="Search records or jump to a section…"
          aria-label="Search records or jump to a section"
          className="w-full rounded-t-lg border-b border-slate-200 px-4 py-3 text-sm focus:outline-none"
        />
        <ul role="listbox" className="max-h-72 overflow-y-auto py-1">
          {!hasRecordQuery && navigationResults.length === 0 && (
            <li className="px-4 py-3 text-sm text-slate-500">No matches.</li>
          )}
          {hasRecordQuery && (
            <RecordSearchResults
              query={query}
              onSelect={(to) => {
                setOpen(false);
                navigate(to);
              }}
            />
          )}
          {!hasRecordQuery &&
            navigationResults.map((c) => (
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
