import type { ReactNode } from "react";

export type BadgeTone = "neutral" | "brand" | "success" | "warning" | "danger";

const TONE_STYLES: Record<BadgeTone, string> = {
  neutral: "border-slate-200 bg-slate-100 text-slate-700",
  brand: "border-brand-200 bg-brand-50 text-brand-700",
  success: "border-success-200 bg-success-50 text-success-700",
  warning: "border-warning-200 bg-warning-50 text-warning-700",
  danger: "border-danger-200 bg-danger-50 text-danger-700",
};

export function Badge({ tone = "neutral", children }: { tone?: BadgeTone; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2.5 py-1 text-[11px] font-semibold ${TONE_STYLES[tone]}`}
    >
      {children}
    </span>
  );
}
