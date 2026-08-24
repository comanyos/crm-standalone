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
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
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

  // Bulk selection is page-local: it is not carried across a filter change
  // or a page turn, both of which already invalidate what "selected" means
  // (a different, or no longer visible, set of rows).
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirmingBulk, setConfirmingBulk] = useState<"complete" | "reopen" | null>(null);
  const toggleSelected = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

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

  // Reports one toast for the whole batch rather than one per task: the
  // caller asked for one bulk action, and 12 individual toasts would say
  // less than "10 of 12 completed" says in one line.
  const reportBulkOutcome = (results: { success: boolean }[], verb: string) => {
    const succeeded = results.filter((r) => r.success).length;
    const failed = results.length - succeeded;
    if (failed === 0) {
      toast.show({ tone: "success", message: `${succeeded} task${succeeded === 1 ? "" : "s"} ${verb}.` });
    } else {
      toast.show({
        tone: "warning",
        message: `${succeeded} ${verb}, ${failed} failed — a task may have changed or been removed since you selected it.`,
      });
    }
  };

  const bulkComplete = useMutation({
    mutationFn: () => crm.bulkCompleteTasks({ ids: Array.from(selected) }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      setConfirmingBulk(null);
      setSelected(new Set());
      reportBulkOutcome(res.results, "completed");
    },
    onError: (err) => {
      setConfirmingBulk(null);
      toast.show({ tone: "danger", message: mutationErrorMessage(err) });
    },
  });
  const bulkReopen = useMutation({
    mutationFn: () => crm.bulkReopenTasks({ ids: Array.from(selected) }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      setConfirmingBulk(null);
      setSelected(new Set());
      reportBulkOutcome(res.results, "reopened");
    },
    onError: (err) => {
      setConfirmingBulk(null);
      toast.show({ tone: "danger", message: mutationErrorMessage(err) });
    },
  });

  const columns: Column<Task>[] = [
    {
      key: "select",
      header: "Select", headerHidden: true,
      render: (task) =>
        canWrite ? (
          <input
            type="checkbox"
            checked={selected.has(task.id)}
            aria-label={`Select ${task.title}`}
            onClick={(event) => event.stopPropagation()}
            onChange={(event) => {
              event.stopPropagation();
              toggleSelected(task.id);
            }}
            className="h-4 w-4 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
          />
        ) : null,
    },
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
          {canWrite && selected.size > 0 && (
            <div className="mb-3 flex items-center gap-3 rounded-md border border-brand-200 bg-brand-50 px-3 py-2 text-sm">
              <span className="font-medium text-brand-800">
                {selected.size} task{selected.size === 1 ? "" : "s"} selected
              </span>
              <Button size="sm" variant="secondary" onClick={() => setConfirmingBulk("complete")}>
                Complete selected
              </Button>
              <Button size="sm" variant="secondary" onClick={() => setConfirmingBulk("reopen")}>
                Reopen selected
              </Button>
              <Button size="sm" variant="secondary" onClick={() => setSelected(new Set())}>
                Clear selection
              </Button>
            </div>
          )}
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
      <ConfirmDialog
        open={confirmingBulk !== null}
        title={confirmingBulk === "complete" ? `Complete ${selected.size} task(s)?` : `Reopen ${selected.size} task(s)?`}
        description="Each task is completed or reopened independently. If one has changed since you selected it, the rest still go through and you'll see how many succeeded."
        confirmLabel={confirmingBulk === "complete" ? "Complete tasks" : "Reopen tasks"}
        loading={bulkComplete.isPending || bulkReopen.isPending}
        onConfirm={() => (confirmingBulk === "complete" ? bulkComplete.mutate() : bulkReopen.mutate())}
        onCancel={() => setConfirmingBulk(null)}
      />
    </div>
  );
}

function statusTone(status: string): BadgeTone {
  if (status === "completed") return "success";
  if (status === "cancelled") return "neutral";
  if (status === "in_progress") return "brand";
  return "warning";
}
