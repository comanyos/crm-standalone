import { useQuery } from "@tanstack/react-query";
import { ConnectError, Code } from "@connectrpc/connect";
import type { DashboardReport, PipelineReport } from "@crm/contracts";
import { PageHeader } from "../components/shell/PageHeader";
import { SkeletonRows } from "../components/ui/Skeleton";
import { Badge } from "../components/ui/Badge";
import { EmptyState } from "../components/ui/StatePanel";
import { ErrorState, ForbiddenState, UnavailableState } from "../components/ui/StatePanel";
import { useCrmClients } from "../lib/useCrmClients";

/** Mirrors the currency display convention Opportunity pages already use
 * (OpportunitiesListPage, OpportunityBoardPage): a raw number formatted as
 * USD. There is no currency field anywhere in this schema, so a tenant
 * recording deals in more than one currency gets a total that mixes them —
 * see internal/domain/reporting.go's "Currency" note. This is a display
 * choice, not a claim that the number is really USD. */
function money(amount: number) {
  return amount.toLocaleString(undefined, { style: "currency", currency: "USD" });
}

const FUNNEL_LABELS: Record<string, string> = {
  new: "New",
  working: "Working",
  qualified: "Qualified",
  converted: "Converted",
  disqualified: "Disqualified",
};

export default function DashboardPage() {
  const { crm } = useCrmClients();

  const summary = useQuery({
    queryKey: ["crm.getSummary"],
    queryFn: () => crm.getSummary({}),
  });
  const report = useQuery({
    queryKey: ["crm.getDashboardReport"],
    queryFn: () => crm.getDashboardReport({}),
  });

  return (
    <div>
      <PageHeader title="Dashboard" />

      {(summary.isPending || report.isPending) && <SkeletonRows rows={4} />}

      {summary.error && <QueryErrorState error={summary.error} onRetry={() => summary.refetch()} />}

      {summary.data && (
        <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4 xl:grid-cols-8">
          <SummaryTile label="Total records" value={summary.data.total} />
          {Object.entries(summary.data.counts).map(([key, value]) => (
            <SummaryTile key={key} label={key} value={value} />
          ))}
        </div>
      )}

      {!summary.error && report.error && (
        <QueryErrorState error={report.error} onRetry={() => report.refetch()} />
      )}

      {report.data?.report && <ReportSections report={report.data.report} />}
    </div>
  );
}

function QueryErrorState({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const connectErr = ConnectError.from(error);
  if (connectErr.code === Code.PermissionDenied) {
    return <ForbiddenState description="You don't have permission to view Savvy CRM reporting." />;
  }
  if (connectErr.code === Code.Unavailable || connectErr.code === Code.DeadlineExceeded) {
    return <UnavailableState onRetry={onRetry} />;
  }
  return <ErrorState description={connectErr.message} onRetry={onRetry} />;
}

function SummaryTile({ label, value }: { label: string; value: number | bigint }) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white px-4 py-3.5 shadow-sm">
      <div className="text-2xl font-semibold leading-none tracking-[-0.025em] text-slate-950">
        {value.toString()}
      </div>
      <div className="mt-2 truncate text-[10px] font-semibold uppercase tracking-[0.11em] text-slate-500">
        {label.replace(/_/g, " ")}
      </div>
    </div>
  );
}

function SectionHeading({ children }: { children: string }) {
  return (
    <div className="mb-3 flex items-center gap-2">
      <span aria-hidden="true" className="h-4 w-0.5 rounded-full bg-brand-600" />
      <h2 className="text-base font-semibold text-slate-950">{children}</h2>
    </div>
  );
}

function ReportSections({ report }: { report: DashboardReport }) {
  const tasks = report.tasks;
  const conversion = report.conversion;
  const forecast = report.forecast;

  return (
    <div className="space-y-6">
      <section>
        <SectionHeading>Pipelines</SectionHeading>
        {report.pipelines.length === 0 ? (
          <EmptyState
            title="No pipelines yet"
            description="Create a pipeline to see its stage breakdown here."
          />
        ) : (
          <div className="grid gap-3 lg:grid-cols-2">
            {report.pipelines.map((p) => (
              <PipelineCard key={p.pipelineId} pipeline={p} />
            ))}
          </div>
        )}
      </section>

      {tasks && (
        <section>
          <SectionHeading>Tasks</SectionHeading>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
            <SummaryTile label="Open" value={tasks.openCount} />
            <SummaryTile label="In progress" value={tasks.inProgressCount} />
            <SummaryTile label="Completed" value={tasks.completedCount} />
            <SummaryTile label="Cancelled" value={tasks.cancelledCount} />
            <div className="rounded-xl border border-danger-200 bg-white px-4 py-3.5 shadow-sm">
              <div className="text-2xl font-semibold leading-none text-danger-700">{tasks.overdueCount}</div>
              <div className="mt-2 text-[10px] font-semibold uppercase tracking-[0.11em] text-slate-500">
                Overdue
              </div>
            </div>
          </div>
        </section>
      )}

      {conversion && (
        <section>
          <SectionHeading>Lead conversion</SectionHeading>
          <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
            <div className="mb-4 flex flex-wrap items-center gap-4">
              {conversion.funnel.map((stage) => (
                <div key={stage.status} className="text-center">
                  <div className="text-xl font-semibold text-slate-900">{stage.count}</div>
                  <div className="text-xs text-slate-500">{FUNNEL_LABELS[stage.status] ?? stage.status}</div>
                </div>
              ))}
            </div>
            <p className="text-sm text-slate-600">
              <strong>{(conversion.conversionRate * 100).toFixed(1)}%</strong> conversion rate —{" "}
              {conversion.convertedLeads} of {conversion.totalLeads} lead
              {conversion.totalLeads === 1 ? "" : "s"} converted.
            </p>
          </div>
        </section>
      )}

      {forecast && (
        <section>
          <SectionHeading>Weighted forecast</SectionHeading>
          <p className="mb-3 max-w-2xl text-sm text-slate-600">
            Uses a default stage-probability weighting, not a confirmed forecasting methodology — see{" "}
            <code className="rounded bg-slate-100 px-1 py-0.5 text-xs">DefaultStageProbabilities</code>. Spans
            every pipeline's opportunities, tenant-wide.
          </p>
          <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
            <div className="mb-4 text-2xl font-semibold tracking-[-0.025em] text-slate-950">
              {money(forecast.total)}
            </div>
            {forecast.buckets.length === 0 ? (
              <p className="text-sm text-slate-500">No open opportunities to forecast.</p>
            ) : (
              <table className="w-full text-left text-sm">
                <thead className="text-xs font-semibold uppercase text-slate-500">
                  <tr>
                    <th className="py-1 pr-3">Stage</th>
                    <th className="py-1 pr-3">Owner</th>
                    <th className="py-1 pr-3 text-right">Count</th>
                    <th className="py-1 pr-3 text-right">Raw</th>
                    <th className="py-1 text-right">Weighted</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {forecast.buckets.map((b) => (
                    <tr key={`${b.ownerId}:${b.stage}`}>
                      <td className="py-1.5 pr-3">{b.stage.replace(/_/g, " ")}</td>
                      <td className="py-1.5 pr-3 text-slate-500">{b.ownerId || "Unassigned"}</td>
                      <td className="py-1.5 pr-3 text-right">{b.count}</td>
                      <td className="py-1.5 pr-3 text-right">{money(b.amount)}</td>
                      <td className="py-1.5 text-right font-medium">{money(b.weighted)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </section>
      )}
    </div>
  );
}

function PipelineCard({ pipeline }: { pipeline: PipelineReport }) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold text-slate-950">{pipeline.pipelineName}</h3>
        <div className="flex gap-2">
          <Badge tone="success">
            {pipeline.wonCount} won — {money(pipeline.wonAmount)}
          </Badge>
          <Badge tone="danger">{pipeline.lostCount} lost</Badge>
        </div>
      </div>
      {pipeline.stages.length === 0 ? (
        <p className="text-sm text-slate-500">No stages configured.</p>
      ) : (
        <ul className="space-y-1.5">
          {pipeline.stages.map((stage) => (
            <li key={stage.stageId} className="flex items-center justify-between text-sm">
              <span className="text-slate-700">{stage.stageName}</span>
              <span className="text-slate-500">
                {stage.count} · {money(stage.amount)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
