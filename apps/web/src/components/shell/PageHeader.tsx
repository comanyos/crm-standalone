import { Link } from "react-router-dom";
import type { ReactNode } from "react";

export interface Breadcrumb {
  label: string;
  to?: string;
}

export function PageHeader({
  title,
  breadcrumbs,
  actions,
}: {
  title: string;
  breadcrumbs?: Breadcrumb[];
  actions?: ReactNode;
}) {
  return (
    <div className="mb-6 flex items-start justify-between gap-4">
      <div>
        {breadcrumbs && breadcrumbs.length > 0 && (
          <nav aria-label="Breadcrumb" className="mb-1 text-xs text-slate-500">
            <ol className="flex items-center gap-1">
              {breadcrumbs.map((b, i) => (
                <li key={i} className="flex items-center gap-1">
                  {i > 0 && <span aria-hidden="true">/</span>}
                  {b.to ? (
                    <Link to={b.to} className="hover:text-brand-700 hover:underline">
                      {b.label}
                    </Link>
                  ) : (
                    <span aria-current="page">{b.label}</span>
                  )}
                </li>
              ))}
            </ol>
          </nav>
        )}
        <h1 className="text-xl font-semibold text-slate-900">{title}</h1>
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}
