import type { ReactNode } from "react";
import { Button } from "./Button";

interface StatePanelProps {
  icon?: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
}

function StatePanel({ icon, title, description, action }: StatePanelProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-slate-300 px-6 py-12 text-center">
      {icon}
      <h2 className="text-sm font-semibold text-slate-900">{title}</h2>
      {description && <p className="max-w-sm text-sm text-slate-500">{description}</p>}
      {action}
    </div>
  );
}

export function EmptyState({ title, description, action }: StatePanelProps) {
  return <StatePanel title={title} description={description} action={action} />;
}

/** A retryable error state — every screen that loads live data must be able
 * to show this rather than a blank page or an uncaught exception. */
export function ErrorState({
  title = "Something went wrong",
  description,
  onRetry,
}: {
  title?: string;
  description?: string;
  onRetry?: () => void;
}) {
  return (
    <StatePanel
      title={title}
      description={description}
      action={
        onRetry && (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            Retry
          </Button>
        )
      }
    />
  );
}

/** Shown when the backend returns permission-denied. Never states what the
 * user was denied access to beyond the current page's own label — record
 * existence must not leak across a permission boundary. */
export function ForbiddenState({ description }: { description?: string }) {
  return (
    <StatePanel
      title="You don't have access to this"
      description={description ?? "Ask an administrator if you believe this is a mistake."}
    />
  );
}

/** Shown for a missing or cross-tenant record. The message is identical
 * either way — a caller must not be able to tell "doesn't exist" apart from
 * "exists in a tenant you can't see" (both surface as ErrNotFound at the
 * store, connect.CodeNotFound at the RPC — see internal/api/convert.go). */
export function NotFoundState({
  title = "Not found",
  description = "This record doesn't exist, or you don't have access to it.",
  action,
}: {
  title?: string;
  description?: string;
  action?: ReactNode;
}) {
  return <StatePanel title={title} description={description} action={action} />;
}

export function UnavailableState({ onRetry }: { onRetry?: () => void }) {
  return (
    <StatePanel
      title="The service is temporarily unavailable"
      description="We couldn't reach the CRM backend. Check your connection and try again."
      action={
        onRetry && (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            Retry
          </Button>
        )
      }
    />
  );
}
