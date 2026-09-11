import type { ReactNode } from "react";

export interface Column<T> {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  /** Right-aligns numeric/amount columns. */
  align?: "left" | "right";
  /** Renders the header for screen readers only, for a column whose purpose
   * is obvious to a sighted reader — a row-actions column, a status badge.
   *
   * The header text is still required. A screen reader announcing a cell
   * names its column, so an empty <th> leaves those cells with no context at
   * all; axe reports it as `empty-table-header`. Hiding the label visually is
   * a design choice, omitting it is a defect. */
  headerHidden?: boolean;
  /** Marks the column that names the row. It becomes the row's
   * `<th scope="row">` — so a cell read on its own says which record it
   * belongs to — and, on an activatable table, holds the control that opens
   * it.
   *
   * Defaults to the first column, which is right for most tables and wrong
   * for any whose first column is itself a control: the tasks table leads
   * with a completion checkbox, and wrapping that in the activating button
   * nests one control inside another. Mark the naming column explicitly
   * there. */
  rowHeader?: boolean;
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
  const marked = columns.findIndex((col) => col.rowHeader);
  const headerIndex = marked === -1 ? 0 : marked;

  return (
    <div className="relative max-w-full overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm">
      <table className="min-w-full divide-y divide-slate-200 text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead className="bg-slate-100/70">
          <tr>
            {columns.map((col) => (
              <th
                key={col.key}
                scope="col"
                className={`px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-slate-600 ${
                  col.align === "right" ? "text-right" : "text-left"
                }`}
              >
                {col.headerHidden ? <span className="sr-only">{col.header}</span> : col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 bg-white">
          {rows.map((row) => {
            const key = getRowKey(row);
            const interactive = !!onRowActivate;
            return (
              // The row keeps a click for pointer users, but it is not itself
              // a control. It used to carry role="button" and tabIndex, which
              // took the <tr> out of the table: its cells stopped being cells,
              // so a screen reader lost the row/column relationship, and any
              // real control inside the row — the tasks table has a checkbox
              // and two buttons — became a control nested inside a control,
              // which axe reports as `nested-interactive` (serious).
              //
              // The keyboard and screen-reader path is now the button in the
              // row header cell below, which is a real control with a real
              // name.
              <tr
                key={key}
                onClick={interactive ? () => onRowActivate(row) : undefined}
                className={interactive ? "cursor-pointer transition-colors hover:bg-brand-50/45" : undefined}
              >
                {columns.map((col, index) => {
                  const content = col.render(row);
                  const className = `px-4 py-3 text-slate-800 ${col.align === "right" ? "text-right" : ""}`;
                  if (index === headerIndex) {
                    return (
                      <th key={col.key} scope="row" className={`${className} font-normal`}>
                        {interactive ? (
                          <button
                            type="button"
                            // The row's own click would fire again as this
                            // bubbles; harmless but for one navigation, not two.
                            onClick={(event) => {
                              event.stopPropagation();
                              onRowActivate(row);
                            }}
                            className="text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
                          >
                            {content}
                          </button>
                        ) : (
                          content
                        )}
                      </th>
                    );
                  }
                  return (
                    <td key={col.key} className={className}>
                      {content}
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
