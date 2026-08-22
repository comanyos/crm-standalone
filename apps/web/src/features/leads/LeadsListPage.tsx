import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import type { Lead } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Table, type Column } from "../../components/ui/Table";
import { Pagination } from "../../components/ui/Pagination";
import { Button } from "../../components/ui/Button";
import { Badge, type BadgeTone } from "../../components/ui/Badge";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";

const PAGE_SIZE = 20;

function statusTone(status: string): BadgeTone {
  if (status === "qualified" || status === "converted") return "success";
  if (status === "disqualified") return "danger";
  if (status === "working") return "brand";
  return "neutral";
}

export default function LeadsListPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const [history, setHistory] = useState<string[]>([""]);
  const currentToken = history[history.length - 1] ?? "";

  const { data, error, isPending, isPlaceholderData, refetch } = useQuery({
    queryKey: ["crm.listLeads", currentToken],
    queryFn: () => crm.listLeads({ pageSize: PAGE_SIZE, pageToken: currentToken }),
    placeholderData: (previous) => previous,
  });

  const columns: Column<Lead>[] = [
    {
      key: "company",
      header: "Company",
      render: (lead) => <span className="font-medium text-slate-900">{lead.company}</span>,
    },
    { key: "email", header: "Contact email", render: (lead) => lead.contactEmail || <span className="text-slate-400">—</span> },
    { key: "source", header: "Source", render: (lead) => lead.source || <span className="text-slate-400">—</span> },
    {
      key: "status",
      header: "Status",
      render: (lead) => <Badge tone={statusTone(lead.status)}>{lead.status || "new"}</Badge>,
    },
    { key: "score", header: "Score", render: (lead) => lead.score.toString() },
    { key: "owner", header: "Owner", render: (lead) => lead.ownerId },
    {
      key: "duplicate",
      header: "",
      render: (lead) => (lead.duplicateOfContactId ? <Badge tone="warning">Possible duplicate</Badge> : null),
    },
  ];

  return (
    <div>
      <PageHeader
        title="Leads"
        actions={hasScope("crm:lead:write") && <Button onClick={() => navigate("/leads/new")}>New lead</Button>}
      />

      {isPending && <SkeletonRows rows={6} />}
      {error &&
        renderQueryError(error, {
          onRetry: () => refetch(),
          forbiddenDescription: "You don't have permission to view leads.",
        })}

      {data && data.leads.length === 0 && (
        <EmptyState
          title="No leads yet"
          description="Add a lead to start qualifying new business."
          action={hasScope("crm:lead:write") && <Button onClick={() => navigate("/leads/new")}>New lead</Button>}
        />
      )}

      {data && data.leads.length > 0 && (
        <div className={isPlaceholderData ? "opacity-60" : undefined}>
          <Table
            caption="Leads"
            columns={columns}
            rows={data.leads}
            getRowKey={(lead) => lead.id}
            onRowActivate={(lead) => navigate(`/leads/${lead.id}`)}
          />
          <div className="mt-3">
            <Pagination
              hasPrevious={history.length > 1}
              hasNext={!!data.nextPageToken}
              onPrevious={() => setHistory((items) => (items.length > 1 ? items.slice(0, -1) : items))}
              onNext={() => setHistory((items) => [...items, data.nextPageToken])}
            />
          </div>
        </div>
      )}
    </div>
  );
}
