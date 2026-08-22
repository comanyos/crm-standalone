import { useQuery } from "@tanstack/react-query";
import { ConnectError, Code } from "@connectrpc/connect";
import { PageHeader } from "../components/shell/PageHeader";
import { SkeletonRows } from "../components/ui/Skeleton";
import { ErrorState, ForbiddenState, UnavailableState } from "../components/ui/StatePanel";
import { useCrmClients } from "../lib/useCrmClients";

export default function DashboardPage() {
  const { crm } = useCrmClients();

  const { data, error, isPending, refetch } = useQuery({
    queryKey: ["crm.getSummary"],
    queryFn: () => crm.getSummary({}),
  });

  return (
    <div>
      <PageHeader title="Dashboard" />

      {isPending && <SkeletonRows rows={4} />}

      {error &&
        (() => {
          const connectErr = ConnectError.from(error);
          if (connectErr.code === Code.PermissionDenied) {
            return <ForbiddenState description="You don't have permission to view the CRM summary." />;
          }
          if (connectErr.code === Code.Unavailable || connectErr.code === Code.DeadlineExceeded) {
            return <UnavailableState onRetry={() => refetch()} />;
          }
          return <ErrorState description={connectErr.message} onRetry={() => refetch()} />;
        })()}

      {data && (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <SummaryTile label="Total records" value={data.total} />
          {Object.entries(data.counts).map(([key, value]) => (
            <SummaryTile key={key} label={key} value={value} />
          ))}
        </div>
      )}
    </div>
  );
}

function SummaryTile({ label, value }: { label: string; value: number | bigint }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4">
      <div className="text-2xl font-semibold text-slate-900">{value.toString()}</div>
      <div className="text-xs capitalize text-slate-500">{label.replace(/_/g, " ")}</div>
    </div>
  );
}
