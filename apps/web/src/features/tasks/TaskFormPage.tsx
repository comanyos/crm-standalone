import { useEffect } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { timestampDate, timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { PageHeader } from "../../components/shell/PageHeader";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Button } from "../../components/ui/Button";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { UnsavedChangesPrompt } from "../../components/ui/UnsavedChangesPrompt";
import { TagsPanel } from "../../components/TagsPanel";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useUnsavedChangesGuard } from "../../lib/useUnsavedChangesGuard";
import { useToast } from "../../components/ui/Toast";
import { useAuth } from "../../auth/AuthProvider";
import { subjectPath } from "./taskDisplay";

const schema = z.object({
  title: z.string().trim().min(1, "Title is required").max(300, "Title must be 300 characters or fewer"),
  description: z.string().max(10000, "Description is too long").optional().default(""),
  status: z.enum(["open", "in_progress", "completed", "cancelled"]),
  priority: z.enum(["low", "normal", "high", "urgent"]),
  assigneeId: z.string().trim().optional().default(""),
  // A date input yields "" or "YYYY-MM-DD"; both are valid, since a task
  // without a due date is a real state rather than an incomplete form.
  dueDate: z.string().optional().default(""),
});

type FormValues = z.infer<typeof schema>;

const statusOptions = [
  { value: "open", label: "Open" },
  { value: "in_progress", label: "In progress" },
  { value: "completed", label: "Completed" },
  { value: "cancelled", label: "Cancelled" },
];

const priorityOptions = [
  { value: "low", label: "Low" },
  { value: "normal", label: "Normal" },
  { value: "high", label: "High" },
  { value: "urgent", label: "Urgent" },
];

/** A date input's value is a calendar day with no time or zone. Sending it as
 * midnight *local* rather than midnight UTC is what makes "due today" mean
 * the reader's today: parsing "2026-08-22" with the Date constructor would
 * treat it as UTC and shift the date backwards for anyone west of Greenwich. */
function dueDateToTimestamp(value: string) {
  if (!value) return undefined;
  const [year, month, day] = value.split("-").map(Number);
  if (!year || !month || !day) return undefined;
  return timestampFromDate(new Date(year, month - 1, day, 23, 59, 59));
}

function timestampToDueDate(ts: Timestamp | undefined): string {
  if (!ts) return "";
  const date = timestampDate(ts);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

export default function TaskFormPage({ mode }: { mode: "create" | "edit" }) {
  const { id = "" } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const { crm } = useCrmClients();
  const { session } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();

  // A task created from a record page carries that record through the query
  // string, so "Add task" from an account lands attached to it.
  const subjectType = searchParams.get("subjectType") ?? "";
  const subjectId = searchParams.get("subjectId") ?? "";

  const taskQuery = useQuery({
    queryKey: ["crm.getTask", id],
    queryFn: () => crm.getTask({ id }),
    enabled: mode === "edit",
  });
  const existing = taskQuery.data?.task;

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isDirty, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      title: "",
      description: "",
      status: "open",
      priority: "normal",
      assigneeId: session?.claims.sub ?? "",
      dueDate: "",
    },
  });

  useEffect(() => {
    if (!existing) return;
    reset({
      title: existing.title,
      description: existing.description,
      status: (existing.status || "open") as FormValues["status"],
      priority: (existing.priority || "normal") as FormValues["priority"],
      assigneeId: existing.assigneeId,
      dueDate: timestampToDueDate(existing.dueAt),
    });
  }, [existing, reset]);

  const { blocker, disarm } = useUnsavedChangesGuard(isDirty && !isSubmitting);

  const save = useMutation({
    mutationFn: async (values: FormValues) => {
      const task = {
        title: values.title.trim(),
        description: values.description ?? "",
        status: values.status,
        priority: values.priority,
        assigneeId: values.assigneeId ?? "",
        dueAt: dueDateToTimestamp(values.dueDate ?? ""),
        subjectType: mode === "edit" ? existing?.subjectType ?? "" : subjectType,
        subjectId: mode === "edit" ? existing?.subjectId ?? "" : subjectId,
      };
      if (mode === "create") {
        const created = await crm.createTask({ task });
        return created.task;
      }
      // updatedAt round-trips the version this form was opened on, so a save
      // that would overwrite somebody else's concurrent edit is rejected
      // rather than silently winning.
      // UpdateTask replaces the custom field document rather than merging
      // it, so anything this form omits is deleted. This form has no
      // custom-field inputs, so it round-trips what it read.
      const updated = await crm.updateTask({
        task: { ...task, id, customFields: existing?.customFields, updatedAt: existing?.updatedAt },
      });
      return updated.task;
    },
    onSuccess: (task) => {
      // Disarm before navigating, or the guard blocks the redirect this
      // handler is about to perform and the user is asked to confirm
      // leaving a form they just saved.
      disarm();
      queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      queryClient.invalidateQueries({ queryKey: ["crm.getTask", id] });
      toast.show({ tone: "success", message: mode === "create" ? "Task created." : "Task saved." });
      // Back to whichever record the task belongs to, if any — otherwise the
      // work list.
      const target = task?.subjectType && task?.subjectId ? subjectPath(task.subjectType, task.subjectId) : "/tasks";
      navigate(target);
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (mode === "edit" && taskQuery.isPending) return <SkeletonRows rows={5} />;
  if (mode === "edit" && taskQuery.error) {
    return renderQueryError(taskQuery.error, {
      onRetry: () => taskQuery.refetch(),
      forbiddenDescription: "You don't have permission to view this task.",
    });
  }

  return (
    <div>
      <PageHeader title={mode === "create" ? "New task" : `Edit ${existing?.title ?? "task"}`} />
      <form
        className="flex max-w-xl flex-col gap-4"
        onSubmit={handleSubmit((values) => save.mutateAsync(values).catch(() => undefined))}
      >
        <Input label="Title" required {...register("title")} error={errors.title?.message} />
        <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
          Description
          <textarea
            {...register("description")}
            rows={4}
            className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-100"
            placeholder="What needs doing?"
          />
        </label>
        <Select label="Status" options={statusOptions} {...register("status")} error={errors.status?.message} />
        <Select label="Priority" options={priorityOptions} {...register("priority")} error={errors.priority?.message} />
        <Input
          label="Assignee"
          {...register("assigneeId")}
          error={errors.assigneeId?.message}
          hint="Leave blank for an unassigned task."
        />
        <Input label="Due date" type="date" {...register("dueDate")} error={errors.dueDate?.message} />

        <div className="flex gap-2">
          <Button type="submit" loading={save.isPending}>
            {mode === "create" ? "Create task" : "Save task"}
          </Button>
          <Button type="button" variant="secondary" onClick={() => navigate(-1)}>
            Cancel
          </Button>
        </div>
      </form>
      {mode === "edit" && id && (
        <div className="mt-6 max-w-xl">
          <TagsPanel objectType="task" objectId={id} />
        </div>
      )}
      <UnsavedChangesPrompt blocker={blocker} />
    </div>
  );
}
