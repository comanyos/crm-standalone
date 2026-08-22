import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Task } from "@crm/contracts";
import type { BadgeTone } from "../../components/ui/Badge";

/** Presentation helpers for tasks, shared by the work list and the panel on
 * record pages so the two cannot describe the same task differently. */

export function statusLabel(status: string): string {
  if (status === "in_progress") return "In progress";
  if (!status) return "Open";
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function priorityTone(priority: string): BadgeTone {
  if (priority === "urgent") return "danger";
  if (priority === "high") return "warning";
  if (priority === "low") return "neutral";
  return "brand";
}

/** dueLabel renders a due date and says whether it is overdue.
 *
 * A task with no due date is never overdue — the same rule the backend
 * applies when filtering, restated here so the badge and the "Overdue" view
 * cannot disagree about which tasks are late. A completed task is never
 * overdue either: it is finished, and colouring it red would say the opposite
 * of what happened. */
export function dueLabel(task: Task): { text: string; overdue: boolean } {
  if (!task.dueAt) return { text: "—", overdue: false };
  const due = timestampDate(task.dueAt);
  const overdue = task.status !== "completed" && task.status !== "cancelled" && due.getTime() < Date.now();
  return { text: due.toLocaleDateString(), overdue };
}

/** Maps a subject back to the page that shows it, so a task saved from a
 * record page returns the reader to that record rather than to a list they
 * did not come from. */
export function subjectPath(subjectType: string, subjectId: string): string {
  const plural: Record<string, string> = {
    account: "accounts",
    contact: "contacts",
    lead: "leads",
    opportunity: "opportunities",
  };
  const segment = plural[subjectType];
  return segment ? `/${segment}/${subjectId}` : "/tasks";
}
