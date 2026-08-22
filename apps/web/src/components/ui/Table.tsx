import type { ReactNode } from "react";

export interface Column<T> {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  /** Right-aligns numeric/amount columns. */
  align?: "left" | "right";
}

interface TableProps<T> {
  caption: string;
  columns: Column<T>[];
  rows: T[];
  getRowKey: (row: T) => string;
  /** Optional: makes each row a link/button, e.g. to open a detail panel. */
  onRowActivate?: (row: T) => void;
}

export function Table<T>({ caption, columns, rows, getRowKey, onRowActivate }: TableProps<T>) {
  return (
    <div className="overflow-x-auto rounded-lg border border-slate-200">
      <table className="min-w-full divide-y divide-slate-200 text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead className="bg-slate-50">
          <tr>
            {columns.map((col) => (
              <th
                key={col.key}
                scope="col"
                className={`px-4 py-2 text-xs font-semibold uppercase tracking-wide text-slate-500 ${
                  col.align === "right" ? "text-right" : "text-left"
                }`}
              >
                {col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 bg-white">
          {rows.map((row) => {
            const key = getRowKey(row);
            const interactive = !!onRowActivate;
            return (
              <tr
                key={key}
                tabIndex={interactive ? 0 : undefined}
                role={interactive ? "button" : undefined}
                onClick={interactive ? () => onRowActivate(row) : undefined}
                onKeyDown={
                  interactive
                    ? (e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          onRowActivate(row);
                        }
                      }
                    : undefined
                }
                className={
                  interactive
                    ? "cursor-pointer hover:bg-slate-50 focus-visible:bg-slate-50 focus-visible:outline-none"
                    : undefined
                }
              >
                {columns.map((col) => (
                  <td
                    key={col.key}
                    className={`px-4 py-2 text-slate-800 ${col.align === "right" ? "text-right" : ""}`}
                  >
                    {col.render(row)}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
