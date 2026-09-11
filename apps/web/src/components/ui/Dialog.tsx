import { useEffect, useId, useRef, type ReactNode } from "react";

interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
}

/** Built on the native <dialog> element: it owns focus trapping, Escape-to-
 * close, and the modal backdrop natively, which is more robust than
 * hand-rolled focus-trap JS and works the same in every modern browser. */
export function Dialog({ open, onClose, title, children, footer }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  // Unique per instance. The id was hardcoded to "dialog-title", so a screen
  // on which two dialogs are mounted — any list offering both "add" and
  // "edit" — gave every one of them the *first* dialog's accessible name,
  // because aria-labelledby resolves to the first element with that id in the
  // document. The heading said "Edit note" while the dialog announced itself
  // as "Add note".
  const titleId = useId();

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onCancel = (e: Event) => {
      // Let Escape close via our own state so the caller's onClose runs.
      e.preventDefault();
      onClose();
    };
    const onBackdropClick = (e: MouseEvent) => {
      if (e.target === el) onClose();
    };
    el.addEventListener("cancel", onCancel);
    el.addEventListener("click", onBackdropClick);
    return () => {
      el.removeEventListener("cancel", onCancel);
      el.removeEventListener("click", onBackdropClick);
    };
  }, [onClose]);

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      className="w-[calc(100%_-_2rem)] max-w-md rounded-2xl border border-slate-200 bg-white p-0 shadow-lg backdrop:bg-slate-950/55"
      onClose={onClose}
    >
      <div className="flex items-center justify-between border-b border-slate-200 px-5 py-4">
        <h2 id={titleId} className="text-lg font-semibold tracking-[-0.015em] text-slate-950">
          {title}
        </h2>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close dialog"
          className="flex h-8 w-8 items-center justify-center rounded-lg text-xl text-slate-500 hover:bg-slate-100 hover:text-slate-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
        >
          ×
        </button>
      </div>
      <div className="px-5 py-4 text-sm leading-6 text-slate-700">{children}</div>
      {footer && (
        <div className="flex flex-wrap justify-end gap-2 border-t border-slate-200 bg-slate-50 px-5 py-3.5">
          {footer}
        </div>
      )}
    </dialog>
  );
}
