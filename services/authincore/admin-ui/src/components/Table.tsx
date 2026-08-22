interface Column<T> { key: string; header: string; render?: (row: T) => React.ReactNode }

export default function Table<T extends object>({
  columns, rows, onRowClick,
}: {
  columns: Column<T>[]
  rows: T[]
  onRowClick?: (row: T) => void
}) {
  return (
    <div className="overflow-x-auto rounded-lg border border-gray-200 bg-white">
      <table className="min-w-full divide-y divide-gray-200 text-sm">
        <thead className="bg-gray-50">
          <tr>
            {columns.map(c => (
              <th key={c.key} className="px-4 py-3 text-left font-medium text-gray-500 uppercase tracking-wide text-xs">
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100">
          {rows.length === 0 && (
            <tr><td colSpan={columns.length} className="px-4 py-6 text-center text-gray-400">No records</td></tr>
          )}
          {rows.map((row, i) => (
            <tr
              key={i}
              onClick={() => onRowClick?.(row)}
              className={onRowClick ? 'cursor-pointer hover:bg-gray-50' : ''}
            >
              {columns.map(c => (
                <td key={c.key} className="px-4 py-3 text-gray-700 whitespace-nowrap">
                  {c.render ? c.render(row) : String((row as Record<string, unknown>)[c.key] ?? '')}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
