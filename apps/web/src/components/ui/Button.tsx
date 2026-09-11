import { forwardRef, type ButtonHTMLAttributes } from "react";

export type ButtonVariant = "primary" | "secondary" | "danger" | "ghost";
export type ButtonSize = "sm" | "md";

const VARIANT_STYLES: Record<ButtonVariant, string> = {
  primary:
    "border border-slate-950 bg-slate-950 text-white shadow-sm hover:border-brand-700 hover:bg-brand-700 disabled:border-slate-300 disabled:bg-slate-300",
  secondary:
    "border border-slate-300 bg-white text-slate-900 hover:border-slate-500 hover:bg-slate-50 disabled:text-slate-400",
  danger:
    "border border-danger-700 bg-danger-700 text-white hover:bg-danger-600 disabled:border-danger-300 disabled:bg-danger-300",
  ghost: "border border-transparent bg-transparent text-slate-700 hover:bg-slate-100 disabled:text-slate-400",
};

const SIZE_STYLES: Record<ButtonSize, string> = {
  sm: "min-h-8 px-3 py-1.5 text-xs",
  md: "min-h-10 px-5 py-2 text-sm",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "primary", size = "md", loading = false, disabled, className = "", children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={`inline-flex items-center justify-center gap-2 rounded-lg font-semibold transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-600 disabled:cursor-not-allowed disabled:shadow-none ${VARIANT_STYLES[variant]} ${SIZE_STYLES[size]} ${className}`}
      {...rest}
    >
      {loading && (
        <span
          className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
          aria-hidden="true"
        />
      )}
      {children}
    </button>
  );
});
