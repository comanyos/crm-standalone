import { NavLink } from "react-router-dom";

const NAV_ITEMS = [
  { to: "/", label: "Dashboard", end: true },
  { to: "/accounts", label: "Accounts" },
  { to: "/contacts", label: "Contacts" },
  { to: "/leads", label: "Leads" },
  { to: "/opportunities", label: "Opportunities" },
  { to: "/tasks", label: "Tasks" },
  { to: "/settings/pipelines", label: "Pipelines" },
];

export function Sidebar() {
  return (
    <nav aria-label="Primary" className="flex h-full w-56 flex-col border-r border-slate-200 bg-white">
      <div className="px-5 py-4 text-lg font-semibold text-brand-700">CRM</div>
      <ul className="flex-1 space-y-0.5 px-3">
        {NAV_ITEMS.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                `block rounded-md px-3 py-2 text-sm font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 ${
                  isActive
                    ? "bg-brand-50 text-brand-700"
                    : "text-slate-600 hover:bg-slate-100 hover:text-slate-900"
                }`
              }
            >
              {item.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
