import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Select } from "../../components/ui/Select";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";

function currency(amount: number) {
  return amount ? amount.toLocaleString(undefined, { style: "currency", currency: "USD" }) : "—";
}

/** Read-only grouping with explicit advance/edit actions. This deliberately
 * avoids drag-and-drop: each stage transition still goes through the same
 * configured API validation and close-reason form as the record page. */
export default function OpportunityBoardPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const [pipelineID, setPipelineID] = useState("");
  const pipelinesQuery = useQuery({ queryKey: ["crm.listPipelines"], queryFn: () => crm.listPipelines({}) });
  const pipelines = (pipelinesQuery.data?.pipelines ?? []).filter((pipeline) => !pipeline.archivedAt);
  useEffect(() => {
    if (!pipelineID && pipelines.length) setPipelineID(pipelines.find((pipeline) => pipeline.isDefault)?.id ?? pipelines[0].id);
  }, [pipelineID, pipelines]);
  const stagesQuery = useQuery({ queryKey: ["crm.listPipelineStages", pipelineID], queryFn: () => crm.listPipelineStages({ pipelineId: pipelineID }), enabled: !!pipelineID });
  const opportunitiesQuery = useQuery({ queryKey: ["crm.listOpportunities", "board"], queryFn: () => crm.listOpportunities({ pageSize: 500 }) });
  const stages = (stagesQuery.data?.stages ?? []).filter((stage) => !stage.archivedAt);
  const opportunities = useMemo(() => (opportunitiesQuery.data?.opportunities ?? []).filter((opportunity) => opportunity.pipelineId === pipelineID), [opportunitiesQuery.data?.opportunities, pipelineID]);
  const canWrite = hasScope("crm:opportunity:write");

  return <div>
    <PageHeader title="Opportunity board" breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} actions={<div className="flex gap-2"><Button variant="secondary" onClick={() => navigate("/opportunities")}>List view</Button>{canWrite && <Button onClick={() => navigate("/opportunities/new")}>New opportunity</Button>}</div>} />
    {pipelinesQuery.isPending && <SkeletonRows rows={4} />}
    {pipelinesQuery.error && renderQueryError(pipelinesQuery.error, { onRetry: () => pipelinesQuery.refetch(), forbiddenDescription: "You don't have permission to view opportunity pipelines." })}
    {pipelinesQuery.data && pipelines.length === 0 && <EmptyState title="No pipeline available" description="Create a pipeline and its stages before using the opportunity board." action={hasScope("crm:pipeline:write") && <Button onClick={() => navigate("/settings/pipelines")}>Configure pipelines</Button>} />}
    {pipelines.length > 0 && <>
      <div className="mb-5 max-w-sm"><Select label="Pipeline" value={pipelineID} onChange={(event) => setPipelineID(event.target.value)} options={pipelines.map((pipeline) => ({ value: pipeline.id, label: pipeline.name }))} /></div>
      {stagesQuery.isPending || opportunitiesQuery.isPending ? <SkeletonRows rows={5} /> : null}
      {stagesQuery.error && renderQueryError(stagesQuery.error, { onRetry: () => stagesQuery.refetch() })}
      {opportunitiesQuery.error && renderQueryError(opportunitiesQuery.error, { onRetry: () => opportunitiesQuery.refetch(), forbiddenDescription: "You don't have permission to view opportunities." })}
      {stagesQuery.data && opportunitiesQuery.data && stages.length === 0 && <EmptyState title="No active stages" description="Add at least one open stage to this pipeline before creating opportunities." />}
      {stagesQuery.data && opportunitiesQuery.data && stages.length > 0 && <div className="flex gap-4 overflow-x-auto pb-4">{stages.map((stage) => {
        const cards = opportunities.filter((opportunity) => opportunity.stageId === stage.id);
        return <section key={stage.id} className="w-72 shrink-0 rounded-lg bg-slate-100 p-3"><div className="mb-3 flex items-center justify-between"><h2 className="text-sm font-semibold text-slate-900">{stage.name}</h2><span className="rounded-full bg-white px-2 py-0.5 text-xs font-medium text-slate-600">{cards.length}</span></div><div className="space-y-3">{cards.map((opportunity) => <button key={opportunity.id} type="button" onClick={() => navigate(`/opportunities/${opportunity.id}`)} className="w-full rounded-md border border-slate-200 bg-white p-3 text-left shadow-sm transition hover:border-brand-300 hover:shadow"><span className="block truncate text-sm font-medium text-slate-900">{opportunity.name}</span><span className="mt-2 block text-sm text-slate-600">{currency(opportunity.amount)}</span><span className="mt-1 block text-xs text-slate-500">{opportunity.probability}% probability</span></button>)}</div>{cards.length === 0 && <p className="px-1 py-6 text-center text-xs text-slate-500">No opportunities</p>}</section>;
      })}</div>}
    </>}
  </div>;
}
