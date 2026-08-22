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
        <div className="flex h-screen flex-col items-center justify-center gap-3 text-center" role="alert">
          <h1 className="text-lg font-semibold text-slate-900">Something went wrong</h1>
          <p className="max-w-sm text-sm text-slate-500">
            This page hit an unexpected error. Reloading usually fixes it; if it keeps happening, contact your
            administrator.
          </p>
          <Button onClick={() => window.location.reload()}>Reload</Button>
        </div>
      );
    }
    return this.props.children;
  }
}
