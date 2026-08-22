import { useCallback, useEffect, useRef, useState } from "react";
import { useBlocker, type Blocker, type BlockerFunction } from "react-router-dom";

export interface UnsavedChangesGuard {
  /** Pass to <UnsavedChangesPrompt blocker={...} />. */
  blocker: Blocker;
  /** Turns the guard off for the rest of this page's life. Call it right
   * before navigating away as the *result* of a successful save: the form
   * is still technically dirty at that point (nothing resets it), so
   * without this the success navigation would prompt to discard the very
   * changes that were just saved. */
  disarm: () => void;
}

/** Guards against losing unsaved form edits two ways: `useBlocker` (needs
 * the data router set up in App.tsx) catches in-app navigation — sidebar
 * links, Back, a Cancel button — and `beforeunload` catches leaving the tab
 * entirely (reload, close, typed URL). Both are inert while `isDirty` is
 * false, and re-armed automatically as soon as the form changes again after
 * a discard. Render <UnsavedChangesPrompt blocker={...} /> alongside this to
 * ask the user before the in-app navigation proceeds. */
export function useUnsavedChangesGuard(isDirty: boolean): UnsavedChangesGuard {
  // The blocker callback registered with the router is not re-read
  // synchronously when state changes, so both inputs it depends on are read
  // through refs: a save handler can disarm the guard and navigate in the
  // same tick and still have the navigation go through unblocked.
  const dirtyRef = useRef(isDirty);
  dirtyRef.current = isDirty;
  const disarmedRef = useRef(false);
  const [disarmed, setDisarmed] = useState(false);

  const disarm = useCallback(() => {
    disarmedRef.current = true;
    setDisarmed(true);
  }, []);

  const blocker = useBlocker(
    useCallback<BlockerFunction>(
      ({ currentLocation, nextLocation }) =>
        dirtyRef.current && !disarmedRef.current && currentLocation.pathname !== nextLocation.pathname,
      [],
    ),
  );

  useEffect(() => {
    if (!isDirty || disarmed) return;
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [isDirty, disarmed]);

  return { blocker, disarm };
}
