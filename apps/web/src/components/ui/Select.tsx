import { forwardRef, useId, type SelectHTMLAttributes } from "react";

export interface SelectOption {
  value: string;
  label: string;
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "children"> {
  label: string;
  options: SelectOption[];
  /** Rendered as the first, disabled-looking option when the field allows
   * "no selection" (e.g. an optional filter). */
  placeholder?: string;
  error?: string;
  hint?: string;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { label, options, placeholder, error, hint, id, className = "", ...rest },
  ref,
) {
  const autoId = useId();
  const selectId = id ?? autoId;
  const hintId = hint ? `${selectId}-hint` : undefined;
  const errorId = error ? `${selectId}-error` : undefined;

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={selectId} className="text-sm font-semibold text-slate-700">
        {label}
      </label>
      <select
        ref={ref}
        id={selectId}
        aria-invalid={!!error || undefined}
        aria-describedby={[hintId, errorId].filter(Boolean).join(" ") || undefined}
        className={`min-h-10 rounded-lg border bg-white px-3 py-2 text-sm text-slate-900 shadow-sm hover:border-slate-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand-600 disabled:bg-slate-100 disabled:text-slate-500 ${
          error ? "border-danger-500" : "border-slate-300"
        } ${className}`}
        {...rest}
      >
        {placeholder !== undefined && <option value="">{placeholder}</option>}
        {options.map((opt) => (
          <option key={opt.value} value={opt.value}>
            {opt.label}
          </option>
        ))}
      </select>
      {hint && !error && (
        <p id={hintId} className="text-xs text-slate-500">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} role="alert" className="text-xs text-danger-600">
          {error}
        </p>
      )}
    </div>
  );
});
