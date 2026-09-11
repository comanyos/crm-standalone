import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";

/** The in-app inbox.
 *
 * It polls rather than subscribes. A rule's notification is written by a
 * background pass that no browser is waiting on, so there is nothing to
 * subscribe to without a streaming channel this deployment does not have;
 * a few seconds of latency on "a task was created for you" is the right trade
 * against a connection per open tab.
 *
 * The list is the caller's own — the RPC takes no recipient — so nothing
 * here decides whose notifications to ask for. */

const POLL_INTERVAL_MS = 10_000;

const SUBJECT_PATHS: Record<string, string> = {
  account: "/accounts",
  contact: "/contacts",
  lead: "/leads",
  opportunity: "/opportunities",
};

export function NotificationBell() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const container = useRef<HTMLDivElement>(null);

  const canRead = hasScope("crm:notification:read");
  const canWrite = hasScope("crm:notification:write");

  const query = useQuery({
    queryKey: ["crm.listNotifications"],
    queryFn: () => crm.listNotifications({ limit: 20 }),
    enabled: canRead,
    refetchInterval: POLL_INTERVAL_MS,
  });

  const markRead = useMutation({
    mutationFn: (id: string) => crm.markNotificationRead({ id }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["crm.listNotifications"] }),
  });

  // Clicking away closes the panel. Escape does too, because a panel that can
  // only be dismissed with the mouse is one a keyboard user is stuck inside.
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (!container.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  if (!canRead) return null;

  const notifications = query.data?.notifications ?? [];
  const unread = query.data?.unreadCount ?? 0;

  return (
    <div ref={container} className="relative">
      <button
        type="button"
        onClick={() => setOpen((current) => !current)}
        aria-expanded={open}
        // The count is in the accessible name rather than only in the badge:
        // "Notifications" alone tells a screen-reader user nothing about
        // whether it is worth opening.
        aria-label={unread > 0 ? `Notifications, ${unread} unread` : "Notifications"}
        className="relative flex h-10 w-10 items-center justify-center rounded-full text-slate-600 hover:bg-slate-100 hover:text-slate-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
      >
        <svg
          aria-hidden="true"
          viewBox="0 0 24 24"
          className="h-[18px] w-[18px]"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
        >
          <path d="M18 9a6 6 0 0 0-12 0c0 7-3 7-3 8h18c0-1-3-1-3-8Z" strokeLinejoin="round" />
          <path d="M10 20h4" strokeLinecap="round" />
        </svg>
        {unread > 0 && (
          <span
            aria-hidden="true"
            className="absolute -right-0.5 -top-0.5 min-w-4 rounded-full bg-danger-600 px-1 text-center text-[10px] font-semibold leading-4 text-white"
          >
            {unread > 9 ? "9+" : unread}
          </span>
        )}
      </button>

      {open && (
        <div className="absolute right-0 z-40 mt-2 w-[min(20rem,calc(100vw_-_2rem))] overflow-hidden rounded-xl border border-slate-200 bg-white shadow-md">
          <h2 className="border-b border-slate-100 px-4 py-3 text-sm font-semibold text-slate-950">
            Notifications
          </h2>
          {notifications.length === 0 && <p className="px-4 py-6 text-sm text-slate-500">Nothing new.</p>}
          <ul className="max-h-96 divide-y divide-slate-100 overflow-y-auto">
            {notifications.map((notification) => {
              const path = SUBJECT_PATHS[notification.subjectType];
              return (
                <li
                  key={notification.id}
                  className={notification.readAt ? "px-4 py-3" : "bg-brand-50/50 px-4 py-3"}
                >
                  <div className="text-sm font-medium text-slate-900">{notification.title}</div>
                  {notification.body && (
                    <div className="mt-0.5 text-xs text-slate-600">{notification.body}</div>
                  )}
                  <div className="mt-1 flex gap-3 text-xs">
                    {path && notification.subjectId && (
                      <Link
                        to={`${path}/${notification.subjectId}`}
                        onClick={() => setOpen(false)}
                        className="text-brand-700 underline"
                      >
                        Open the record
                      </Link>
                    )}
                    {canWrite && !notification.readAt && (
                      <button
                        type="button"
                        onClick={() => markRead.mutate(notification.id)}
                        className="text-slate-600 underline"
                      >
                        Mark read<span className="sr-only">: {notification.title}</span>
                      </button>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}
