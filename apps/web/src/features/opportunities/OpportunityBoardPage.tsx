import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Opportunity, PipelineStage } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { checkStageMove, legalTargets } from "./transitions";

function currency(amount: number) {
  return amount ? amount.toLocaleString(undefined, { style: "currency", currency: "USD" }) : "—";
}

/** A move the user has asked for and the server has not yet answered. The
 * card renders in its destination column while this is set, so the board
 * feels immediate without the query cache being edited behind the server's
 * back — clearing it on failure puts the card back where it really is. */
type PendingMove = { opportunityID: string; toStageID: string };

/** A closing move waiting on the reason the destination stage requires. */
type ClosePrompt = { opportunity: Opportunity; to: PipelineStage };

/** Opportunity board with validated drag-and-drop.
 *
 * Dragging is the pointer affordance, not the mechanism. Every move — dropped
 * or chosen from a card's "Move" menu — goes through {@link moveTo}, which
 * sends the same UpdateOpportunity call the edit form does, so the configured
 * stage machine, the close-reason rule and the optimistic-concurrency check
 * apply identically. `transitions.ts` mirrors those rules well enough to grey
 * out an illegal column before the user drops on it; the server still decides.
 *
 * The "Move" menu is not a fallback. Native HTML5 drag events reach neither
 * keyboard nor touch users, so the menu is the accessible path and the drag is
 * the shortcut, both calling one function. */
export default function OpportunityBoardPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();

  const [pipelineID, setPipelineID] = useState("");
  const [draggingID, setDraggingID] = useState("");
  const [dropTargetID, setDropTargetID] = useState("");
  const [pending, setPending] = useState<PendingMove | null>(null);
  const [closePrompt, setClosePrompt] = useState<ClosePrompt | null>(null);
  const [closeReason, setCloseReason] = useState("");
  const [menuFor, setMenuFor] = useState("");

  const pipelinesQuery = useQuery({ queryKey: ["crm.listPipelines"], queryFn: () => crm.listPipelines({}) });
  const pipelines = (pipelinesQuery.data?.pipelines ?? []).filter((pipeline) => !pipeline.archivedAt);
  useEffect(() => {
    if (!pipelineID && pipelines.length) setPipelineID(pipelines.find((pipeline) => pipeline.isDefault)?.id ?? pipelines[0].id);
  }, [pipelineID, pipelines]);

  const stagesQuery = useQuery({ queryKey: ["crm.listPipelineStages", pipelineID], queryFn: () => crm.listPipelineStages({ pipelineId: pipelineID }), enabled: !!pipelineID });
  const opportunitiesQuery = useQuery({ queryKey: ["crm.listOpportunities", "board"], queryFn: () => crm.listOpportunities({ pageSize: 500 }) });
  const stages = useMemo(() => (stagesQuery.data?.stages ?? []).filter((stage) => !stage.archivedAt), [stagesQuery.data?.stages]);
  const opportunities = useMemo(
    () => (opportunitiesQuery.data?.opportunities ?? []).filter((opportunity) => opportunity.pipelineId === pipelineID),
    [opportunitiesQuery.data?.opportunities, pipelineID],
  );
  const canWrite = hasScope("crm:opportunity:write");
  const stageByID = useMemo(() => new Map(stages.map((stage) => [stage.id, stage])), [stages]);

  const move = useMutation({
    mutationFn: async ({ opportunity, to, reason, closes }: { opportunity: Opportunity; to: PipelineStage; reason: string; closes: boolean }) => {
      return (
        await crm.updateOpportunity({
          opportunity: {
            ...opportunity,
            stageId: to.id,
            closeReason: closes ? reason : opportunity.closeReason,
            // The server stamps close_date on the closing write and refuses a
            // client-supplied one, so it must be absent on the transition that
            // closes and preserved on every other.
            closeDate: closes ? undefined : opportunity.closeDate,
            // Sent back verbatim because UpdateOpportunity replaces the custom
            // field document rather than merging it — omitting this wipes every
            // value on the record.
            customFields: opportunity.customFields,
            // The version the card was rendered from. A board left open while
            // someone else edits the deal gets a conflict, not a silent
            // overwrite.
            updatedAt: opportunity.updatedAt,
          },
        })
      ).opportunity;
    },
    onSuccess: (saved) => {
      toast.show({ tone: "success", message: `${saved?.name ?? "Opportunity"} moved to ${stageByID.get(saved?.stageId ?? "")?.name ?? "its new stage"}.` });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
    onSettled: () => {
      setPending(null);
      queryClient.invalidateQueries({ queryKey: ["crm.listOpportunities"] });
    },
  });

  /** The one path every move takes, from a drop or from the keyboard menu. */
  function moveTo(opportunity: Opportunity, to: PipelineStage) {
    setMenuFor("");
    if (move.isPending) return;
    const from = stageByID.get(opportunity.stageId);
    if (!from) {
      // The deal is standing on a stage this pipeline no longer offers —
      // archived out from under it. The server would refuse every write, so
      // say that rather than firing a request that cannot succeed.
      toast.show({ tone: "danger", message: `${opportunity.name} is on a stage that is no longer active. Open the record to fix it.` });
      return;
    }
    const check = checkStageMove(from, to);
    if (!check.allowed) {
      toast.show({ tone: "warning", message: check.reason });
      return;
    }
    if (check.noop) return;
    if (check.needsCloseReason) {
      setCloseReason("");
      setClosePrompt({ opportunity, to });
      return;
    }
    setPending({ opportunityID: opportunity.id, toStageID: to.id });
    move.mutate({ opportunity, to, reason: "", closes: check.closes });
  }

  function confirmClose() {
    if (!closePrompt || !closeReason.trim()) return;
    const { opportunity, to } = closePrompt;
    setClosePrompt(null);
    setPending({ opportunityID: opportunity.id, toStageID: to.id });
    move.mutate({ opportunity, to, reason: closeReason.trim(), closes: true });
  }

  const dragging = draggingID ? opportunities.find((opportunity) => opportunity.id === draggingID) : undefined;
  const draggingFrom = dragging ? stageByID.get(dragging.stageId) : undefined;
  /** Whether a column may receive the card currently under the pointer. */
  function acceptsDrag(stage: PipelineStage) {
    if (!dragging || !draggingFrom) return false;
    const check = checkStageMove(draggingFrom, stage);
    return check.allowed && !check.noop;
  }

  function cardsFor(stage: PipelineStage) {
    return opportunities.filter((opportunity) => {
      if (pending?.opportunityID === opportunity.id) return pending.toStageID === stage.id;
      return opportunity.stageId === stage.id;
    });
  }

  return <div>
    <PageHeader title="Opportunity board" breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} actions={<div className="flex gap-2"><Button variant="secondary" onClick={() => navigate("/opportunities")}>List view</Button>{canWrite && <Button onClick={() => navigate("/opportunities/new")}>New opportunity</Button>}</div>} />
    {pipelinesQuery.isPending && <SkeletonRows rows={4} />}
    {pipelinesQuery.error && renderQueryError(pipelinesQuery.error, { onRetry: () => pipelinesQuery.refetch(), forbiddenDescription: "You don't have permission to view opportunity pipelines." })}
    {pipelinesQuery.data && pipelines.length === 0 && <EmptyState title="No pipeline available" description="Create a pipeline and its stages before using the opportunity board." action={hasScope("crm:pipeline:write") && <Button onClick={() => navigate("/settings/pipelines")}>Configure pipelines</Button>} />}
    {pipelines.length > 0 && <>
      <div className="mb-5 max-w-sm"><Select label="Pipeline" value={pipelineID} onChange={(event) => setPipelineID(event.target.value)} options={pipelines.map((pipeline) => ({ value: pipeline.id, label: pipeline.name }))} /></div>
      {canWrite && stages.length > 0 && <p className="mb-3 text-xs text-slate-500">Drag a card to an eligible stage, or use a card&rsquo;s Move button. Deals advance one stage at a time and can be closed from any open stage.</p>}
      {stagesQuery.isPending || opportunitiesQuery.isPending ? <SkeletonRows rows={5} /> : null}
      {stagesQuery.error && renderQueryError(stagesQuery.error, { onRetry: () => stagesQuery.refetch() })}
      {opportunitiesQuery.error && renderQueryError(opportunitiesQuery.error, { onRetry: () => opportunitiesQuery.refetch(), forbiddenDescription: "You don't have permission to view opportunities." })}
      {stagesQuery.data && opportunitiesQuery.data && stages.length === 0 && <EmptyState title="No active stages" description="Add at least one open stage to this pipeline before creating opportunities." />}
      {stagesQuery.data && opportunitiesQuery.data && stages.length > 0 && <div className="flex gap-4 overflow-x-auto pb-4">{stages.map((stage) => {
        const cards = cardsFor(stage);
        const eligible = acceptsDrag(stage);
        const refusing = !!dragging && !eligible && dragging.stageId !== stage.id;
        return <section
          key={stage.id}
          aria-label={`${stage.name}, ${cards.length} ${cards.length === 1 ? "opportunity" : "opportunities"}`}
          onDragOver={(event) => { if (eligible) { event.preventDefault(); setDropTargetID(stage.id); } }}
          onDragLeave={() => setDropTargetID((current) => (current === stage.id ? "" : current))}
          onDrop={(event) => {
            event.preventDefault();
            setDropTargetID("");
            const dropped = opportunities.find((opportunity) => opportunity.id === event.dataTransfer.getData("text/plain"));
            if (dropped) moveTo(dropped, stage);
          }}
          className={`w-72 shrink-0 rounded-lg p-3 transition ${dropTargetID === stage.id ? "bg-brand-50 ring-2 ring-brand-400" : refusing ? "bg-slate-100 opacity-50" : "bg-slate-100"}`}
        >
          <div className="mb-3 flex items-center justify-between"><h2 className="text-sm font-semibold text-slate-900">{stage.name}</h2><span className="rounded-full bg-white px-2 py-0.5 text-xs font-medium text-slate-600">{cards.length}</span></div>
          <div className="space-y-3">{cards.map((opportunity) => {
            const from = stageByID.get(opportunity.stageId);
            const targets = canWrite && from ? legalTargets(from, stages) : [];
            const inFlight = pending?.opportunityID === opportunity.id;
            const frozen = move.isPending;
            return <div
              key={opportunity.id}
              draggable={canWrite && targets.length > 0 && !frozen}
              onDragStart={(event) => { event.dataTransfer.setData("text/plain", opportunity.id); event.dataTransfer.effectAllowed = "move"; setDraggingID(opportunity.id); }}
              onDragEnd={() => { setDraggingID(""); setDropTargetID(""); }}
              className={`rounded-md border border-slate-200 bg-white shadow-sm transition ${inFlight ? "opacity-60" : "hover:border-brand-300 hover:shadow"} ${draggingID === opportunity.id ? "opacity-40" : ""}`}
            >
              <button type="button" onClick={() => navigate(`/opportunities/${opportunity.id}`)} className="w-full p-3 text-left">
                <span className="block truncate text-sm font-medium text-slate-900">{opportunity.name}</span>
                <span className="mt-2 block text-sm text-slate-600">{currency(opportunity.amount)}</span>
                <span className="mt-1 block text-xs text-slate-500">{opportunity.probability}% probability</span>
              </button>
              {inFlight && <p className="px-3 pb-2 text-xs text-slate-500">Moving&hellip;</p>}
              {!frozen && targets.length > 0 && <div className="border-t border-slate-100 px-3 py-2">
                <Button size="sm" variant="ghost" aria-expanded={menuFor === opportunity.id} onClick={() => setMenuFor((current) => (current === opportunity.id ? "" : opportunity.id))}>
                  Move{menuFor === opportunity.id ? " ▴" : " ▾"}
                </Button>
                {menuFor === opportunity.id && <ul aria-label={`Move ${opportunity.name} to`} className="mt-2 space-y-1">
                  {targets.map((target) => <li key={target.id}>
                    <button type="button" onClick={() => moveTo(opportunity, target)} className="w-full rounded px-2 py-1 text-left text-xs text-slate-700 hover:bg-slate-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600">
                      {target.name}{target.terminalType !== "open" && " (closes the deal)"}
                    </button>
                  </li>)}
                </ul>}
              </div>}
            </div>;
          })}</div>
          {cards.length === 0 && <p className="px-1 py-6 text-center text-xs text-slate-500">No opportunities</p>}
        </section>;
      })}</div>}
    </>}

    <Dialog
      open={!!closePrompt}
      onClose={() => setClosePrompt(null)}
      title={closePrompt ? `Close ${closePrompt.opportunity.name} as ${closePrompt.to.name}` : ""}
      footer={<>
        <Button variant="secondary" onClick={() => setClosePrompt(null)}>Cancel</Button>
        <Button onClick={confirmClose} disabled={!closeReason.trim()} loading={move.isPending}>Close deal</Button>
      </>}
    >
      <p className="mb-3">This stage requires a reason, and closing is final — a closed deal cannot be reopened from the board.</p>
      <Input
        label="Close reason"
        value={closeReason}
        onChange={(event) => setCloseReason(event.target.value)}
        maxLength={500}
        required
        onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); confirmClose(); } }}
      />
    </Dialog>
  </div>;
}
