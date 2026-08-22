import { ConnectError, Code } from "@connectrpc/connect";
import { ErrorState, ForbiddenState, NotFoundState, UnavailableState } from "../components/ui/StatePanel";

/** Maps a failed query's error onto the right state panel. Shared by every
 * screen so a permission-denied, not-found, or unreachable-backend response
 * always looks the same regardless of which RPC produced it. */
export function renderQueryError(
  error: unknown,
  opts: { onRetry?: () => void; forbiddenDescription?: string; notFoundDescription?: string } = {},
) {
  const err = ConnectError.from(error);
  switch (err.code) {
    case Code.PermissionDenied:
      return <ForbiddenState description={opts.forbiddenDescription} />;
    case Code.NotFound:
      return <NotFoundState description={opts.notFoundDescription} />;
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return <UnavailableState onRetry={opts.onRetry} />;
    default:
      return <ErrorState description={err.message} onRetry={opts.onRetry} />;
  }
}

/** A short, user-safe message for a failed mutation (create/update/delete),
 * for a toast rather than a full-page state panel. */
export function mutationErrorMessage(error: unknown): string {
  const err = ConnectError.from(error);
  switch (err.code) {
    case Code.PermissionDenied:
      return "You don't have permission to do that.";
    case Code.Aborted:
      return "This record was changed by someone else. Reload the page and try again.";
    case Code.InvalidArgument:
      return err.message;
    case Code.NotFound:
      return "This record no longer exists.";
    default:
      return "Something went wrong. Please try again.";
  }
}
