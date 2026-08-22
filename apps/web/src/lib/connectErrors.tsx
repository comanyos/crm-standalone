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

/** For a mutation the server refuses with an explanation written to be read.
 *
 * `mutationErrorMessage` replaces the server's text with a generic line, which
 * is right for a stale write — "someone else changed this, reload" is more
 * useful than whatever the storage layer said. It is wrong for a refusal whose
 * whole point is what it says: archiving a pipeline or stage that still holds
 * open deals names those deals and the step that unblocks it, and a caller
 * told only "reload the page" would reload it forever.
 *
 * rawMessage, not message: the latter is prefixed with the status code. */
export function refusalMessage(error: unknown): string {
  const err = ConnectError.from(error);
  if (err.code === Code.Aborted || err.code === Code.FailedPrecondition) {
    return err.rawMessage || mutationErrorMessage(error);
  }
  return mutationErrorMessage(error);
}
