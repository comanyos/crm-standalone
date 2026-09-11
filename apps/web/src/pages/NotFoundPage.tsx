import { Link } from "react-router-dom";

export default function NotFoundPage() {
  return (
    <main className="flex h-screen items-center justify-center bg-paper px-5">
      <div className="flex w-full max-w-md flex-col items-center gap-4 rounded-3xl border border-slate-200 bg-white p-8 text-center shadow-savvy">
        <p className="text-[10px] font-bold uppercase tracking-[0.22em] text-brand-600">Savvy CRM</p>
        <h1 className="font-display text-3xl font-normal tracking-[-0.035em] text-slate-950">Page not found</h1>
        <p className="max-w-sm text-sm leading-6 text-slate-500">
          The page you're looking for doesn't exist, or you may not have access to it.
        </p>
        <Link
          to="/"
          className="inline-flex min-h-10 items-center justify-center rounded-full border border-slate-950 bg-slate-950 px-5 py-2 text-sm font-semibold text-white shadow-savvy-sm hover:border-brand-700 hover:bg-brand-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-600"
        >
          Back to dashboard
        </Link>
      </div>
    </main>
  );
}
