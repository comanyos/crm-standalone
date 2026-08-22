import { useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useCrmClients } from "../lib/useCrmClients";
import { useDebouncedValue } from "../lib/useDebouncedValue";

interface AccountPickerProps {
  label: string;
  /** The selected account's id, or "" for none. */
  value: string;
  /** The selected account's name, shown in the input when it isn't focused.
   * The caller owns this (usually from a prior Get/List response) so the
   * picker never has to resolve an id back to a name itself. */
  valueLabel: string;
  onChange: (accountId: string, accountName: string) => void;
  error?: string;
  disabled?: boolean;
}

/** A searchable account combobox: types a name, picks from live matches
 * (ListAccounts name_contains, debounced). There is no free-text account
 * option — a Contact's account_id must name a real Account in this tenant
 * (internal/domain/contact_integrity.go's requireContactAccount), so this
 * only ever commits an id chosen from the list. */
export function AccountPicker({ label, value, valueLabel, onChange, error, disabled }: AccountPickerProps) {
  const { crm } = useCrmClients();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const debouncedQuery = useDebouncedValue(query, 250);
  const inputId = useId();
  const listboxId = `${inputId}-listbox`;
  const containerRef = useRef<HTMLDivElement>(null);

  const { data, isFetching } = useQuery({
    queryKey: ["crm.listAccounts.picker", debouncedQuery],
    queryFn: () => crm.listAccounts({ nameContains: debouncedQuery, pageSize: 10 }),
    enabled: open,
  });

  const showingSelection = !open && value !== "";

  return (
    <div className="relative flex flex-col gap-1" ref={containerRef}>
      <label htmlFor={inputId} className="text-sm font-medium text-slate-700">
        {label}
      </label>
      <input
        id={inputId}
        role="combobox"
        aria-expanded={open}
        aria-controls={listboxId}
        aria-autocomplete="list"
        aria-invalid={!!error || undefined}
        autoComplete="off"
        disabled={disabled}
        value={showingSelection ? valueLabel : query}
        placeholder="Search accounts by name…"
        onFocus={() => {
          setOpen(true);
          setQuery("");
        }}
        onChange={(e) => {
          setQuery(e.target.value);
          if (value) onChange("", "");
        }}
        onBlur={() => {
          // Let a mousedown on an option register before we close and wipe query.
          setTimeout(() => setOpen(false), 150);
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape") setOpen(false);
        }}
        className={`rounded-md border px-3 py-2 text-sm text-slate-900 placeholder:text-slate-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand-600 disabled:bg-slate-50 disabled:text-slate-500 ${
          error ? "border-danger-500" : "border-slate-300"
        }`}
      />
      {error && (
        <p role="alert" className="text-xs text-danger-600">
          {error}
        </p>
      )}
      {open && (
        <ul
          id={listboxId}
          role="listbox"
          aria-label="Matching accounts"
          className="absolute top-full z-10 mt-1 max-h-56 w-full overflow-y-auto rounded-md border border-slate-200 bg-white py-1 shadow-lg"
        >
          {isFetching && <li className="px-3 py-2 text-sm text-slate-500">Searching…</li>}
          {!isFetching && (data?.accounts.length ?? 0) === 0 && (
            <li className="px-3 py-2 text-sm text-slate-500">No matching accounts.</li>
          )}
          {!isFetching &&
            data?.accounts.map((a) => (
              <li key={a.id} role="option" aria-selected={a.id === value}>
                <button
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => {
                    onChange(a.id, a.name);
                    setOpen(false);
                  }}
                  className="block w-full px-3 py-2 text-left text-sm text-slate-900 hover:bg-slate-50"
                >
                  {a.name}
                </button>
              </li>
            ))}
        </ul>
      )}
    </div>
  );
}
