import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError, mutationErrorMessage, refusalMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

export default function PipelinesPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [creatingPipeline, setCreatingPipeline] = useState(false);
  const [pipelineName, setPipelineName] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [stageName, setStageName] = useState("");
  const [stagePosition, setStagePosition] = useState("0");
  const [stageProbability, setStageProbability] = useState("0");
  const [terminalType, setTerminalType] = useState("open");
  const [requiresCloseReason, setRequiresCloseReason] = useState(false);
  const [editingPipeline, setEditingPipeline] = useState(false);
  const [editingStageID, setEditingStageID] = useState("");
  // The stage an administrator has asked to archive, held while the
  // confirmation is open. Archiving is refused server-side while open deals
  // stand on the stage, so the confirmation promises retention, not safety.
  // Whether the pipeline archive confirmation is open. Like the stage one
  // below, this is a ConfirmDialog rather than window.confirm: a browser that
  // has been told to stop showing this page's dialogs makes confirm() return
  // false with no prompt, so the button silently stops working — on an
  // archival that is the difference between "declined" and "never asked".
  const [archivingPipeline, setArchivingPipeline] = useState(false);
  const [archivingStage, setArchivingStage] = useState<NonNullable<typeof stageQuery.data>["stages"][number] | null>(null);
  const [pipelineIsDefault, setPipelineIsDefault] = useState(false);
  const canWrite = hasScope("crm:pipeline:write");

  const pipelinesQuery = useQuery({ queryKey: ["crm.listPipelines"], queryFn: () => crm.listPipelines({}) });
  const pipelines = useMemo(() => pipelinesQuery.data?.pipelines ?? [], [pipelinesQuery.data?.pipelines]);
  const selectedPipeline = pipelines.find((pipeline) => pipeline.id === selectedID);
  useEffect(() => {
    if (!selectedID && pipelines[0]) setSelectedID(pipelines[0].id);
  }, [pipelines, selectedID]);
  const stageQuery = useQuery({ queryKey: ["crm.listPipelineStages", selectedID], queryFn: () => crm.listPipelineStages({ pipelineId: selectedID }), enabled: !!selectedID });

  const createPipeline = useMutation({
    mutationFn: () => crm.createPipeline({ pipeline: { name: pipelineName.trim() } }),
    onSuccess: (result) => { setCreatingPipeline(false); setPipelineName(""); setSelectedID(result.pipeline?.id ?? ""); queryClient.invalidateQueries({ queryKey: ["crm.listPipelines"] }); toast.show({ tone: "success", message: "Pipeline created." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const createStage = useMutation({
    mutationFn: () => crm.createPipelineStage({ stage: { pipelineId: selectedID, name: stageName.trim(), position: Number(stagePosition), probability: Number(stageProbability), terminalType, requiresCloseReason } }),
    onSuccess: () => { setStageName(""); setStagePosition(String((stageQuery.data?.stages.length ?? 0) + 1)); setStageProbability("0"); setTerminalType("open"); setRequiresCloseReason(false); queryClient.invalidateQueries({ queryKey: ["crm.listPipelineStages", selectedID] }); toast.show({ tone: "success", message: "Stage created." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const updatePipeline = useMutation({
    mutationFn: (pipeline: NonNullable<typeof selectedPipeline>) => crm.updatePipeline({ pipeline }),
    onSuccess: () => { setEditingPipeline(false); queryClient.invalidateQueries({ queryKey: ["crm.listPipelines"] }); toast.show({ tone: "success", message: "Pipeline updated." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const updateStage = useMutation({
    mutationFn: (stage: NonNullable<typeof stageQuery.data>["stages"][number]) => crm.updatePipelineStage({ stage }),
    onSuccess: () => { setEditingStageID(""); queryClient.invalidateQueries({ queryKey: ["crm.listPipelineStages", selectedID] }); toast.show({ tone: "success", message: "Stage updated." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const archivePipeline = useMutation({
    mutationFn: (pipeline: NonNullable<typeof selectedPipeline>) => crm.updatePipeline({ pipeline: { ...pipeline, isDefault: false, archivedAt: timestampFromDate(new Date()) } }),
    onSuccess: () => { setArchivingPipeline(false); setSelectedID(""); queryClient.invalidateQueries({ queryKey: ["crm.listPipelines"] }); toast.show({ tone: "success", message: "Pipeline archived. Historical records are retained." }); },
    // refusalMessage, not mutationErrorMessage: the server refuses this while
    // open deals still stand on the pipeline, and names them. That sentence is
    // the whole value of the refusal.
    onError: (error) => { setArchivingPipeline(false); toast.show({ tone: "danger", message: refusalMessage(error) }); },
  });

  const archiveStage = useMutation({
    mutationFn: (stage: NonNullable<typeof stageQuery.data>["stages"][number]) =>
      crm.updatePipelineStage({ stage: { ...stage, archivedAt: timestampFromDate(new Date()) } }),
    onSuccess: () => { setArchivingStage(null); queryClient.invalidateQueries({ queryKey: ["crm.listPipelineStages", selectedID] }); toast.show({ tone: "success", message: "Stage archived. Deals already closed on it are retained." }); },
    // The server refuses this while open deals still stand on the stage, and
    // names them — see refusalMessage.
    onError: (error) => { setArchivingStage(null); toast.show({ tone: "danger", message: refusalMessage(error) }); },
  });

  return <div>
    <PageHeader title="Pipelines" actions={canWrite && <Button onClick={() => setCreatingPipeline(true)}>New pipeline</Button>} />
    <p className="mb-5 max-w-3xl text-sm text-slate-600">Configure future sales processes and their reporting semantics. Current opportunities remain on the established default stage flow until the pipeline-ID migration is completed.</p>
    {pipelinesQuery.isPending && <SkeletonRows rows={4} />}
    {pipelinesQuery.error && renderQueryError(pipelinesQuery.error, { onRetry: () => pipelinesQuery.refetch(), forbiddenDescription: "You don't have permission to manage pipelines." })}
    {pipelinesQuery.data && pipelines.length === 0 && <EmptyState title="No pipelines configured" description="Create a pipeline before defining its stages." action={canWrite && <Button onClick={() => setCreatingPipeline(true)}>New pipeline</Button>} />}
    {pipelines.length > 0 && <div className="grid gap-6 lg:grid-cols-[15rem_1fr]">
      <aside className="rounded-lg border border-slate-200 bg-white p-2"><ul className="space-y-1">{pipelines.map((pipeline) => <li key={pipeline.id}><button type="button" onClick={() => setSelectedID(pipeline.id)} className={`w-full rounded-md px-3 py-2 text-left text-sm ${selectedID === pipeline.id ? "bg-brand-50 font-medium text-brand-700" : "text-slate-700 hover:bg-slate-50"}`}>{pipeline.name}{pipeline.isDefault && <span className="ml-2 text-xs">Default</span>}{pipeline.archivedAt && <span className="ml-2 text-xs text-slate-500">Archived</span>}</button></li>)}</ul></aside>
      <section className="rounded-lg border border-slate-200 bg-white p-4">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3"><div><h2 className="text-sm font-semibold text-slate-900">{selectedPipeline?.name ?? "Stages"}</h2><p className="text-xs text-slate-500">{selectedPipeline?.isDefault ? "Default pipeline for new opportunities." : "Not selected as the default pipeline."}</p></div>{canWrite && selectedPipeline && !selectedPipeline.archivedAt && <div className="flex gap-2"><Button size="sm" variant="secondary" onClick={() => { setPipelineName(selectedPipeline.name); setPipelineIsDefault(selectedPipeline.isDefault); setEditingPipeline(true); }} aria-label={`Edit ${selectedPipeline.name}`}>Edit</Button><Button size="sm" variant="secondary" onClick={() => setArchivingPipeline(true)} loading={archivePipeline.isPending} aria-label={`Archive pipeline ${selectedPipeline.name}`}>Archive</Button></div>}</div>
        {stageQuery.isPending && <SkeletonRows rows={3}/>}{stageQuery.error && renderQueryError(stageQuery.error, { onRetry: () => stageQuery.refetch() })}
        <ol className="mb-5 space-y-2">{stageQuery.data?.stages.map((stage) => <li key={stage.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-slate-50 px-3 py-2 text-sm"><span><span className="mr-2 text-slate-500">{stage.position + 1}.</span>{stage.name}{stage.archivedAt && <span className="ml-2 text-xs text-slate-500">Archived</span>}</span><span className="flex items-center gap-3 text-slate-500">{stage.probability}% · {stage.terminalType}{canWrite && !selectedPipeline?.archivedAt && !stage.archivedAt && <><Button size="sm" variant="secondary" onClick={() => { setStageName(stage.name); setStagePosition(String(stage.position)); setStageProbability(String(stage.probability)); setTerminalType(stage.terminalType); setRequiresCloseReason(stage.requiresCloseReason); setEditingStageID(stage.id); }} aria-label={`Edit stage ${stage.name}`}>Edit</Button><Button size="sm" variant="secondary" onClick={() => setArchivingStage(stage)} aria-label={`Archive stage ${stage.name}`}>Archive</Button></>}</span></li>)}</ol>{stageQuery.data?.stages.length === 0 && <p className="mb-5 text-sm text-slate-500">No stages yet.</p>}{canWrite && !selectedPipeline?.archivedAt && <form onSubmit={(event) => { event.preventDefault(); createStage.mutate(); }} className="grid gap-3 border-t border-slate-200 pt-4 md:grid-cols-4"><Input label="Stage name" value={stageName} onChange={(event) => setStageName(event.target.value)} required /><Input label="Position" type="number" min={0} value={stagePosition} onChange={(event) => setStagePosition(event.target.value)} required /><Input label="Probability (%)" type="number" min={0} max={100} value={stageProbability} onChange={(event) => setStageProbability(event.target.value)} required /><Select label="Outcome" value={terminalType} onChange={(event) => { const value = event.target.value; setTerminalType(value); if (value === "won") setStageProbability("100"); if (value === "lost") setStageProbability("0"); }} options={[{ value: "open", label: "Open" }, { value: "won", label: "Won" }, { value: "lost", label: "Lost" }]} /><label className="flex items-center gap-2 text-sm text-slate-700 md:col-span-3"><input type="checkbox" checked={requiresCloseReason} onChange={(event) => setRequiresCloseReason(event.target.checked)} /> Require a close reason</label><div><Button type="submit" loading={createStage.isPending}>Add stage</Button></div></form>}</section>
    </div>}
    <Dialog open={creatingPipeline} onClose={() => setCreatingPipeline(false)} title="New pipeline" footer={<><Button variant="secondary" onClick={() => setCreatingPipeline(false)} disabled={createPipeline.isPending}>Cancel</Button><Button onClick={() => createPipeline.mutate()} loading={createPipeline.isPending} disabled={!pipelineName.trim()}>Create pipeline</Button></>}><Input label="Pipeline name" value={pipelineName} onChange={(event) => setPipelineName(event.target.value)} placeholder="e.g. Enterprise sales" required /></Dialog>
    <Dialog open={editingPipeline} onClose={() => setEditingPipeline(false)} title="Edit pipeline" footer={<><Button variant="secondary" onClick={() => setEditingPipeline(false)} disabled={updatePipeline.isPending}>Cancel</Button><Button onClick={() => selectedPipeline && updatePipeline.mutate({ ...selectedPipeline, name: pipelineName.trim() || selectedPipeline.name, isDefault: pipelineIsDefault })} loading={updatePipeline.isPending}>Save changes</Button></>}><Input label="Pipeline name" value={pipelineName} onChange={(event) => setPipelineName(event.target.value)} /><label className="mt-4 flex items-center gap-2 text-sm text-slate-700"><input type="checkbox" checked={pipelineIsDefault} onChange={(event) => setPipelineIsDefault(event.target.checked)} /> Use as the default pipeline</label><p className="mt-2 text-xs text-slate-500">There can be one active default pipeline per organization.</p></Dialog>
    {stageQuery.data?.stages.map((stage) => <Dialog key={stage.id} open={editingStageID === stage.id} onClose={() => setEditingStageID("")} title={`Edit ${stage.name}`} footer={<><Button variant="secondary" onClick={() => setEditingStageID("")} disabled={updateStage.isPending}>Cancel</Button><Button onClick={() => updateStage.mutate({ ...stage, name: stageName.trim() || stage.name, position: Number(stagePosition), probability: Number(stageProbability), terminalType, requiresCloseReason })} loading={updateStage.isPending}>Save changes</Button></>}><Input label="Stage name" value={stageName} onChange={(event) => setStageName(event.target.value)} /><div className="mt-3 grid grid-cols-2 gap-3"><Input label="Position" type="number" min={0} value={stagePosition} onChange={(event) => setStagePosition(event.target.value)} /><Input label="Probability (%)" type="number" min={0} max={100} value={stageProbability} onChange={(event) => setStageProbability(event.target.value)} /></div><div className="mt-3"><Select label="Outcome" value={terminalType} onChange={(event) => setTerminalType(event.target.value)} options={[{ value: "open", label: "Open" }, { value: "won", label: "Won" }, { value: "lost", label: "Lost" }]} /></div><label className="mt-3 flex items-center gap-2 text-sm text-slate-700"><input type="checkbox" checked={requiresCloseReason} onChange={(event) => setRequiresCloseReason(event.target.checked)} /> Require a close reason</label></Dialog>)}
    <ConfirmDialog
      open={archivingPipeline}
      title={selectedPipeline ? `Archive ${selectedPipeline.name}?` : ""}
      description="An archived pipeline stops being a destination for new deals and remains available for historical reporting. Any open deals still on it must be moved to another pipeline first."
      confirmLabel="Archive pipeline"
      loading={archivePipeline.isPending}
      onConfirm={() => { if (selectedPipeline) archivePipeline.mutate(selectedPipeline); }}
      onCancel={() => setArchivingPipeline(false)}
    />
    <ConfirmDialog
      open={!!archivingStage}
      title={archivingStage ? `Archive ${archivingStage.name}?` : ""}
      description="New deals cannot enter an archived stage and it stops being a destination. Deals already closed on it keep it, so historical reporting still explains itself. Open deals must be moved to another stage first."
      confirmLabel="Archive stage"
      loading={archiveStage.isPending}
      onConfirm={() => archivingStage && archiveStage.mutate(archivingStage)}
      onCancel={() => setArchivingStage(null)}
    />
  </div>;
}
