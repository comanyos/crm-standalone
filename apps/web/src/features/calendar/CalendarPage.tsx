import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { GoogleCalendarEvent, Task } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
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
import { dateToZonedLocal, zonedLocalToDate } from "../../lib/zonedDateTime";

const weekdays = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
type CalendarView = "month" | "week" | "day";
type StatusFilter = "active" | "open" | "in_progress" | "completed" | "cancelled" | "all";

type EventDraft = {
  requestId: string;
  providerEventId: string;
  etag: string;
  title: string;
  start: string;
  end: string;
  timeZone: string;
  attendees: string;
  location: string;
  description: string;
  subjectType: "contact" | "lead" | "opportunity";
  subjectId: string;
  recurrence: "none" | "daily" | "weekly" | "monthly";
  recurrenceCount: string;
  reminder: string;
};

function dayKey(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function datetimeLocal(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
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
  const firstLabel = days[0].toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const lastLabel = days[6].toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
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

function blankEvent(date: Date): EventDraft {
  const start = new Date(date);
  const now = new Date();
  start.setHours(date.toDateString() === now.toDateString() ? now.getHours() + 1 : 9, 0, 0, 0);
  const end = new Date(start.getTime() + 60 * 60 * 1000);
  return {
    requestId: crypto.randomUUID(),
    providerEventId: "",
    etag: "",
    title: "",
    start: datetimeLocal(start),
    end: datetimeLocal(end),
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    attendees: "",
    location: "",
    description: "",
    subjectType: "contact",
    subjectId: "",
    recurrence: "none",
    recurrenceCount: "10",
    reminder: "30",
  };
}

function draftFromEvent(event: GoogleCalendarEvent): EventDraft {
  const recurrence = event.recurrence[0]?.match(/FREQ=(DAILY|WEEKLY|MONTHLY)/)?.[1]?.toLowerCase();
  const count = event.recurrence[0]?.match(/COUNT=(\d+)/)?.[1] ?? "10";
  return {
    requestId: "",
    providerEventId: event.providerEventId,
    etag: event.etag,
    title: event.title,
    start: event.startsAt ? dateToZonedLocal(timestampDate(event.startsAt), event.timeZone || "UTC") : "",
    end: event.endsAt ? dateToZonedLocal(timestampDate(event.endsAt), event.timeZone || "UTC") : "",
    timeZone: event.timeZone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    attendees: event.attendees.join(", "),
    location: event.location,
    description: event.description,
    subjectType: (event.subjectType || "contact") as EventDraft["subjectType"],
    subjectId: event.subjectId,
    recurrence: (recurrence ?? "none") as EventDraft["recurrence"],
    recurrenceCount: count,
    reminder: String(event.reminderMinutes[0] ?? 30),
  };
}

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
  const [eventDraft, setEventDraft] = useState<EventDraft | null>(null);
  const [cancelEvent, setCancelEvent] = useState<GoogleCalendarEvent | null>(null);
  const [eventLoading, setEventLoading] = useState(false);
  const [assigneeFilter, setAssigneeFilter] = useState("all");
  const [priorityFilter, setPriorityFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("active");
  const canWriteTasks = hasScope("crm:task:write");
  const canReadEvents = hasScope("crm:integration:read");
  const canWriteEvents = hasScope("crm:integration:write");

  const tasksQuery = useQuery({
    queryKey: ["crm.listTasks", "calendar"],
    queryFn: () => crm.listTasks({ pageSize: 500 }),
  });
  const eventsQuery = useQuery({
    queryKey: ["crm.listGoogleCalendarEvents"],
    queryFn: () => crm.listGoogleCalendarEvents({ limit: 500 }),
    enabled: canReadEvents,
  });
  const contactsQuery = useQuery({
    queryKey: ["crm.listContacts", "calendar-link"],
    queryFn: () => crm.listContacts({ pageSize: 200 }),
    enabled: !!eventDraft && eventDraft.subjectType === "contact",
  });
  const leadsQuery = useQuery({
    queryKey: ["crm.listLeads", "calendar-link"],
    queryFn: () => crm.listLeads({ pageSize: 200 }),
    enabled: !!eventDraft && eventDraft.subjectType === "lead",
  });
  const opportunitiesQuery = useQuery({
    queryKey: ["crm.listOpportunities", "calendar-link"],
    queryFn: () => crm.listOpportunities({ pageSize: 200 }),
    enabled: !!eventDraft && eventDraft.subjectType === "opportunity",
  });

  const createTask = useMutation({
    mutationFn: () => {
      if (!newTaskDate) throw new Error("Choose a date for this task.");
      return crm.createTask({ task: { title: newTaskTitle.trim(), description: newTaskDescription.trim(),
        status: "open", priority: "normal", assigneeId: session?.claims.sub ?? "",
        dueAt: dueDateTimestamp(newTaskDate), subjectType: "", subjectId: "" } });
    },
    onSuccess: () => {
      setNewTaskDate(null); setNewTaskTitle(""); setNewTaskDescription("");
      void queryClient.invalidateQueries({ queryKey: ["crm.listTasks"] });
      toast.show({ tone: "success", message: "Task created." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const eventMessage = (draft: EventDraft) => {
    const count = Math.max(1, Math.min(100, Number(draft.recurrenceCount) || 10));
    const recurrence = draft.recurrence === "none" ? [] : [`RRULE:FREQ=${draft.recurrence.toUpperCase()};COUNT=${count}`];
    const reminder = Number(draft.reminder);
    return {
      providerEventId: draft.providerEventId,
      etag: draft.etag,
      title: draft.title.trim(),
      description: draft.description.trim(),
      location: draft.location.trim(),
      startsAt: timestampFromDate(zonedLocalToDate(draft.start, draft.timeZone)),
      endsAt: timestampFromDate(zonedLocalToDate(draft.end, draft.timeZone)),
      timeZone: draft.timeZone.trim(),
      attendees: draft.attendees.split(",").map((value) => value.trim()).filter(Boolean),
      recurrence,
      reminderMinutes: Number.isFinite(reminder) && reminder >= 0 ? [reminder] : [],
      subjectType: draft.subjectType,
      subjectId: draft.subjectId,
    };
  };
  const saveEvent = useMutation({
    mutationFn: async (draft: EventDraft) => {
      if (draft.providerEventId) {
        return (await crm.updateGoogleCalendarEvent({ event: eventMessage(draft) })).event;
      }
      return (await crm.createGoogleCalendarEvent({ requestId: draft.requestId, event: eventMessage(draft) })).event;
    },
    onSuccess: (_result, draft) => {
      setEventDraft(null);
      void queryClient.invalidateQueries({ queryKey: ["crm.listGoogleCalendarEvents"] });
      toast.show({ tone: "success", message: draft.providerEventId ? "Meeting updated in Google Calendar." : "Meeting created in Google Calendar." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const cancelMeeting = useMutation({
    mutationFn: (event: GoogleCalendarEvent) => crm.cancelGoogleCalendarEvent({ providerEventId: event.providerEventId, etag: event.etag }),
    onSuccess: () => {
      setCancelEvent(null); setEventDraft(null);
      void queryClient.invalidateQueries({ queryKey: ["crm.listGoogleCalendarEvents"] });
      toast.show({ tone: "success", message: "Meeting cancelled and attendees notified." });
    },
    onError: (error) => { setCancelEvent(null); toast.show({ tone: "danger", message: mutationErrorMessage(error) }); },
  });

  const assigneeOptions = useMemo(() => {
    const assignees = [...new Set((tasksQuery.data?.tasks ?? []).map((task) => task.assigneeId).filter(Boolean))].sort();
    return [{ value: "all", label: "All assignees" }, ...(session?.claims.sub ? [{ value: "mine", label: "My tasks" }] : []),
      { value: "unassigned", label: "Unassigned" }, ...assignees.map((id) => ({ value: id, label: id }))];
  }, [tasksQuery.data, session?.claims.sub]);
  const filteredTasks = useMemo(() => (tasksQuery.data?.tasks ?? []).filter((task) => {
    const status = task.status || "open";
    const statusMatches = statusFilter === "all" || (statusFilter === "active" ? status === "open" || status === "in_progress" : status === statusFilter);
    const priorityMatches = priorityFilter === "all" || (task.priority || "normal") === priorityFilter;
    const assigneeMatches = assigneeFilter === "all" || (assigneeFilter === "mine" ? task.assigneeId === session?.claims.sub : assigneeFilter === "unassigned" ? !task.assigneeId : task.assigneeId === assigneeFilter);
    return statusMatches && priorityMatches && assigneeMatches;
  }), [assigneeFilter, priorityFilter, session?.claims.sub, statusFilter, tasksQuery.data]);
  const scheduledTasks = useMemo(() => {
    const grouped = new Map<string, Task[]>();
    for (const task of filteredTasks) if (task.dueAt) { const key = dayKey(timestampDate(task.dueAt)); grouped.set(key, [...(grouped.get(key) ?? []), task]); }
    return grouped;
  }, [filteredTasks]);
  const scheduledEvents = useMemo(() => {
    const grouped = new Map<string, GoogleCalendarEvent[]>();
    for (const event of eventsQuery.data?.events ?? []) if (event.startsAt) { const key = dayKey(timestampDate(event.startsAt)); grouped.set(key, [...(grouped.get(key) ?? []), event]); }
    return grouped;
  }, [eventsQuery.data]);

  const month = useMemo(() => new Date(focusDate.getFullYear(), focusDate.getMonth(), 1), [focusDate]);
  const days = useMemo(() => monthDays(month), [month]);
  const week = useMemo(() => weekDays(focusDate), [focusDate]);
  const today = dayKey(new Date());
  const title = view === "month" ? month.toLocaleDateString(undefined, { month: "long", year: "numeric" }) : view === "week" ? weekTitle(week) : focusDate.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" });
  const visibleDays = view === "month" ? days : view === "week" ? week : [focusDate];
  const dateStep = view === "month" ? 30 : view === "week" ? 7 : 1;
  const periodName = view === "month" ? "month" : view;
  const hasItems = scheduledTasks.size > 0 || scheduledEvents.size > 0;
  const patchEvent = (changes: Partial<EventDraft>) => setEventDraft((current) => current && ({ ...current, ...changes }));

  const recordOptions = eventDraft?.subjectType === "lead"
    ? (leadsQuery.data?.leads ?? []).map((lead) => ({ value: lead.id, label: `${lead.firstName} ${lead.lastName}`.trim() || lead.company || lead.id }))
    : eventDraft?.subjectType === "opportunity"
      ? (opportunitiesQuery.data?.opportunities ?? []).map((opportunity) => ({ value: opportunity.id, label: opportunity.name || opportunity.id }))
      : (contactsQuery.data?.contacts ?? []).map((contact) => ({ value: contact.id, label: `${contact.firstName} ${contact.lastName}`.trim() || contact.email || contact.id }));

  const openEvent = async (event: GoogleCalendarEvent) => {
    setEventLoading(true);
    try {
      const result = await crm.getGoogleCalendarEvent({ providerEventId: event.providerEventId });
      if (result.event) setEventDraft(draftFromEvent(result.event));
    } catch (error) {
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    } finally { setEventLoading(false); }
  };

  return (
    <div>
      <PageHeader title="Calendar" actions={<div className="flex gap-2">
        {canWriteTasks && <Button variant="secondary" onClick={() => setNewTaskDate(new Date())}>New task</Button>}
        {canWriteEvents && <Button onClick={() => setEventDraft(blankEvent(new Date()))}>New event</Button>}
      </div>} />
      <p className="mb-5 max-w-3xl text-sm text-slate-600">
        Plan work and manage Google meetings in one view. Savvy CRM events notify attendees and remain linked to their contact, lead, or opportunity.
      </p>
      <div className="mb-4 flex flex-wrap items-end gap-3" aria-label="Calendar task filters">
        <Select label="Assignee" value={assigneeFilter} onChange={(e) => setAssigneeFilter(e.target.value)} options={assigneeOptions} />
        <Select label="Priority" value={priorityFilter} onChange={(e) => setPriorityFilter(e.target.value)} options={[{ value: "all", label: "All priorities" }, { value: "urgent", label: "Urgent" }, { value: "high", label: "High" }, { value: "normal", label: "Normal" }, { value: "low", label: "Low" }]} />
        <Select label="Status" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as StatusFilter)} options={[{ value: "active", label: "Open & in progress" }, { value: "open", label: "Open" }, { value: "in_progress", label: "In progress" }, { value: "completed", label: "Completed" }, { value: "cancelled", label: "Cancelled" }, { value: "all", label: "All statuses" }]} />
      </div>
      <div className="mb-4 flex flex-wrap items-center gap-2" aria-label="Calendar controls">
        <Button variant="secondary" onClick={() => setFocusDate((value) => view === "month" ? new Date(value.getFullYear(), value.getMonth() - 1, 1) : addDays(value, -dateStep))}>Previous {periodName}</Button>
        <Button variant="secondary" onClick={() => setFocusDate(new Date())}>Today</Button>
        <Button variant="secondary" onClick={() => setFocusDate((value) => view === "month" ? new Date(value.getFullYear(), value.getMonth() + 1, 1) : addDays(value, dateStep))}>Next {periodName}</Button>
        <h2 className="ml-2 text-lg font-semibold text-slate-900" aria-live="polite">{title}</h2>
        <div className="ml-auto flex gap-1" role="group" aria-label="Calendar view">{(["month", "week", "day"] as const).map((option) => <Button key={option} size="sm" variant={view === option ? "primary" : "secondary"} aria-pressed={view === option} onClick={() => setView(option)}>{option[0].toUpperCase() + option.slice(1)}</Button>)}</div>
      </div>

      {(tasksQuery.isPending || (canReadEvents && eventsQuery.isPending)) && <SkeletonRows rows={6} />}
      {tasksQuery.error && renderQueryError(tasksQuery.error, { onRetry: () => tasksQuery.refetch(), forbiddenDescription: "You don't have permission to view scheduled tasks." })}
      {eventsQuery.error && renderQueryError(eventsQuery.error, { onRetry: () => eventsQuery.refetch(), forbiddenDescription: "You don't have permission to view connected calendar events." })}
      {tasksQuery.data && !hasItems && <EmptyState title="Nothing scheduled yet" description="Create a Google meeting or give a task a due date to see it here." action={canWriteEvents ? <Button onClick={() => setEventDraft(blankEvent(new Date()))}>New event</Button> : undefined} />}
      {tasksQuery.data && <section aria-label={`${title} ${view} calendar`} className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
        {view !== "day" && <div className="grid min-w-[700px] grid-cols-7 border-b border-slate-200 bg-slate-50">{weekdays.map((weekday) => <div key={weekday} className="px-3 py-2 text-center text-xs font-semibold uppercase tracking-wide text-slate-600">{weekday}</div>)}</div>}
        <div className={view === "day" ? "min-h-48 p-4" : "grid min-w-[700px] grid-cols-7"}>
          {visibleDays.map((day) => {
            const key = dayKey(day); const tasks = scheduledTasks.get(key) ?? []; const meetings = scheduledEvents.get(key) ?? []; const inMonth = day.getMonth() === month.getMonth();
            return <div key={key} className={view === "day" ? "max-w-2xl" : `border-b border-r border-slate-200 p-2 ${view === "month" ? "min-h-32" : "min-h-[24rem]"} ${inMonth ? "bg-white" : "bg-slate-50 text-slate-400"}`}>
              <div className="mb-1 flex items-center justify-between gap-2">
                {canWriteEvents ? <button type="button" onClick={() => setEventDraft(blankEvent(day))} aria-label={`Add event on ${day.toLocaleDateString()}`} className={`${view === "day" ? "h-8 w-auto px-2" : "h-6 w-6"} flex items-center justify-center rounded-full text-sm hover:bg-brand-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600 ${key === today ? "bg-brand-600 font-semibold text-white hover:bg-brand-700" : ""}`}>{view === "day" ? "Add event" : day.getDate()}</button> : <div className={`flex h-6 w-6 items-center justify-center rounded-full text-sm ${key === today ? "bg-brand-600 font-semibold text-white" : ""}`}>{day.getDate()}</div>}
                {canWriteTasks && <button type="button" onClick={() => setNewTaskDate(day)} aria-label={`Add task on ${day.toLocaleDateString()}`} className="rounded-md px-1.5 py-0.5 text-xs font-semibold text-slate-500 hover:bg-slate-100 hover:text-slate-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600">{view === "day" ? "Add task" : "+ task"}</button>}
              </div>
              {view === "day" && tasks.length === 0 && meetings.length === 0 && <p className="text-sm text-slate-500">Nothing scheduled.</p>}
              <ul className={view === "day" ? "space-y-2" : "space-y-1"}>
                {meetings.map((meeting) => <li key={meeting.providerEventId} className={`rounded border-l-4 border-emerald-500 bg-emerald-50 text-emerald-950 ${view === "day" ? "p-3 text-sm" : "p-1.5 text-xs"}`}><button type="button" disabled={eventLoading} className={`block w-full text-left font-semibold hover:underline ${meeting.status === "cancelled" ? "line-through opacity-60" : ""}`} onClick={() => void openEvent(meeting)}>{meeting.title}</button><span className="block opacity-75">Google meeting{meeting.startsAt ? ` · ${timestampDate(meeting.startsAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : ""}</span></li>)}
                {tasks.map((task) => <li key={task.id} className={`rounded ${priorityClass(task.priority)} ${view === "day" ? "p-3 text-sm" : "p-1.5 text-xs"}`}><button type="button" className="block w-full text-left font-medium hover:underline" onClick={() => navigate(`/tasks/${task.id}/edit`)}>{task.title}</button><button type="button" className="mt-1 opacity-80 hover:underline" onClick={() => downloadTaskIcs(task)}>Download .ics</button></li>)}
              </ul>
            </div>;
          })}
        </div>
      </section>}

      <Dialog open={!!newTaskDate} onClose={() => !createTask.isPending && setNewTaskDate(null)} title="New task" footer={<><Button variant="secondary" onClick={() => setNewTaskDate(null)} disabled={createTask.isPending}>Cancel</Button><Button type="submit" form="calendar-new-task" loading={createTask.isPending} disabled={!newTaskTitle.trim()}>Create task</Button></>}>
        <form id="calendar-new-task" className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); if (newTaskTitle.trim()) createTask.mutate(); }}><Input label="Task title" value={newTaskTitle} onChange={(e) => setNewTaskTitle(e.target.value)} required /><label className="flex flex-col gap-1 text-sm font-medium text-slate-700">Description<textarea value={newTaskDescription} onChange={(e) => setNewTaskDescription(e.target.value)} rows={3} className="rounded-md border border-slate-300 px-3 py-2 font-normal" /></label></form>
      </Dialog>

      <Dialog open={!!eventDraft} onClose={() => !saveEvent.isPending && setEventDraft(null)} title={eventDraft?.providerEventId ? "Edit Google meeting" : "New Google meeting"} footer={eventDraft && <>{eventDraft.providerEventId && <Button variant="danger" className="mr-auto" onClick={() => setCancelEvent({ providerEventId: eventDraft.providerEventId, etag: eventDraft.etag } as GoogleCalendarEvent)}>Cancel meeting</Button>}<Button variant="secondary" onClick={() => setEventDraft(null)} disabled={saveEvent.isPending}>Close</Button><Button type="submit" form="calendar-event-form" loading={saveEvent.isPending} disabled={!eventDraft.title.trim() || !eventDraft.subjectId}>Save in Google</Button></>}>
        {eventDraft && <form id="calendar-event-form" className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); saveEvent.mutate(eventDraft); }}>
          <Input label="Title" value={eventDraft.title} onChange={(e) => patchEvent({ title: e.target.value })} required />
          <div className="grid gap-3 sm:grid-cols-2"><Input label="Starts" type="datetime-local" value={eventDraft.start} onChange={(e) => patchEvent({ start: e.target.value })} required /><Input label="Ends" type="datetime-local" value={eventDraft.end} onChange={(e) => patchEvent({ end: e.target.value })} required /></div>
          <Input label="Time zone" value={eventDraft.timeZone} onChange={(e) => patchEvent({ timeZone: e.target.value })} hint="IANA name, for example Asia/Kolkata" required />
          <div className="grid gap-3 sm:grid-cols-2"><Select label="Link to" value={eventDraft.subjectType} onChange={(e) => patchEvent({ subjectType: e.target.value as EventDraft["subjectType"], subjectId: "" })} options={[{ value: "contact", label: "Contact" }, { value: "lead", label: "Lead" }, { value: "opportunity", label: "Opportunity" }]} /><Select label="CRM record" value={eventDraft.subjectId} onChange={(e) => patchEvent({ subjectId: e.target.value })} placeholder="Choose a record" options={recordOptions} required /></div>
          <Input label="Attendees" value={eventDraft.attendees} onChange={(e) => patchEvent({ attendees: e.target.value })} hint="Comma-separated email addresses" />
          <Input label="Location" value={eventDraft.location} onChange={(e) => patchEvent({ location: e.target.value })} />
          <label className="flex flex-col gap-1.5 text-sm font-semibold text-slate-700">Description<textarea value={eventDraft.description} onChange={(e) => patchEvent({ description: e.target.value })} rows={3} maxLength={10000} className="rounded-lg border border-slate-300 px-3 py-2 font-normal text-slate-900" /></label>
          <div className="grid gap-3 sm:grid-cols-2"><Select label="Repeats" value={eventDraft.recurrence} onChange={(e) => patchEvent({ recurrence: e.target.value as EventDraft["recurrence"] })} options={[{ value: "none", label: "Does not repeat" }, { value: "daily", label: "Daily" }, { value: "weekly", label: "Weekly" }, { value: "monthly", label: "Monthly" }]} />{eventDraft.recurrence !== "none" && <Input label="Number of meetings" type="number" min="1" max="100" value={eventDraft.recurrenceCount} onChange={(e) => patchEvent({ recurrenceCount: e.target.value })} />}</div>
          <Select label="Reminder" value={eventDraft.reminder} onChange={(e) => patchEvent({ reminder: e.target.value })} options={[{ value: "10", label: "10 minutes before" }, { value: "30", label: "30 minutes before" }, { value: "60", label: "1 hour before" }, { value: "1440", label: "1 day before" }]} />
        </form>}
      </Dialog>
      <ConfirmDialog open={!!cancelEvent} title="Cancel this meeting?" description="Google Calendar will cancel the event and notify attendees. This action is recorded on the linked CRM timeline." confirmLabel="Cancel meeting" tone="danger" loading={cancelMeeting.isPending} onConfirm={() => cancelEvent && cancelMeeting.mutate(cancelEvent)} onCancel={() => setCancelEvent(null)} />
    </div>
  );
}
