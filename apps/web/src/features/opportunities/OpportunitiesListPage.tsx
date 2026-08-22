import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { PageHeader } from "../../components/shell/PageHeader";
import { Table, type Column } from "../../components/ui/Table";
import { Pagination } from "../../components/ui/Pagination";
import { Button } from "../../components/ui/Button";
import { Badge } from "../../components/ui/Badge";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import type { Opportunity } from "@crm/contracts";
import { stageLabel, stageTone } from "./stages";

const PAGE_SIZE = 20;

function currency(amount: number) {
  return amount ? amount.toLocaleString(undefined, { style: "currency", currency: "USD" }) : "—";
}

export default function OpportunitiesListPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const [history, setHistory] = useState<string[]>([""]);
  const currentToken = history[history.length - 1];

  const { data, error, isPending, isPlaceholderData, refetch } = useQuery({
    queryKey: ["crm.listOpportunities", currentToken],
    queryFn: () => crm.listOpportunities({ pageSize: PAGE_SIZE, pageToken: currentToken }),
    placeholderData: (previous) => previous,
  });

  const columns: Column<Opportunity>[] = [
    { key: "name", header: "Opportunity", render: (opportunity) => <span className="font-medium text-slate-900">{opportunity.name}</span> },
    { key: "stage", header: "Stage", render: (opportunity) => <Badge tone={stageTone(opportunity.stage)}>{stageLabel(opportunity.stage)}</Badge> },
    { key: "amount", header: "Amount", align: "right", render: (opportunity) => currency(opportunity.amount) },
    { key: "probability", header: "Probability", align: "right", render: (opportunity) => opportunity.probability ? `${opportunity.probability}%` : "—" },
    { key: "closeDate", header: "Close date", render: (opportunity) => opportunity.closeDate ? timestampDate(opportunity.closeDate).toLocaleDateString() : "—" },
  ];

  return (
    <div>
      <PageHeader
        title="Opportunities"
        actions={<div className="flex gap-2"><Button variant="secondary" onClick={() => navigate("/opportunities/board")}>Board</Button>{hasScope("crm:opportunity:write") && <Button onClick={() => navigate("/opportunities/new")}>New opportunity</Button>}</div>}
      />

      {isPending && <SkeletonRows rows={6} />}
      {error && renderQueryError(error, { onRetry: () => refetch(), forbiddenDescription: "You don't have permission to view opportunities." })}
      {data && data.opportunities.length === 0 && (
        <EmptyState
          title="No opportunities yet"
          description="Create an opportunity from an account or convert a qualified lead."
          action={hasScope("crm:opportunity:write") && <Button onClick={() => navigate("/opportunities/new")}>New opportunity</Button>}
        />
      )}
      {data && data.opportunities.length > 0 && (
        <div className={isPlaceholderData ? "opacity-60" : undefined}>
          <Table caption="Opportunities" columns={columns} rows={data.opportunities} getRowKey={(opportunity) => opportunity.id} onRowActivate={(opportunity) => navigate(`/opportunities/${opportunity.id}`)} />
          <div className="mt-3">
            <Pagination
              hasPrevious={history.length > 1}
              hasNext={!!data.nextPageToken}
              onPrevious={() => setHistory((items) => items.length > 1 ? items.slice(0, -1) : items)}
              onNext={() => setHistory((items) => [...items, data.nextPageToken])}
            />
          </div>
        </div>
      )}
    </div>
  );
}
