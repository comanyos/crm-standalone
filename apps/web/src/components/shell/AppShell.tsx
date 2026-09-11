/* eslint-disable jsx-a11y/no-noninteractive-tabindex -- #main-content is the app's independently scrollable viewport. It must be keyboard-focusable both for the skip link and WCAG scrollable-region access. */
import { Suspense, useEffect, useRef, useState } from "react";
import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "../../auth/AuthProvider";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";

export function AppShell() {
  const [navOpen, setNavOpen] = useState(false);
  const main = useRef<HTMLElement>(null);
  const location = useLocation();
  const { hasRole } = useAuth();
  const superAdmin = hasRole("platform_admin");

  useEffect(() => {
    main.current?.scrollTo?.({ top: 0, left: 0 });
    if (window.scrollX !== 0 || window.scrollY !== 0) window.scrollTo({ top: 0, left: 0 });
  }, [location.pathname]);

  return (
    <div className="flex h-screen bg-slate-50">
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:rounded-lg focus:bg-white focus:px-4 focus:py-2 focus:text-slate-950 focus:shadow-md"
      >
        Skip to content
      </a>
      <Sidebar open={navOpen} onClose={() => setNavOpen(false)} />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Topbar onOpenNav={() => setNavOpen(true)} />
        {/* Narrower gutters on a phone: 1.5rem each side of a 375px screen is
            13% of the width spent on nothing. */}
        <main
          ref={main}
          id="main-content"
          tabIndex={0}
          className="flex-1 overflow-y-auto bg-slate-50 px-4 py-5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-brand-600 sm:px-6 sm:py-6 xl:px-8"
        >
          <div className="mx-auto w-full max-w-[1440px]">
            <Suspense fallback={<RouteLoading />}>
              {superAdmin && location.pathname !== "/admin" ? <Navigate to="/admin" replace /> : <Outlet />}
            </Suspense>
          </div>
        </main>
      </div>
    </div>
  );
}

function RouteLoading() {
  return (
    <div role="status" aria-live="polite" className="space-y-4">
      <div className="h-8 w-48 animate-pulse rounded-lg bg-slate-200" aria-hidden="true" />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4" aria-hidden="true">
        {Array.from({ length: 4 }, (_, index) => (
          <div key={index} className="h-20 animate-pulse rounded-xl border border-slate-200 bg-white" />
        ))}
      </div>
      <span className="sr-only">Loading page…</span>
    </div>
  );
}
