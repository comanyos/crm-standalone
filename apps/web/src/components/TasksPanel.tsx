import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import type { Task } from "@crm/contracts";
import { Button } from "./ui/Button";
import { Badge } from "./ui/Badge";
import { SkeletonRows } from "./ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../lib/connectErrors";
import { useCrmClients } from "../lib/useCrmClients";
import { useAuth } from "../auth/AuthProvider";
import { useToast } from "./ui/Toast";
import { dueLabel, priorityTone, statusLabel } from "../features/tasks/taskDisplay";

/** The open tasks attached to one record.
 *
 * Only the open ones: a record page answers "what is outstanding here", and a
 * completed task belongs to the history rather than the working view. The
 * full list, including completed, is on /tasks.
 */
export function TasksPanel({
  subjectType,
  subjectId,
}: {
  subjectType: "account" | "contact" | "lead" | "opportunity";
  subjectId: string;
}) {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();

  const canWrite = hasScope("crm:task:write");
  const key = ["crm.listTasks", "subject", subjectType, subjectId] as const;
  const query = useQuery({
    queryKey: key,
    queryFn: () => crm.listTasks({ subjectType, subjectId, openOnly: true, pageSize: 50 }),
    enabled: !!subjectId,
  });

  const complete = useMutation({
    mutationFn: async (task: Task) => (await crm.completeTask({ id: task.id })).task,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      toast.show({ tone: "success", message: "Task completed." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  return (
    <section className="mt-4 max-w-2xl rounded-lg border border-slate-200 bg-white p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-slate-900">Open tasks</h2>
        {canWrite && (
          <Button
            size="sm"
            onClick={() => navigate(`/tasks/new?subjectType=${subjectType}&subjectId=${subjectId}`)}
          >
            Add task
          </Button>
        )}
      </div>

      {query.isPending && <SkeletonRows rows={2} />}
      {query.error &&
        renderQueryError(query.error, {
          onRetry: () => query.refetch(),
          forbiddenDescription: "You don't have permission to view tasks on this record.",
        })}
      {query.data?.tasks.length === 0 && <p className="text-sm text-slate-500">Nothing outstanding.</p>}

      {query.data?.tasks.length ? (
        <ul className="space-y-2">
          {query.data.tasks.map((task) => {
            const due = dueLabel(task);
            return (
              <li key={task.id} className="flex items-start justify-between gap-3 rounded-md border border-slate-200 p-3 text-sm">
                <div className="min-w-0">
                  <button
                    type="button"
                    onClick={() => navigate(`/tasks/${task.id}/edit`)}
                    className="text-left font-medium text-slate-900 hover:underline"
                  >
                    {task.title}
                  </button>
                  <p className="mt-1 flex flex-wrap items-center gap-2 text-xs text-slate-500">
                    <Badge tone={priorityTone(task.priority)}>{task.priority || "normal"}</Badge>
                    <span>{statusLabel(task.status)}</span>
                    <span className={due.overdue ? "font-medium text-rose-600" : undefined}>
                      {due.text === "—" ? "No due date" : `Due ${due.text}`}
                    </span>
                    {task.assigneeId ? <span>· {task.assigneeId}</span> : <span>· Unassigned</span>}
                  </p>
                </div>
                {canWrite && (
                  <Button
                    size="sm"
                    variant="secondary"
                    loading={complete.isPending}
                    onClick={() => complete.mutate(task)}
                  >
                    Complete
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      ) : null}
    </section>
  );
}
