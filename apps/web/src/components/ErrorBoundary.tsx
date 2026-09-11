import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./ui/Button";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

/** Route-level crash guard. React error boundaries must be class components
 * — there is no hook equivalent. Catches render errors in the page below it
 * (not errors in event handlers or async code, which callers must handle
 * themselves, e.g. via React Query's own error state). */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Unhandled render error", error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <main className="flex h-screen items-center justify-center bg-paper px-5" role="alert">
          <div className="flex w-full max-w-md flex-col items-center gap-4 rounded-3xl border border-slate-200 bg-white p-8 text-center shadow-savvy">
            <span aria-hidden="true" className="h-2 w-2 rounded-full bg-brand-600" />
            <h1 className="font-display text-3xl font-normal tracking-[-0.035em] text-slate-950">Something went wrong</h1>
            <p className="max-w-sm text-sm leading-6 text-slate-500">
              This page hit an unexpected error. Reloading usually fixes it; if it keeps happening, contact your
              administrator.
            </p>
            <Button onClick={() => window.location.reload()}>Reload</Button>
          </div>
        </main>
      );
    }
    return this.props.children;
  }
}
