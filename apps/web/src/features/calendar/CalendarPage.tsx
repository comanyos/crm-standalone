import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Task } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { downloadTaskIcs } from "./ics";

const weekdays = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
type CalendarView = "month" | "week" | "day";
type StatusFilter = "active" | "open" | "in_progress" | "completed" | "cancelled" | "all";

function dayKey(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function monthDays(month: Date): Date[] {
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  const start = new Date(first);
  start.setDate(start.getDate() - start.getDay());
  return Array.from({ length: 42 }, (_, index) => {
    const day = new Date(start);
    day.setDate(start.getDate() + index);
    return day;
  });
}

function addDays(date: Date, amount: number): Date {
  const next = new Date(date);
  next.setDate(next.getDate() + amount);
  return next;
}

function weekDays(date: Date): Date[] {
  const first = new Date(date);
  first.setDate(first.getDate() - first.getDay());
  return Array.from({ length: 7 }, (_, index) => addDays(first, index));
}

function weekTitle(days: Date[]): string {
  const first = days[0];
  const last = days[6];
  const firstLabel = first.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const lastLabel = last.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
  return `${firstLabel} – ${lastLabel}`;
}

function dueDateTimestamp(date: Date) {
  return timestampFromDate(new Date(date.getFullYear(), date.getMonth(), date.getDate(), 23, 59, 59));
}

function priorityClass(priority: string): string {
  if (priority === "urgent") return "bg-rose-50 text-rose-900";
  if (priority === "high") return "bg-amber-50 text-amber-900";
  if (priority === "low") return "bg-slate-100 text-slate-700";
  return "bg-brand-50 text-brand-900";
}

/** Calendar is the first provider-neutral scheduling surface. Tasks remain
 * the authoritative reminder record; calendar connections are deliberately
 * deferred until users can export a portable ICS representation first. */
export default function CalendarPage() {
  const { crm } = useCrmClients();
  const { hasScope, session } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [view, setView] = useState<CalendarView>("month");
  const [focusDate, setFocusDate] = useState(() => new Date());
  const [newTaskDate, setNewTaskDate] = useState<Date | null>(null);
  const [newTaskTitle, setNewTaskTitle] = useState("");
  const [newTaskDescription, setNewTaskDescription] = useState("");
  const [assigneeFilter, setAssigneeFilter] = useState("all");
  const [priorityFilter, setPriorityFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("active");
  const canWrite = hasScope("crm:task:write");
  const { data, error, isPending, refetch } = useQuery({
    queryKey: ["crm.listTasks", "calendar"],
    // A calendar has to see scheduled work beyond the first work-list page.
    // Range queries and server-side calendar pagination belong with a future
    // event model; this bounded initial view intentionally asks for 500.
    queryFn: () => crm.listTasks({ pageSize: 500 }),
  });
  const createTask = useMutation({
    mutationFn: () => {
      if (!newTaskDate) throw new Error("Choose a date for this task.");
      return crm.createTask({
        task: {
          title: newTaskTitle.trim(),
          description: newTaskDescription.trim(),
          status: "open",
          priority: "normal",
          assigneeId: session?.claims.sub ?? "",
          dueAt: dueDateTimestamp(newTaskDate),
          subjectType: "",
          subjectId: "",
        },
      });
    },
    onSuccess: () => {
      setNewTaskDate(null);
      setNewTaskTitle("");
      setNewTaskDescription("");
      queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      toast.show({ tone: "success", message: "Task created." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const assigneeOptions = useMemo(() => {
    const assignees = [...new Set((data?.tasks ?? []).map((task) => task.assigneeId).filter(Boolean))].sort();
    return [
      { value: "all", label: "All assignees" },
      ...(session?.claims.sub ? [{ value: "mine", label: "My tasks" }] : []),
      { value: "unassigned", label: "Unassigned" },
      ...assignees.map((id) => ({ value: id, label: id })),
    ];
  }, [data, session?.claims.sub]);
  const filteredTasks = useMemo(() => {
    return (data?.tasks ?? []).filter((task) => {
      const status = task.status || "open";
      const statusMatches =
        statusFilter === "all" ||
        (statusFilter === "active" ? status === "open" || status === "in_progress" : status === statusFilter);
      const priorityMatches = priorityFilter === "all" || (task.priority || "normal") === priorityFilter;
      const assigneeMatches =
        assigneeFilter === "all" ||
        (assigneeFilter === "mine"
          ? task.assigneeId === session?.claims.sub
          : assigneeFilter === "unassigned"
            ? !task.assigneeId
            : task.assigneeId === assigneeFilter);
      return statusMatches && priorityMatches && assigneeMatches;
    });
  }, [assigneeFilter, data, priorityFilter, session?.claims.sub, statusFilter]);
  const scheduled = useMemo(() => {
    const grouped = new Map<string, Task[]>();
    for (const task of filteredTasks) {
      if (!task.dueAt) continue;
      const key = dayKey(timestampDate(task.dueAt));
      grouped.set(key, [...(grouped.get(key) ?? []), task]);
    }
    return grouped;
  }, [filteredTasks]);
  const month = useMemo(() => new Date(focusDate.getFullYear(), focusDate.getMonth(), 1), [focusDate]);
  const days = useMemo(() => monthDays(month), [month]);
  const week = useMemo(() => weekDays(focusDate), [focusDate]);
  const today = dayKey(new Date());
  const title =
    view === "month"
      ? month.toLocaleDateString(undefined, { month: "long", year: "numeric" })
      : view === "week"
        ? weekTitle(week)
        : focusDate.toLocaleDateString(undefined, {
            weekday: "long",
            month: "long",
            day: "numeric",
            year: "numeric",
          });
  const visibleDays = view === "month" ? days : view === "week" ? week : [focusDate];
  const dateStep = view === "month" ? 30 : view === "week" ? 7 : 1;
  const periodName = view === "month" ? "month" : view;
  const selectedDateLabel = newTaskDate?.toLocaleDateString(undefined, {
    weekday: "long",
    month: "long",
    day: "numeric",
    year: "numeric",
  });
  const openNewTask = (date: Date) => {
    setNewTaskDate(new Date(date));
    setNewTaskTitle("");
    setNewTaskDescription("");
  };
  const closeNewTask = () => {
    if (!createTask.isPending) setNewTaskDate(null);
  };

  return (
    <div>
      <PageHeader
        title="Calendar"
        actions={canWrite && <Button onClick={() => openNewTask(new Date())}>New task</Button>}
      />
      <p className="mb-5 max-w-3xl text-sm text-slate-600">
        Scheduled tasks appear on their due date. Download an event to add it to Google Calendar or Outlook;
        direct calendar connections come later.
      </p>
      <div className="mb-4 flex flex-wrap items-end gap-3" aria-label="Calendar task filters">
        <Select
          label="Assignee"
          value={assigneeFilter}
          onChange={(event) => setAssigneeFilter(event.target.value)}
          options={assigneeOptions}
        />
        <Select
          label="Priority"
          value={priorityFilter}
          onChange={(event) => setPriorityFilter(event.target.value)}
          options={[
            { value: "all", label: "All priorities" },
            { value: "urgent", label: "Urgent" },
            { value: "high", label: "High" },
            { value: "normal", label: "Normal" },
            { value: "low", label: "Low" },
          ]}
        />
        <Select
          label="Status"
          value={statusFilter}
          onChange={(event) => setStatusFilter(event.target.value as StatusFilter)}
          options={[
            { value: "active", label: "Open & in progress" },
            { value: "open", label: "Open" },
            { value: "in_progress", label: "In progress" },
            { value: "completed", label: "Completed" },
            { value: "cancelled", label: "Cancelled" },
            { value: "all", label: "All statuses" },
          ]}
        />
      </div>
      <div
        className="mb-4 flex flex-wrap gap-x-3 gap-y-1 text-xs text-slate-600"
        aria-label="Task priority colors"
      >
        <span>
          <span className="mr-1 inline-block h-2 w-2 rounded-full bg-rose-400" />
          Urgent
        </span>
        <span>
          <span className="mr-1 inline-block h-2 w-2 rounded-full bg-amber-400" />
          High
        </span>
        <span>
          <span className="mr-1 inline-block h-2 w-2 rounded-full bg-brand-400" />
          Normal
        </span>
        <span>
          <span className="mr-1 inline-block h-2 w-2 rounded-full bg-slate-400" />
          Low
        </span>
      </div>

      <div className="mb-4 flex flex-wrap items-center gap-2" aria-label="Calendar controls">
        <Button
          variant="secondary"
          onClick={() =>
            setFocusDate((value) =>
              view === "month"
                ? new Date(value.getFullYear(), value.getMonth() - 1, 1)
                : addDays(value, -dateStep),
            )
          }
        >
          Previous {periodName}
        </Button>
        <Button variant="secondary" onClick={() => setFocusDate(new Date())}>
          Today
        </Button>
        <Button
          variant="secondary"
          onClick={() =>
            setFocusDate((value) =>
              view === "month"
                ? new Date(value.getFullYear(), value.getMonth() + 1, 1)
                : addDays(value, dateStep),
            )
          }
        >
          Next {periodName}
        </Button>
        <h2 className="ml-2 text-lg font-semibold text-slate-900" aria-live="polite">
          {title}
        </h2>
        <div className="ml-auto flex gap-1" role="group" aria-label="Calendar view">
          {(["month", "week", "day"] as const).map((option) => (
            <Button
              key={option}
              size="sm"
              variant={view === option ? "primary" : "secondary"}
              aria-pressed={view === option}
              onClick={() => setView(option)}
            >
              {option[0].toUpperCase() + option.slice(1)}
            </Button>
          ))}
        </div>
      </div>

      {isPending && <SkeletonRows rows={6} />}
      {error &&
        renderQueryError(error, {
          onRetry: () => refetch(),
          forbiddenDescription: "You don't have permission to view scheduled tasks.",
        })}
      {data && scheduled.size === 0 && (
        <EmptyState
          title="No matching scheduled tasks"
          description="Change a filter or give a task a due date to see it here. You can then download it for Google Calendar or Outlook."
          action={canWrite && <Button onClick={() => openNewTask(new Date())}>New task</Button>}
        />
      )}
      {data && (
        <section
          aria-label={`${title} ${view} calendar`}
          className="overflow-x-auto rounded-lg border border-slate-200 bg-white"
        >
          {view !== "day" && (
            <div className="grid min-w-[700px] grid-cols-7 border-b border-slate-200 bg-slate-50">
              {weekdays.map((weekday) => (
                <div
                  key={weekday}
                  className="px-3 py-2 text-center text-xs font-semibold uppercase tracking-wide text-slate-600"
                >
                  {weekday}
                </div>
              ))}
            </div>
          )}
          <div className={view === "day" ? "min-h-48 p-4" : "grid min-w-[700px] grid-cols-7"}>
            {visibleDays.map((day) => {
              const key = dayKey(day);
              const items = scheduled.get(key) ?? [];
              const inMonth = day.getMonth() === month.getMonth();
              return (
                <div
                  key={key}
                  className={
                    view === "day"
                      ? "max-w-2xl"
                      : `border-b border-r border-slate-200 p-2 ${view === "month" ? "min-h-32" : "min-h-[24rem]"} ${inMonth ? "bg-white" : "bg-slate-50 text-slate-400"}`
                  }
                >
                  {canWrite ? (
                    <button
                      type="button"
                      onClick={() => openNewTask(day)}
                      aria-label={`Add task on ${day.toLocaleDateString(undefined, { month: "long", day: "numeric", year: "numeric" })}`}
                      className={`${view === "day" ? "mb-3 h-8 w-auto px-2" : "mb-1 h-6 w-6"} flex items-center justify-center rounded-full text-sm hover:bg-brand-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 ${key === today ? "bg-brand-600 font-semibold text-white hover:bg-brand-700" : ""}`}
                    >
                      {view === "day" ? "Add task" : day.getDate()}
                    </button>
                  ) : (
                    <div
                      className={`mb-1 flex h-6 w-6 items-center justify-center rounded-full text-sm ${key === today ? "bg-brand-600 font-semibold text-white" : ""}`}
                    >
                      {day.getDate()}
                    </div>
                  )}
                  {view === "day" && items.length === 0 && (
                    <p className="text-sm text-slate-500">No tasks scheduled.</p>
                  )}
                  <ul className={view === "day" ? "space-y-2" : "space-y-1"}>
                    {items.map((task) => (
                      <li
                        key={task.id}
                        className={`rounded ${priorityClass(task.priority)} ${view === "day" ? "p-3 text-sm" : "p-1.5 text-xs"}`}
                      >
                        <button
                          type="button"
                          className="block w-full text-left font-medium hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
                          onClick={() => navigate(`/tasks/${task.id}/edit`)}
                        >
                          {task.title}
                        </button>
                        <button
                          type="button"
                          className="mt-1 opacity-80 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
                          onClick={() => downloadTaskIcs(task)}
                        >
                          Download .ics
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              );
            })}
          </div>
        </section>
      )}
      <Dialog
        open={!!newTaskDate}
        onClose={closeNewTask}
        title={selectedDateLabel ? `New task — ${selectedDateLabel}` : "New task"}
        footer={
          <>
            <Button type="button" variant="secondary" onClick={closeNewTask} disabled={createTask.isPending}>
              Cancel
            </Button>
            <Button
              type="submit"
              form="calendar-new-task"
              loading={createTask.isPending}
              disabled={!newTaskTitle.trim()}
            >
              Create task
            </Button>
          </>
        }
      >
        <form
          id="calendar-new-task"
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            if (newTaskTitle.trim()) createTask.mutate();
          }}
        >
          <Input
            label="Task title"
            value={newTaskTitle}
            onChange={(event) => setNewTaskTitle(event.target.value)}
            placeholder="What needs doing?"
            required
          />
          <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
            Description <span className="font-normal text-slate-500">(optional)</span>
            <textarea
              value={newTaskDescription}
              onChange={(event) => setNewTaskDescription(event.target.value)}
              rows={3}
              maxLength={10000}
              className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-100"
              placeholder="Add any context"
            />
          </label>
        </form>
      </Dialog>
    </div>
  );
}
