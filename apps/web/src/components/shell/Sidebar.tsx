import { useEffect, useRef } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { useAuth } from "../../auth/AuthProvider";

interface NavItem {
  to: string;
  label: string;
  icon: NavIconName;
  end?: boolean;
  adminOnly?: boolean;
}

type NavIconName =
  | "dashboard"
  | "accounts"
  | "contacts"
  | "leads"
  | "opportunities"
  | "tasks"
  | "calendar"
  | "automation"
  | "admin"
  | "pipelines"
  | "fields"
  | "objects"
  | "tags"
  | "messages"
  | "capture"
  | "connections";

const NAV_ICON_PATHS: Record<NavIconName, string[]> = {
  dashboard: ["M4 4h6v6H4z", "M14 4h6v6h-6z", "M4 14h6v6H4z", "M14 14h6v6h-6z"],
  accounts: ["M4 21V7l8-4 8 4v14", "M8 10h.01M12 10h.01M16 10h.01M8 14h.01M12 14h.01M16 14h.01M9 21v-3h6v3"],
  contacts: [
    "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2",
    "M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z",
    "M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75",
  ],
  leads: [
    "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z",
    "M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10Z",
    "M12 13a1 1 0 1 0 0-2 1 1 0 0 0 0 2Z",
  ],
  opportunities: ["M4 19 10 13l4 4 6-9", "M15 8h5v5"],
  tasks: ["M9 11l3 3L22 4", "M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"],
  calendar: ["M3 5h18v16H3z", "M16 3v4M8 3v4M3 10h18"],
  automation: ["M13 2 3 14h8l-1 8 11-13h-8z"],
  admin: ["M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z", "m9 12 2 2 4-4"],
  pipelines: ["M6 3v12a3 3 0 0 0 3 3h9", "m15 15 3 3-3 3", "M6 9h8a3 3 0 0 0 3-3V3"],
  fields: ["M4 6h16M4 12h16M4 18h16", "M8 4v4M16 10v4M10 16v4"],
  objects: ["M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z"],
  tags: ["M20 13 13 20 3 10V3h7Z", "M7.5 7.5h.01"],
  messages: ["M4 5h16v12H8l-4 4Z", "M8 9h8M8 13h5"],
  capture: ["M4 4h16v16H4z", "M8 2v4M16 2v4M8 12h8M12 8v8"],
  connections: [
    "M10 13a5 5 0 0 0 7.54.54l2-2a5 5 0 0 0-7.07-7.07l-1.15 1.15",
    "M14 11a5 5 0 0 0-7.54-.54l-2 2a5 5 0 0 0 7.07 7.07l1.14-1.14",
  ],
};

function NavIcon({ name }: { name: NavIconName }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className="h-[17px] w-[17px]"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.65"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {NAV_ICON_PATHS[name].map((path) => (
        <path key={path} d={path} />
      ))}
    </svg>
  );
}

interface NavGroup {
  label: string;
  items: NavItem[];
}

const NAV_GROUPS: NavGroup[] = [
  {
    label: "Workspace",
    items: [
      { to: "/", label: "Dashboard", icon: "dashboard", end: true },
      { to: "/accounts", label: "Accounts", icon: "accounts" },
      { to: "/contacts", label: "Contacts", icon: "contacts" },
      { to: "/leads", label: "Leads", icon: "leads" },
      { to: "/opportunities", label: "Opportunities", icon: "opportunities" },
    ],
  },
  {
    label: "Follow-up",
    items: [
      { to: "/tasks", label: "Tasks", icon: "tasks" },
      { to: "/calendar", label: "Calendar", icon: "calendar" },
      { to: "/settings/automation", label: "Automation", icon: "automation" },
    ],
  },
  {
    label: "Manage",
    items: [
      { to: "/admin", label: "Administration", icon: "admin", adminOnly: true },
      { to: "/settings/pipelines", label: "Pipelines", icon: "pipelines" },
      { to: "/settings/custom-fields", label: "Custom fields", icon: "fields" },
      { to: "/settings/custom-objects", label: "Custom objects", icon: "objects" },
      { to: "/settings/tags", label: "Tags", icon: "tags" },
      { to: "/settings/message-templates", label: "Message templates", icon: "messages" },
      { to: "/settings/lead-capture", label: "Lead capture", icon: "capture" },
      { to: "/settings/integrations", label: "Connected accounts", icon: "connections" },
    ],
  },
];

const SUPER_ADMIN_GROUPS: NavGroup[] = [
  {
    label: "Platform",
    items: [{ to: "/admin", label: "Super Admin", icon: "admin" }],
  },
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
  const { hasRole, session } = useAuth();
  const platformAdmin = hasRole("platform_admin");
  const navigationGroups = platformAdmin ? SUPER_ADMIN_GROUPS : NAV_GROUPS;
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
          className="fixed inset-0 z-30 bg-slate-950/65 backdrop-blur-sm lg:hidden"
        />
      )}
      <nav
        ref={panel}
        tabIndex={-1}
        aria-label="Primary"
        // `hidden` rather than a transform when closed: an off-screen nav that
        // is still in the tree is reachable by Tab and readable by a screen
        // reader, which is how invisible focus traps happen.
        className={`z-40 flex h-full w-60 shrink-0 flex-col border-r border-white/10 bg-slate-950 text-white lg:static lg:flex ${
          open ? "fixed inset-y-0 left-0 shadow-savvy" : "hidden"
        }`}
      >
        <div className="flex items-start justify-between px-4 pb-4 pt-4">
          <div>
            <span
              className="inline-flex items-center gap-2 rounded-lg bg-white px-2.5 py-2"
              aria-label="Savvy CRM"
            >
              <img src="/savvy-wordmark.png" alt="Savvy" className="h-[18px] w-auto" />
              <span aria-hidden="true" className="h-[18px] w-px bg-slate-300" />
              <span className="text-[11px] font-bold tracking-[0.16em] text-slate-950">CRM</span>
            </span>
            <p className="mt-2.5 max-w-[11rem] truncate pl-0.5 text-xs text-white/55">
              {platformAdmin ? "Super Admin console" : (session?.organization?.name ?? "Sales workspace")}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-full p-2 text-white/65 hover:bg-white/10 hover:text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-400 lg:hidden"
          >
            <span className="sr-only">Close navigation</span>
            <svg
              aria-hidden="true"
              viewBox="0 0 24 24"
              className="h-5 w-5"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.8"
            >
              <path d="m6 6 12 12M18 6 6 18" strokeLinecap="round" />
            </svg>
          </button>
        </div>
        <div className="flex-1 space-y-4 overflow-y-auto px-2.5 pb-4">
          {navigationGroups.map((group) => {
            const visibleItems = group.items.filter(
              (item) => !item.adminOnly || platformAdmin || hasRole("crm_admin"),
            );
            if (visibleItems.length === 0) return null;
            return (
              <section
                key={group.label}
                aria-labelledby={`nav-${group.label.toLowerCase().replace(/[^a-z]+/g, "-")}`}
              >
                <h2
                  id={`nav-${group.label.toLowerCase().replace(/[^a-z]+/g, "-")}`}
                  className="mb-1 px-2.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-white/45"
                >
                  {group.label}
                </h2>
                <ul className="space-y-px">
                  {visibleItems.map((item) => (
                    <li key={item.to}>
                      <NavLink
                        to={item.to}
                        end={item.end}
                        className={({ isActive }) =>
                          `group flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-[13px] font-medium transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-400 ${
                            isActive
                              ? "bg-white/10 text-white"
                              : "text-white/70 hover:bg-white/10 hover:text-white"
                          }`
                        }
                      >
                        {({ isActive }) => (
                          <>
                            <span
                              className={
                                isActive ? "text-brand-400" : "text-white/40 group-hover:text-white/70"
                              }
                            >
                              <NavIcon name={item.icon} />
                            </span>
                            {item.label}
                          </>
                        )}
                      </NavLink>
                    </li>
                  ))}
                </ul>
              </section>
            );
          })}
        </div>
        <div className="border-t border-white/10 px-4 py-3 text-[11px] leading-5 text-white/45">
          {platformAdmin ? "Protected platform access." : "Every lead. One clear next step."}
        </div>
      </nav>
    </>
  );
}
