import { useState } from "react";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  WorkflowApproval,
  WorkflowRule,
  WorkflowSchedule,
  WorkflowWebhookEndpoint,
} from "@crm/contracts";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Table } from "../../components/ui/Table";
import { useToast } from "../../components/ui/Toast";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { zonedLocalToDate } from "../../lib/zonedDateTime";

type Props = { canWrite: boolean; rules: WorkflowRule[] };

type ScheduleDraft = {
  ruleId: string;
  subjectId: string;
  nextRunAt: string;
  timeZone: string;
  recurrence: "once" | "daily" | "weekly" | "monthly";
  interval: string;
  endsAt: string;
};

const pad = (value: number) => String(value).padStart(2, "0");
const localDateTime = (date: Date) =>
  `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;

function newSchedule(ruleId = ""): ScheduleDraft {
  const start = new Date(Date.now() + 60 * 60 * 1000);
  start.setMinutes(0, 0, 0);
  return {
    ruleId,
    subjectId: "",
    nextRunAt: localDateTime(start),
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    recurrence: "once",
    interval: "1",
    endsAt: "",
  };
}

const displayTime = (value: WorkflowSchedule["nextRunAt"], timeZone: string) =>
  value ? timestampDate(value).toLocaleString(undefined, { timeZone }) : "—";

export function AdvancedAutomation({ canWrite, rules }: Props) {
  const { crm } = useCrmClients();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [scheduleDraft, setScheduleDraft] = useState<ScheduleDraft | null>(null);
  const [deleteSchedule, setDeleteSchedule] = useState<WorkflowSchedule | null>(null);
  const [approvalDecision, setApprovalDecision] = useState<{ row: WorkflowApproval; decision: "approved" | "rejected" } | null>(null);
  const [decisionNote, setDecisionNote] = useState("");
  const [webhookDraft, setWebhookDraft] = useState<{ name: string; url: string } | null>(null);
  const [revealedSecret, setRevealedSecret] = useState("");
  const [deleteWebhook, setDeleteWebhook] = useState<WorkflowWebhookEndpoint | null>(null);

  const schedulesQuery = useQuery({
    queryKey: ["crm.listWorkflowSchedules"],
    queryFn: () => crm.listWorkflowSchedules({}),
  });
  const approvalsQuery = useQuery({
    queryKey: ["crm.listWorkflowApprovals", "pending"],
    queryFn: () => crm.listWorkflowApprovals({ status: "pending", limit: 100 }),
  });
  const endpointsQuery = useQuery({
    queryKey: ["crm.listWorkflowWebhookEndpoints"],
    queryFn: () => crm.listWorkflowWebhookEndpoints({}),
  });

  const selectedRuleQuery = useQuery({
    queryKey: ["crm.getWorkflowRule", scheduleDraft?.ruleId],
    queryFn: () => crm.getWorkflowRule({ id: scheduleDraft?.ruleId ?? "" }),
    enabled: Boolean(scheduleDraft?.ruleId),
  });
  const subjectType = selectedRuleQuery.data?.activeVersion?.triggerObjectType ?? "";
  const contactsQuery = useQuery({
    queryKey: ["crm.listContacts", "workflow-schedule"],
    queryFn: () => crm.listContacts({ pageSize: 200 }),
    enabled: Boolean(scheduleDraft) && subjectType === "contact",
  });
  const leadsQuery = useQuery({
    queryKey: ["crm.listLeads", "workflow-schedule"],
    queryFn: () => crm.listLeads({ pageSize: 200 }),
    enabled: Boolean(scheduleDraft) && subjectType === "lead",
  });
  const opportunitiesQuery = useQuery({
    queryKey: ["crm.listOpportunities", "workflow-schedule"],
    queryFn: () => crm.listOpportunities({ pageSize: 200 }),
    enabled: Boolean(scheduleDraft) && subjectType === "opportunity",
  });
  const accountsQuery = useQuery({
    queryKey: ["crm.listAccounts", "workflow-schedule"],
    queryFn: () => crm.listAccounts({ pageSize: 200 }),
    enabled: Boolean(scheduleDraft) && subjectType === "account",
  });

  const activeRules = rules.filter((rule) => rule.status === "active" && rule.activeVersionId);
  const subjectOptions =
    subjectType === "lead"
      ? (leadsQuery.data?.leads ?? []).map((row) => ({ value: row.id, label: `${row.firstName} ${row.lastName}`.trim() || row.company || row.id }))
      : subjectType === "opportunity"
        ? (opportunitiesQuery.data?.opportunities ?? []).map((row) => ({ value: row.id, label: row.name || row.id }))
        : subjectType === "account"
          ? (accountsQuery.data?.accounts ?? []).map((row) => ({ value: row.id, label: row.name || row.id }))
          : (contactsQuery.data?.contacts ?? []).map((row) => ({ value: row.id, label: `${row.firstName} ${row.lastName}`.trim() || row.email || row.id }));

  const refreshSchedules = () => queryClient.invalidateQueries({ queryKey: ["crm.listWorkflowSchedules"] });
  const createSchedule = useMutation({
    mutationFn: (draft: ScheduleDraft) =>
      crm.createWorkflowSchedule({
        ruleId: draft.ruleId,
        subjectType,
        subjectId: draft.subjectId,
        timeZone: draft.timeZone.trim(),
        recurrence: draft.recurrence,
        interval: Number(draft.interval) || 1,
        nextRunAt: timestampFromDate(zonedLocalToDate(draft.nextRunAt, draft.timeZone)),
        endsAt: draft.endsAt ? timestampFromDate(zonedLocalToDate(draft.endsAt, draft.timeZone)) : undefined,
      }),
    onSuccess: () => {
      setScheduleDraft(null);
      void refreshSchedules();
      toast.show({ tone: "success", message: "Scheduled sequence created." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const updateSchedule = useMutation({
    mutationFn: ({ row, status }: { row: WorkflowSchedule; status: string }) =>
      crm.updateWorkflowSchedule({
        id: row.id,
        timeZone: row.timeZone,
        recurrence: row.recurrence,
        interval: row.interval,
        status,
        nextRunAt: row.nextRunAt,
        endsAt: row.endsAt,
      }),
    onSuccess: () => {
      void refreshSchedules();
      toast.show({ tone: "success", message: "Schedule updated." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const removeSchedule = useMutation({
    mutationFn: (id: string) => crm.deleteWorkflowSchedule({ id }),
    onSuccess: () => {
      setDeleteSchedule(null);
      void refreshSchedules();
      toast.show({ tone: "success", message: "Schedule deleted." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const decideApproval = useMutation({
    mutationFn: ({ row, decision }: { row: WorkflowApproval; decision: string }) =>
      crm.decideWorkflowApproval({ id: row.id, decision, note: decisionNote.trim() }),
    onSuccess: (_data, input) => {
      setApprovalDecision(null);
      setDecisionNote("");
      void queryClient.invalidateQueries({ queryKey: ["crm.listWorkflowApprovals"] });
      toast.show({ tone: "success", message: input.decision === "approved" ? "Approved. The sequence will continue." : "Rejected. The sequence has stopped." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const createWebhook = useMutation({
    mutationFn: (draft: { name: string; url: string }) =>
      crm.createWorkflowWebhookEndpoint({ name: draft.name.trim(), url: draft.url.trim() }),
    onSuccess: (data) => {
      setWebhookDraft(null);
      setRevealedSecret(data.signingSecret);
      void queryClient.invalidateQueries({ queryKey: ["crm.listWorkflowWebhookEndpoints"] });
      toast.show({ tone: "success", message: "Webhook endpoint created." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const removeWebhook = useMutation({
    mutationFn: (id: string) => crm.deleteWorkflowWebhookEndpoint({ id }),
    onSuccess: () => {
      setDeleteWebhook(null);
      void queryClient.invalidateQueries({ queryKey: ["crm.listWorkflowWebhookEndpoints"] });
      toast.show({ tone: "success", message: "Webhook endpoint deleted." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  return (
    <div className="mb-8 grid gap-4 xl:grid-cols-3">
      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm" aria-labelledby="approval-heading">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <h2 id="approval-heading" className="font-semibold text-slate-950">My approvals</h2>
            <p className="text-xs text-slate-500">Sequences waiting for your decision.</p>
          </div>
          <Badge tone={(approvalsQuery.data?.approvals.length ?? 0) > 0 ? "warning" : "neutral"}>{approvalsQuery.data?.approvals.length ?? 0}</Badge>
        </div>
        {approvalsQuery.error && renderQueryError(approvalsQuery.error, { onRetry: () => approvalsQuery.refetch() })}
        {approvalsQuery.data?.approvals.length === 0 && <p className="py-4 text-sm text-slate-500">Nothing is waiting for you.</p>}
        <ul className="space-y-3">
          {(approvalsQuery.data?.approvals ?? []).map((row) => (
            <li key={row.id} className="rounded-lg border border-slate-200 p-3">
              <p className="font-medium text-slate-900">{row.title}</p>
              {row.body && <p className="mt-1 text-sm text-slate-600">{row.body}</p>}
              <p className="mt-1 text-xs text-slate-500">{row.subjectType} · {row.subjectId}</p>
              {canWrite && <div className="mt-3 flex gap-2"><Button size="sm" onClick={() => setApprovalDecision({ row, decision: "approved" })}>Approve</Button><Button size="sm" variant="danger" onClick={() => setApprovalDecision({ row, decision: "rejected" })}>Reject</Button></div>}
            </li>
          ))}
        </ul>
      </section>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm xl:col-span-2" aria-labelledby="schedule-heading">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div><h2 id="schedule-heading" className="font-semibold text-slate-950">Scheduled sequences</h2><p className="text-xs text-slate-500">Run a published rule once or on a recurring date and time.</p></div>
          {canWrite && <Button size="sm" onClick={() => setScheduleDraft(newSchedule(activeRules[0]?.id))} disabled={activeRules.length === 0}>New schedule</Button>}
        </div>
        {schedulesQuery.error && renderQueryError(schedulesQuery.error, { onRetry: () => schedulesQuery.refetch() })}
        {schedulesQuery.data?.schedules.length === 0 && <p className="py-4 text-sm text-slate-500">No scheduled sequences yet.</p>}
        {(schedulesQuery.data?.schedules.length ?? 0) > 0 && <Table caption="Scheduled sequences" rows={schedulesQuery.data?.schedules ?? []} getRowKey={(row) => row.id} columns={[
          { key: "rule", header: "Rule / record", render: (row) => <><div className="font-medium">{rules.find((rule) => rule.id === row.ruleId)?.name ?? row.ruleId}</div><div className="text-xs text-slate-500">{row.subjectType} · {row.subjectId}</div></> },
          { key: "next", header: "Next run", render: (row) => <><div>{displayTime(row.nextRunAt, row.timeZone)}</div><div className="text-xs text-slate-500">{row.recurrence === "once" ? "Once" : `Every ${row.interval > 1 ? `${row.interval} ` : ""}${row.recurrence}` } · {row.timeZone}</div></> },
          { key: "status", header: "Status", render: (row) => <Badge tone={row.status === "active" ? "success" : row.status === "paused" ? "warning" : "neutral"}>{row.status}</Badge> },
          { key: "actions", header: "Actions", headerHidden: true, render: (row) => canWrite ? <div className="flex gap-2">{row.status !== "completed" && <Button size="sm" variant="secondary" onClick={() => updateSchedule.mutate({ row, status: row.status === "active" ? "paused" : "active" })}>{row.status === "active" ? "Pause" : "Resume"}</Button>}<Button size="sm" variant="danger" onClick={() => setDeleteSchedule(row)}>Delete</Button></div> : null },
        ]} />}
      </section>

      <section className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm xl:col-span-3" aria-labelledby="webhook-heading">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div><h2 id="webhook-heading" className="font-semibold text-slate-950">Outbound webhooks</h2><p className="text-xs text-slate-500">Signed HTTPS destinations available to rule actions.</p></div>
          {canWrite && <Button size="sm" variant="secondary" onClick={() => setWebhookDraft({ name: "", url: "" })}>Add endpoint</Button>}
        </div>
        {endpointsQuery.error && renderQueryError(endpointsQuery.error, { onRetry: () => endpointsQuery.refetch() })}
        {endpointsQuery.data?.endpoints.length === 0 && <p className="py-3 text-sm text-slate-500">No webhook endpoints configured.</p>}
        <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3">{(endpointsQuery.data?.endpoints ?? []).map((endpoint) => <div key={endpoint.id} className="flex items-start justify-between gap-3 rounded-lg border border-slate-200 p-3"><div className="min-w-0"><p className="font-medium text-slate-900">{endpoint.name}</p><p className="truncate text-xs text-slate-500" title={endpoint.url}>{endpoint.url}</p></div>{canWrite && <Button size="sm" variant="danger" onClick={() => setDeleteWebhook(endpoint)}>Delete</Button>}</div>)}</div>
      </section>

      <Dialog open={Boolean(scheduleDraft)} onClose={() => setScheduleDraft(null)} title="New scheduled sequence" footer={scheduleDraft && <><Button variant="secondary" onClick={() => setScheduleDraft(null)}>Cancel</Button><Button type="submit" form="workflow-schedule-form" loading={createSchedule.isPending} disabled={!scheduleDraft.ruleId || !scheduleDraft.subjectId}>Create schedule</Button></>}>
        {scheduleDraft && <form id="workflow-schedule-form" className="space-y-4" onSubmit={(event) => { event.preventDefault(); createSchedule.mutate(scheduleDraft); }}>
          <Select label="Active rule" value={scheduleDraft.ruleId} onChange={(event) => setScheduleDraft({ ...scheduleDraft, ruleId: event.target.value, subjectId: "" })} options={activeRules.map((rule) => ({ value: rule.id, label: rule.name }))} placeholder="Choose a rule" />
          <Select label={subjectType ? `Linked ${subjectType}` : "Linked CRM record"} value={scheduleDraft.subjectId} onChange={(event) => setScheduleDraft({ ...scheduleDraft, subjectId: event.target.value })} options={subjectOptions} placeholder={subjectType ? `Choose a ${subjectType}` : "Choose a rule first"} disabled={!subjectType} />
          <Input label="First run" type="datetime-local" value={scheduleDraft.nextRunAt} onChange={(event) => setScheduleDraft({ ...scheduleDraft, nextRunAt: event.target.value })} required />
          <Input label="Time zone" value={scheduleDraft.timeZone} onChange={(event) => setScheduleDraft({ ...scheduleDraft, timeZone: event.target.value })} hint="IANA name, for example Asia/Kolkata" required />
          <div className="grid gap-3 sm:grid-cols-2"><Select label="Repeat" value={scheduleDraft.recurrence} onChange={(event) => setScheduleDraft({ ...scheduleDraft, recurrence: event.target.value as ScheduleDraft["recurrence"] })} options={[{ value: "once", label: "Once" }, { value: "daily", label: "Daily" }, { value: "weekly", label: "Weekly" }, { value: "monthly", label: "Monthly" }]} /><Input label="Every" type="number" min={1} max={365} value={scheduleDraft.interval} onChange={(event) => setScheduleDraft({ ...scheduleDraft, interval: event.target.value })} disabled={scheduleDraft.recurrence === "once"} /></div>
          {scheduleDraft.recurrence !== "once" && <Input label="End after (optional)" type="datetime-local" value={scheduleDraft.endsAt} onChange={(event) => setScheduleDraft({ ...scheduleDraft, endsAt: event.target.value })} />}
        </form>}
      </Dialog>

      <Dialog open={Boolean(approvalDecision)} onClose={() => setApprovalDecision(null)} title={approvalDecision?.decision === "approved" ? "Approve this sequence?" : "Reject this sequence?"} footer={approvalDecision && <><Button variant="secondary" onClick={() => setApprovalDecision(null)}>Cancel</Button><Button variant={approvalDecision.decision === "rejected" ? "danger" : "primary"} loading={decideApproval.isPending} onClick={() => decideApproval.mutate(approvalDecision)}>{approvalDecision.decision === "approved" ? "Approve" : "Reject"}</Button></>}>
        <p className="mb-4">{approvalDecision?.decision === "approved" ? "The paused sequence will continue with its next action." : "The sequence will stop and its execution history will record the rejection."}</p><Input label="Decision note (optional)" value={decisionNote} onChange={(event) => setDecisionNote(event.target.value)} />
      </Dialog>

      <Dialog open={Boolean(webhookDraft)} onClose={() => setWebhookDraft(null)} title="Add outbound webhook" footer={webhookDraft && <><Button variant="secondary" onClick={() => setWebhookDraft(null)}>Cancel</Button><Button type="submit" form="workflow-webhook-form" loading={createWebhook.isPending} disabled={!webhookDraft.name.trim() || !webhookDraft.url.trim()}>Create endpoint</Button></>}>
        {webhookDraft && <form id="workflow-webhook-form" className="space-y-4" onSubmit={(event) => { event.preventDefault(); createWebhook.mutate(webhookDraft); }}><Input label="Name" value={webhookDraft.name} onChange={(event) => setWebhookDraft({ ...webhookDraft, name: event.target.value })} required /><Input label="HTTPS URL" type="url" placeholder="https://hooks.example.com/savvy" value={webhookDraft.url} onChange={(event) => setWebhookDraft({ ...webhookDraft, url: event.target.value })} hint="Public HTTPS on port 443 only. Local and private network addresses are blocked." required /></form>}
      </Dialog>

      <Dialog open={Boolean(revealedSecret)} onClose={() => setRevealedSecret("")} title="Save the signing secret">
        <p className="mb-3 text-sm text-slate-600">This is the only time Savvy CRM will show this secret. Store it in the receiving service&apos;s secret manager.</p><Input label="Signing secret" value={revealedSecret} readOnly /><Button className="mt-3" variant="secondary" onClick={() => void navigator.clipboard.writeText(revealedSecret)}>Copy secret</Button><p className="mt-3 text-xs text-slate-500">Verify HMAC-SHA256 in X-Savvy-Signature over &lt;X-Savvy-Timestamp&gt;.&lt;raw body&gt;.</p>
      </Dialog>

      <ConfirmDialog open={Boolean(deleteSchedule)} title="Delete this schedule?" description="Future runs stop immediately. Existing execution history is kept." confirmLabel="Delete schedule" onCancel={() => setDeleteSchedule(null)} onConfirm={() => deleteSchedule && removeSchedule.mutate(deleteSchedule.id)} />
      <ConfirmDialog open={Boolean(deleteWebhook)} title="Delete this webhook endpoint?" description="Rules that still reference it will fail until they are edited. Existing delivery history is kept." confirmLabel="Delete endpoint" onCancel={() => setDeleteWebhook(null)} onConfirm={() => deleteWebhook && removeWebhook.mutate(deleteWebhook.id)} />
    </div>
  );
}
