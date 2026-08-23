import { useEffect, useRef } from "react";
import { NavLink, useLocation } from "react-router-dom";

const NAV_ITEMS = [
  { to: "/", label: "Dashboard", end: true },
  { to: "/accounts", label: "Accounts" },
  { to: "/contacts", label: "Contacts" },
  { to: "/leads", label: "Leads" },
  { to: "/opportunities", label: "Opportunities" },
  { to: "/tasks", label: "Tasks" },
  { to: "/settings/pipelines", label: "Pipelines" },
  { to: "/settings/custom-fields", label: "Custom fields" },
  { to: "/settings/custom-objects", label: "Custom objects" },
];

interface SidebarProps {
  /** Whether the small-screen drawer is showing. Ignored from `lg` up, where
   * the navigation is a permanent column and cannot be dismissed. */
  open: boolean;
  onClose: () => void;
}

/** Primary navigation.
 *
 * From `lg` up this is the permanent left column it has always been. Below
 * that it is an off-canvas drawer, because a fixed 14rem column on a 375px
 * phone leaves about 150px for the page — every table, form and board was
 * being read through a slot.
 *
 * One <nav> serves both, rather than a duplicate for small screens: two would
 * put every link in the accessibility tree twice, and "Accounts" appearing
 * twice to a screen-reader user is a worse bug than the one being fixed. */
export function Sidebar({ open, onClose }: SidebarProps) {
  const location = useLocation();
  const panel = useRef<HTMLElement>(null);

  // Following a link inside the drawer navigates behind it; without this the
  // new page sits under an open overlay.
  useEffect(() => {
    onClose();
    // Only on an actual navigation — onClose identity must not re-trigger it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    // Move focus into the drawer so the next Tab lands on a nav link rather
    // than continuing from the button that is now behind an overlay.
    panel.current?.focus();
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);

  return (
    <>
      {/* The scrim only exists while the drawer does. aria-hidden because the
          drawer's own Close button is the accessible way out. */}
      {open && (
        <div
          aria-hidden="true"
          onClick={onClose}
          className="fixed inset-0 z-30 bg-slate-900/40 lg:hidden"
        />
      )}
      <nav
        ref={panel}
        tabIndex={-1}
        aria-label="Primary"
        // `hidden` rather than a transform when closed: an off-screen nav that
        // is still in the tree is reachable by Tab and readable by a screen
        // reader, which is how invisible focus traps happen.
        className={`z-40 flex h-full w-56 shrink-0 flex-col border-r border-slate-200 bg-white lg:static lg:flex ${
          open ? "fixed inset-y-0 left-0 shadow-xl" : "hidden"
        }`}
      >
        <div className="flex items-center justify-between px-5 py-4">
          <span className="text-lg font-semibold text-brand-700">CRM</span>
          <button
            type="button"
            onClick={onClose}
            className="rounded p-1 text-slate-500 hover:bg-slate-100 hover:text-slate-900 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 lg:hidden"
          >
            <span className="sr-only">Close navigation</span>
            <span aria-hidden="true">✕</span>
          </button>
        </div>
        <ul className="flex-1 space-y-0.5 overflow-y-auto px-3 pb-4">
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
    </>
  );
}
