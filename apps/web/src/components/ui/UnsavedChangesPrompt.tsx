import type { Blocker } from "react-router-dom";
import { ConfirmDialog } from "./ConfirmDialog";

/** Pairs with useUnsavedChangesGuard: renders the "discard changes?"
 * confirmation when the blocker has intercepted an in-app navigation. */
export function UnsavedChangesPrompt({ blocker }: { blocker: Blocker }) {
  return (
    <ConfirmDialog
      open={blocker.state === "blocked"}
      title="Discard unsaved changes?"
      description="You have unsaved changes. Leaving this page now will lose them."
      confirmLabel="Discard changes"
      tone="danger"
      onConfirm={() => blocker.state === "blocked" && blocker.proceed()}
      onCancel={() => blocker.state === "blocked" && blocker.reset()}
    />
  );
}
