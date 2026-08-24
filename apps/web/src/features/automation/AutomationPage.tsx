import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { WorkflowAction, WorkflowCondition, WorkflowRule } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Badge, type BadgeTone } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Table } from "../../components/ui/Table";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useToast } from "../../components/ui/Toast";

/** Automation rules (docs/WORKFLOW_AUTOMATION.md).
 *
 * Every choice this editor offers comes from DescribeWorkflowCatalog rather
 * than a list written here: triggers, comparable fields and the operators
 * each field's kind accepts are server-side allowlists, and an editor with
 * its own copy of them would let an author compose a rule that cannot be
 * saved — or worse, one that saves and silently never matches. */

const STATUS_TONE: Record<string, BadgeTone> = {
  draft: "neutral", active: "success", paused: "warning", archived: "neutral",
};

const ACTION_LABELS: Record<string, string> = {
  create_task: "Create a task", add_activity: "Log an activity", notify: "Send a notification",
};

const OPERATOR_LABELS: Record<string, string> = {
  eq: "is", neq: "is not", contains: "contains", in: "is one of",
  gt: "is greater than", lt: "is less than",
};

const ASSIGNEE_LABELS: Record<string, string> = {
  record_owner: "The record's owner", fixed_user: "A specific person",
};

const EXECUTION_TONE: Record<string, BadgeTone> = {
  succeeded: "success", failed: "danger", skipped: "neutral",
  pending: "brand", running: "brand",
};

/** The failure codes the engine records. They are bounded codes rather than
 * messages precisely so they can be rendered as sentences here. */
const FAILURE_REASONS: Record<string, string> = {
  record_gone: "The record was deleted before the rule ran.",
  no_assignee: "Nobody to assign the work to — the record has no owner.",
  action_failed: "An action could not be completed.",
  rule_version_gone: "The rule version was removed.",
  invalid_action: "The rule asks for something this version cannot do.",
};

type Draft = {
  ruleId: string;
  name: string;
  description: string;
  trigger: string;
  conditions: WorkflowCondition[];
  actions: WorkflowAction[];
};

const blankCondition = (field: string, operator: string): WorkflowCondition =>
  ({ $typeName: "crm.v1.WorkflowCondition", field, operator, value: "" }) as WorkflowCondition;

const blankAction = (): WorkflowAction => ({
  $typeName: "crm.v1.WorkflowAction", type: "create_task", title: "", body: "",
  assignee: "record_owner", assigneeId: "", priority: "medium", dueInDays: 0, activityKind: "note",
}) as WorkflowAction;

const blankDraft = (): Draft => ({
  ruleId: "", name: "", description: "", trigger: "", conditions: [], actions: [blankAction()],
});

export default function AutomationPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const canRead = hasScope("crm:workflow:read");
  const canWrite = hasScope("crm:workflow:write");

  const [draft, setDraft] = useState<Draft | null>(null);
  const [archiving, setArchiving] = useState<WorkflowRule | null>(null);
  const [history, setHistory] = useState<WorkflowRule | null>(null);

  const catalogQuery = useQuery({
    queryKey: ["crm.describeWorkflowCatalog"],
    queryFn: () => crm.describeWorkflowCatalog({}),
    enabled: canRead,
  });
  const rulesQuery = useQuery({
    queryKey: ["crm.listWorkflowRules"],
    queryFn: () => crm.listWorkflowRules({}),
    enabled: canRead,
  });
  const executionsQuery = useQuery({
    queryKey: ["crm.listWorkflowExecutions", history?.id],
    queryFn: () => crm.listWorkflowExecutions({ ruleId: history?.id ?? "" }),
    enabled: Boolean(history),
  });

  const rules = rulesQuery.data?.rules ?? [];
  const triggers = catalogQuery.data?.triggers ?? [];
  const trigger = triggers.find((candidate) => candidate.eventType === draft?.trigger);
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["crm.listWorkflowRules"] });

  /** Saving is two calls, deliberately in this order: the rule row carries
   * the name, the version carries the behaviour, and a version is what the
   * engine runs. Publishing with activate is what makes an edit live. */
  const save = useMutation({
    mutationFn: async (input: { draft: Draft; activate: boolean }) => {
      const { draft: d, activate } = input;
      const ruleId = d.ruleId
        ? (await crm.updateWorkflowRule({ id: d.ruleId, name: d.name.trim(), description: d.description.trim() }))
            .rule?.id ?? d.ruleId
        : (await crm.createWorkflowRule({ name: d.name.trim(), description: d.description.trim() }))
            .rule?.id ?? "";
      await crm.publishWorkflowRuleVersion({
        ruleId, triggerEventType: d.trigger,
        conditions: d.conditions, actions: d.actions, activate,
      });
    },
    onSuccess: (_data, input) => {
      setDraft(null);
      void refresh();
      toast.show({
        tone: "success",
        message: input.activate
          ? "Rule published and switched on."
          : "Version saved. It will not run until you switch it on.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const setStatus = useMutation({
    mutationFn: (input: { id: string; status: string }) => crm.setWorkflowRuleStatus(input),
    onSuccess: (_data, input) => {
      setArchiving(null);
      void refresh();
      toast.show({ tone: "success", message: `Rule ${input.status}.` });
    },
    onError: (error) => {
      setArchiving(null);
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    },
  });

  if (!canRead) {
    return <div>
      <PageHeader title="Automation" />
      <EmptyState title="Automation access required"
        description="Rules are managed by CRM managers and administrators." />
    </div>;
  }

  const startNew = () => setDraft({ ...blankDraft(), trigger: triggers[0]?.eventType ?? "" });

  const startEdit = async (rule: WorkflowRule) => {
    const detail = await crm.getWorkflowRule({ id: rule.id });
    const version = detail.activeVersion;
    setDraft({
      ruleId: rule.id, name: rule.name, description: rule.description,
      trigger: version?.triggerEventType ?? triggers[0]?.eventType ?? "",
      conditions: version?.conditions.map((condition) => ({ ...condition })) ?? [],
      actions: version?.actions.map((action) => ({ ...action })) ?? [blankAction()],
    });
  };

  const patch = (changes: Partial<Draft>) => setDraft((current) => current && { ...current, ...changes });

  const patchCondition = (index: number, changes: Partial<WorkflowCondition>) =>
    setDraft((current) => current && {
      ...current,
      conditions: current.conditions.map((condition, i) => i === index ? { ...condition, ...changes } : condition),
    });

  const patchAction = (index: number, changes: Partial<WorkflowAction>) =>
    setDraft((current) => current && {
      ...current,
      actions: current.actions.map((action, i) => i === index ? { ...action, ...changes } : action),
    });

  return <div>
    <PageHeader title="Automation"
      actions={canWrite && triggers.length > 0 && <Button onClick={startNew}>New rule</Button>} />
    <p className="mb-5 max-w-3xl text-sm text-slate-600">
      Rules watch for one thing happening and do a fixed set of things in response. Editing a
      running rule saves a new version rather than changing what is already running, so a rule&apos;s
      history always says exactly what ran.
    </p>

    {rulesQuery.isPending && <SkeletonRows rows={4} />}
    {rulesQuery.error && renderQueryError(rulesQuery.error, {
      onRetry: () => rulesQuery.refetch(),
      forbiddenDescription: "You don't have permission to view automation rules.",
    })}
    {rulesQuery.data && rules.length === 0 && <EmptyState title="No automation rules"
      description="A rule can create follow-up work the moment a lead arrives or a deal moves stage."
      action={canWrite && <Button onClick={startNew}>New rule</Button>} />}

    {rules.length > 0 && <Table caption="Automation rules" rows={rules} getRowKey={(rule) => rule.id} columns={[
      {
        key: "name", header: "Rule", render: (rule) => <>
          <div className="font-medium">{rule.name}</div>
          {rule.description && <div className="text-xs text-slate-500">{rule.description}</div>}
        </>,
      },
      {
        key: "status", header: "Status",
        render: (rule) => <Badge tone={STATUS_TONE[rule.status] ?? "neutral"}>{rule.status}</Badge>,
      },
      {
        key: "actions", header: "Actions", headerHidden: true, render: (rule) => <div className="flex flex-wrap gap-2">
          <Button variant="secondary" onClick={() => setHistory(rule)}>
            History<span className="sr-only"> for {rule.name}</span>
          </Button>
          {canWrite && rule.status !== "archived" && <>
            <Button variant="secondary" onClick={() => void startEdit(rule)}>
              Edit<span className="sr-only"> {rule.name}</span>
            </Button>
            {rule.status === "active"
              ? <Button variant="secondary"
                  onClick={() => setStatus.mutate({ id: rule.id, status: "paused" })}>
                  Pause<span className="sr-only"> {rule.name}</span>
                </Button>
              : rule.activeVersionId && <Button variant="secondary"
                  onClick={() => setStatus.mutate({ id: rule.id, status: "active" })}>
                  Switch on<span className="sr-only"> {rule.name}</span>
                </Button>}
            <Button variant="danger" onClick={() => setArchiving(rule)}>
              Archive<span className="sr-only"> {rule.name}</span>
            </Button>
          </>}
        </div>,
      },
    ]} />}

    <Dialog open={Boolean(draft)} onClose={() => setDraft(null)}
      title={draft?.ruleId ? "Edit rule" : "New rule"}
      footer={draft && <div className="flex flex-wrap justify-end gap-2">
        <Button variant="secondary" onClick={() => setDraft(null)}>Cancel</Button>
        <Button variant="secondary" disabled={save.isPending}
          onClick={() => save.mutate({ draft, activate: false })}>Save without switching on</Button>
        <Button disabled={save.isPending} onClick={() => save.mutate({ draft, activate: true })}>
          Save and switch on
        </Button>
      </div>}>
      {draft && <div className="space-y-4">
        <Input label="Name" value={draft.name} onChange={(event) => patch({ name: event.target.value })} />
        <Input label="Description" value={draft.description}
          onChange={(event) => patch({ description: event.target.value })} />
        <Select label="When this happens" value={draft.trigger}
          onChange={(event) => patch({ trigger: event.target.value, conditions: [] })}
          options={triggers.map((candidate) => ({
            value: candidate.eventType, label: triggerLabel(candidate.eventType),
          }))} />

        <fieldset className="space-y-3 rounded-md border border-slate-200 p-3">
          <legend className="px-1 text-sm font-medium text-slate-700">Only when</legend>
          {draft.conditions.length === 0 &&
            <p className="text-sm text-slate-500">Every {triggerLabel(draft.trigger).toLowerCase()} matches.</p>}
          {draft.conditions.map((condition, index) => {
            const field = trigger?.fields.find((candidate) => candidate.name === condition.field);
            return <div key={index} className="grid gap-2 sm:grid-cols-3">
              <Select label={`Field ${index + 1}`} value={condition.field}
                onChange={(event) => patchCondition(index, {
                  field: event.target.value,
                  operator: trigger?.fields.find((c) => c.name === event.target.value)?.operators[0] ?? "eq",
                })}
                options={(trigger?.fields ?? []).map((candidate) => ({
                  value: candidate.name, label: fieldLabel(candidate.name),
                }))} />
              <Select label={`Comparison ${index + 1}`} value={condition.operator}
                onChange={(event) => patchCondition(index, { operator: event.target.value })}
                options={(field?.operators ?? []).map((operator) => ({
                  value: operator, label: OPERATOR_LABELS[operator] ?? operator,
                }))} />
              <div className="flex items-end gap-2">
                <div className="flex-1">
                  <Input label={`Value ${index + 1}`} value={condition.value}
                    hint={condition.operator === "in" ? "Comma-separated" : undefined}
                    onChange={(event) => patchCondition(index, { value: event.target.value })} />
                </div>
                <Button variant="secondary" onClick={() => patch({
                  conditions: draft.conditions.filter((_, i) => i !== index),
                })}>Remove<span className="sr-only"> condition {index + 1}</span></Button>
              </div>
            </div>;
          })}
          {trigger && draft.conditions.length < (catalogQuery.data?.maxConditions ?? 10) &&
            <Button variant="secondary" onClick={() => patch({
              conditions: [...draft.conditions, blankCondition(
                trigger.fields[0]?.name ?? "", trigger.fields[0]?.operators[0] ?? "eq")],
            })}>Add a condition</Button>}
        </fieldset>

        <fieldset className="space-y-3 rounded-md border border-slate-200 p-3">
          <legend className="px-1 text-sm font-medium text-slate-700">Do this</legend>
          {draft.actions.map((action, index) => <div key={index} className="space-y-2 border-t border-slate-100 pt-3 first:border-0 first:pt-0">
            <Select label={`Action ${index + 1}`} value={action.type}
              onChange={(event) => patchAction(index, { type: event.target.value })}
              options={(catalogQuery.data?.actionTypes ?? []).map((type) => ({
                value: type, label: ACTION_LABELS[type] ?? type,
              }))} />
            {action.type !== "add_activity" &&
              <Input label={`Title ${index + 1}`} value={action.title}
                onChange={(event) => patchAction(index, { title: event.target.value })} />}
            <Input label={action.type === "add_activity" ? `Note ${index + 1}` : `Details ${index + 1}`}
              value={action.body} onChange={(event) => patchAction(index, { body: event.target.value })} />
            {action.type === "add_activity" &&
              <Input label={`Activity kind ${index + 1}`} value={action.activityKind}
                onChange={(event) => patchAction(index, { activityKind: event.target.value })} />}
            {action.type !== "add_activity" && <>
              <Select label={`Assign to ${index + 1}`} value={action.assignee}
                onChange={(event) => patchAction(index, { assignee: event.target.value })}
                options={(catalogQuery.data?.assigneeSelectors ?? []).map((selector) => ({
                  value: selector, label: ASSIGNEE_LABELS[selector] ?? selector,
                }))} />
              {action.assignee === "fixed_user" &&
                <Input label={`Person ${index + 1}`} value={action.assigneeId}
                  onChange={(event) => patchAction(index, { assigneeId: event.target.value })} />}
            </>}
            {action.type === "create_task" &&
              <Input label={`Due in days ${index + 1}`} type="number" min={0} value={action.dueInDays}
                onChange={(event) => patchAction(index, { dueInDays: Number(event.target.value) })} />}
            {draft.actions.length > 1 &&
              <Button variant="secondary" onClick={() => patch({
                actions: draft.actions.filter((_, i) => i !== index),
              })}>Remove<span className="sr-only"> action {index + 1}</span></Button>}
          </div>)}
          {draft.actions.length < (catalogQuery.data?.maxActions ?? 10) &&
            <Button variant="secondary"
              onClick={() => patch({ actions: [...draft.actions, blankAction()] })}>Add an action</Button>}
        </fieldset>
      </div>}
    </Dialog>

    <Dialog open={Boolean(history)} onClose={() => setHistory(null)}
      title={history ? `History — ${history.name}` : "History"}>
      {executionsQuery.isPending && <SkeletonRows rows={3} />}
      {executionsQuery.data?.executions.length === 0 &&
        <p className="text-sm text-slate-600">This rule has not been considered for any event yet.</p>}
      {(executionsQuery.data?.executions.length ?? 0) > 0 &&
        <Table caption="Rule history" rows={executionsQuery.data?.executions ?? []}
          getRowKey={(execution) => execution.id} columns={[
            { key: "event", header: "Event", render: (execution) => execution.sourceEventType },
            {
              key: "status", header: "Outcome", render: (execution) => <>
                <Badge tone={EXECUTION_TONE[execution.status] ?? "neutral"}>{execution.status}</Badge>
                {execution.status === "skipped" &&
                  <div className="text-xs text-slate-500">Its conditions did not hold.</div>}
                {execution.errorCategory &&
                  <div className="text-xs text-slate-500">
                    {FAILURE_REASONS[execution.errorCategory] ?? execution.errorCategory}
                  </div>}
              </>,
            },
            { key: "attempts", header: "Attempts", render: (execution) => String(execution.attempts) },
          ]} />}
    </Dialog>

    <ConfirmDialog open={Boolean(archiving)} title="Archive this rule?"
      description={`"${archiving?.name}" stops running. Archiving is permanent: a retired rule cannot be switched back on, and reinstating it means writing a new one. Its history is kept.`}
      confirmLabel="Archive" onCancel={() => setArchiving(null)}
      onConfirm={() => archiving && setStatus.mutate({ id: archiving.id, status: "archived" })} />
  </div>;
}

/** Event types are read by rule authors, not by machines, on this screen. */
function triggerLabel(eventType: string) {
  switch (eventType) {
    case "lead.created": return "A lead is created";
    case "opportunity.stage_changed": return "An opportunity changes stage";
    case "opportunity.closed_won": return "An opportunity is won";
    default: return eventType;
  }
}

function fieldLabel(name: string) {
  return name.replace(/_id$/, "").replace(/_/g, " ").replace(/^./, (c) => c.toUpperCase());
}
