import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Task } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Table, type Column } from "../../components/ui/Table";
import { Pagination } from "../../components/ui/Pagination";
import { Button } from "../../components/ui/Button";
import { Badge, type BadgeTone } from "../../components/ui/Badge";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { dueLabel, priorityTone, statusLabel } from "./taskDisplay";

const PAGE_SIZE = 20;

/** The three questions a work list is actually opened to answer. "Mine" is
 * first because it is the one asked every morning; "All" exists so a manager
 * can see the team's, and is deliberately not the default — landing on every
 * open task in the organization buries the reader's own. */
type View = "mine" | "overdue" | "all";

const views: { id: View; label: string }[] = [
  { id: "mine", label: "My open tasks" },
  { id: "overdue", label: "Overdue" },
  { id: "all", label: "All tasks" },
];

export default function TasksListPage() {
  const { crm } = useCrmClients();
  const { hasScope, session } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [view, setView] = useState<View>("mine");
  const [history, setHistory] = useState<string[]>([""]);
  const currentToken = history[history.length - 1] ?? "";
  const canWrite = hasScope("crm:task:write");
  const me = session?.claims.sub ?? "";

  // The filter each view sends. Overdue is open_only plus due_before now,
  // which is exactly how the backend defines it — an undated task is never
  // overdue, so it never appears here.
  const filterFor = (v: View) => {
    if (v === "overdue") return { openOnly: true, dueBefore: timestampFromDate(new Date()) };
    if (v === "mine") return { openOnly: true, assigneeId: me };
    return {};
  };

  const queryKey = ["crm.listTasks", view, currentToken, me] as const;
  const { data, error, isPending, isPlaceholderData, refetch } = useQuery({
    queryKey,
    queryFn: () => crm.listTasks({ pageSize: PAGE_SIZE, pageToken: currentToken, ...filterFor(view) }),
    placeholderData: (previous) => previous,
  });

  // Completing from the list uses CompleteTask rather than a full update, so
  // ticking a box cannot lose a concurrent edit to the same task.
  const complete = useMutation({
    // Returns the updated task from either branch, so the two response
    // types do not have to be reconciled by the caller.
    mutationFn: async (task: Task) => {
      const result =
        task.status === "completed"
          ? await crm.reopenTask({ id: task.id })
          : await crm.completeTask({ id: task.id });
      return result.task;
    },
    onSuccess: (_result, task) => {
      queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      toast.show({
        tone: "success",
        message: task.status === "completed" ? "Task reopened." : "Task completed.",
      });
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });

  const columns: Column<Task>[] = [
    {
      key: "done",
      header: "Row actions", headerHidden: true,
      render: (task) =>
        canWrite ? (
          <input
            type="checkbox"
            checked={task.status === "completed"}
            aria-label={task.status === "completed" ? `Reopen ${task.title}` : `Complete ${task.title}`}
            disabled={complete.isPending}
            // The row itself navigates to the task; the checkbox must not.
            onClick={(event) => event.stopPropagation()}
            onChange={(event) => {
              event.stopPropagation();
              complete.mutate(task);
            }}
            className="h-4 w-4 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
          />
        ) : null,
    },
    {
      key: "title",
      rowHeader: true,
      header: "Task",
      render: (task) => (
        <span className={task.status === "completed" ? "text-slate-400 line-through" : "font-medium text-slate-900"}>
          {task.title}
        </span>
      ),
    },
    { key: "status", header: "Status", render: (task) => <Badge tone={statusTone(task.status)}>{statusLabel(task.status)}</Badge> },
    { key: "priority", header: "Priority", render: (task) => <Badge tone={priorityTone(task.priority)}>{task.priority || "normal"}</Badge> },
    {
      key: "due",
      header: "Due",
      render: (task) => {
        const due = dueLabel(task);
        return due.overdue ? <span className="font-medium text-rose-600">{due.text}</span> : <span>{due.text}</span>;
      },
    },
    { key: "assignee", header: "Assignee", render: (task) => task.assigneeId || <span className="text-slate-400">Unassigned</span> },
  ];

  return (
    <div>
      <PageHeader
        title="Tasks"
        actions={canWrite && <Button onClick={() => navigate("/tasks/new")}>New task</Button>}
      />

      <div className="mb-4 flex gap-2" role="tablist" aria-label="Task views">
        {views.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={view === item.id}
            onClick={() => {
              setView(item.id);
              // A page token belongs to the query that produced it; carrying
              // one across a filter change asks the server to resume a list
              // that no longer exists.
              setHistory([""]);
            }}
            className={
              view === item.id
                ? "rounded-md bg-brand-600 px-3 py-1.5 text-sm font-medium text-white"
                : "rounded-md border border-slate-300 px-3 py-1.5 text-sm text-slate-700 hover:bg-slate-50"
            }
          >
            {item.label}
          </button>
        ))}
      </div>

      {isPending && <SkeletonRows rows={6} />}
      {error &&
        renderQueryError(error, {
          onRetry: () => refetch(),
          forbiddenDescription: "You don't have permission to view tasks.",
        })}

      {data && data.tasks.length === 0 && (
        <EmptyState
          title={view === "overdue" ? "Nothing overdue" : view === "mine" ? "No open tasks assigned to you" : "No tasks yet"}
          description={
            view === "overdue"
              ? "Tasks appear here once their due date has passed."
              : "Tasks track what still needs doing, on a record or on their own."
          }
          action={canWrite && <Button onClick={() => navigate("/tasks/new")}>New task</Button>}
        />
      )}

      {data && data.tasks.length > 0 && (
        <div className={isPlaceholderData ? "opacity-60" : undefined}>
          <Table
            caption="Tasks"
            columns={columns}
            rows={data.tasks}
            getRowKey={(task) => task.id}
            onRowActivate={(task) => navigate(`/tasks/${task.id}/edit`)}
          />
          <div className="mt-3">
            <Pagination
              hasPrevious={history.length > 1}
              hasNext={!!data.nextPageToken}
              onPrevious={() => setHistory((items) => (items.length > 1 ? items.slice(0, -1) : items))}
              onNext={() => setHistory((items) => [...items, data.nextPageToken])}
            />
          </div>
        </div>
      )}
    </div>
  );
}

function statusTone(status: string): BadgeTone {
  if (status === "completed") return "success";
  if (status === "cancelled") return "neutral";
  if (status === "in_progress") return "brand";
  return "warning";
}
