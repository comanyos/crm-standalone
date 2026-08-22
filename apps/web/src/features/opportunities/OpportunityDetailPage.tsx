import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Badge } from "../../components/ui/Badge";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { isClosedStage, stageLabel, stageTone } from "./stages";
import { ActivityTimeline } from "../../components/ActivityTimeline";
import { TasksPanel } from "../../components/TasksPanel";
import { NotesPanel } from "../../components/NotesPanel";

export default function OpportunityDetailPage() {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const opportunityQuery = useQuery({ queryKey: ["crm.getOpportunity", id], queryFn: () => crm.getOpportunity({ id }) });
  const opportunity = opportunityQuery.data?.opportunity;
  const accountQuery = useQuery({ queryKey: ["crm.getAccount", opportunity?.accountId], queryFn: () => crm.getAccount({ id: opportunity!.accountId }), enabled: !!opportunity?.accountId });
  const stageHistoryQuery = useQuery({ queryKey: ["crm.listOpportunityStageHistory", id], queryFn: () => crm.listOpportunityStageHistory({ opportunityId: id }), enabled: !!opportunity });
  const deleteMutation = useMutation({
    mutationFn: () => crm.deleteOpportunity({ id }),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: ["crm.listOpportunities"] }); toast.show({ tone: "success", message: "Opportunity deleted." }); navigate("/opportunities"); },
    onError: (error) => { setConfirmingDelete(false); toast.show({ tone: "danger", message: mutationErrorMessage(error) }); },
  });

  if (opportunityQuery.isPending) return <div><PageHeader title="Opportunity" breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} /><SkeletonRows rows={5} /></div>;
  if (opportunityQuery.error) return <div><PageHeader title="Opportunity" breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} />{renderQueryError(opportunityQuery.error, { onRetry: () => opportunityQuery.refetch(), forbiddenDescription: "You don't have permission to view this opportunity." })}</div>;
  if (!opportunity) return null;
  const canWrite = hasScope("crm:opportunity:write");
  const terminal = isClosedStage(opportunity.stage);
  const currency = opportunity.amount ? opportunity.amount.toLocaleString(undefined, { style: "currency", currency: "USD" }) : undefined;

  return <div>
    <PageHeader title={opportunity.name} breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} actions={canWrite && <>{!terminal && <Button onClick={() => navigate(`/opportunities/${id}/edit`)}>Advance or edit</Button>}<Button variant="danger" onClick={() => setConfirmingDelete(true)}>Delete</Button></>} />
    <section className="max-w-2xl rounded-lg border border-slate-200 bg-white p-4">
      <h2 className="mb-3 text-sm font-semibold text-slate-900">Details</h2>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
        <Field label="Stage" value={<Badge tone={stageTone(opportunity.stage)}>{stageLabel(opportunity.stage)}</Badge>} />
        <Field label="Amount" value={currency} />
        <Field label="Probability" value={opportunity.probability ? `${opportunity.probability}%` : undefined} />
        <Field label="Account" value={<Link className="text-brand-700 underline" to={`/accounts/${opportunity.accountId}`}>{accountQuery.data?.account?.name ?? opportunity.accountId}</Link>} />
        <Field label="Close date" value={opportunity.closeDate ? timestampDate(opportunity.closeDate).toLocaleDateString() : undefined} />
        <Field label="Close reason" value={opportunity.closeReason} />
      </dl>
    </section>
    <section className="mt-4 max-w-2xl rounded-lg border border-slate-200 bg-white p-4">
      <h2 className="mb-3 text-sm font-semibold text-slate-900">Stage history</h2>
      {stageHistoryQuery.isPending && <SkeletonRows rows={2} />}
      {stageHistoryQuery.error && renderQueryError(stageHistoryQuery.error, { onRetry: () => stageHistoryQuery.refetch(), forbiddenDescription: "You don't have permission to view this opportunity's history." })}
      {stageHistoryQuery.data?.changes.length === 0 && <p className="text-sm text-slate-500">No stage changes have been recorded yet.</p>}
      {stageHistoryQuery.data?.changes.length ? <ol className="space-y-3">{stageHistoryQuery.data.changes.map((change) => <li key={change.id} className="border-l-2 border-brand-200 pl-3 text-sm"><p className="font-medium text-slate-800">{stageLabel(change.fromStage) || "Created"} <span className="px-1 text-slate-400">→</span> {stageLabel(change.toStage)}</p><p className="mt-0.5 text-xs text-slate-500">{change.changedAt ? timestampDate(change.changedAt).toLocaleString() : "Recorded"}</p></li>)}</ol> : null}
    </section>
    <TasksPanel subjectType="opportunity" subjectId={id} />
      <NotesPanel subjectType="opportunity" subjectId={id} />
      <ActivityTimeline subjectType="opportunity" subjectId={id} />
    {terminal && <p className="mt-4 max-w-2xl rounded-md border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600">Closed opportunities are final. Create a new opportunity to pursue a new deal.</p>}
    <ConfirmDialog open={confirmingDelete} title="Delete opportunity" description={`Delete ${opportunity.name}? This cannot be undone.`} confirmLabel="Delete" tone="danger" loading={deleteMutation.isPending} onConfirm={() => deleteMutation.mutate()} onCancel={() => setConfirmingDelete(false)} />
  </div>;
}

function Field({ label, value }: { label: string; value?: ReactNode }) {
  return <div><dt className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</dt><dd className="mt-0.5 text-slate-900">{value || <span className="text-slate-400">—</span>}</dd></div>;
}
