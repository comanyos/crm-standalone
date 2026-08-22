import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Table, type Column } from "../../components/ui/Table";
import { Pagination } from "../../components/ui/Pagination";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Button } from "../../components/ui/Button";
import { Badge } from "../../components/ui/Badge";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useDebouncedValue } from "../../lib/useDebouncedValue";
import { useAuth } from "../../auth/AuthProvider";
import type { Account } from "@crm/contracts";

const SORT_OPTIONS = [
  { value: "", label: "Name (A–Z)" },
  { value: "-created_at", label: "Newest first" },
];

const PAGE_SIZE = 20;

export default function AccountsListPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();

  const [nameContains, setNameContains] = useState("");
  const [tier, setTier] = useState("");
  const [sort, setSort] = useState("");
  const debouncedName = useDebouncedValue(nameContains);
  const debouncedTier = useDebouncedValue(tier);

  const [history, setHistory] = useState<string[]>([""]);
  const currentToken = history[history.length - 1];

  useEffect(() => {
    setHistory([""]);
  }, [debouncedName, debouncedTier, sort]);

  const { data, error, isPending, isPlaceholderData, refetch } = useQuery({
    queryKey: ["crm.listAccounts", debouncedName, debouncedTier, sort, currentToken],
    queryFn: () =>
      crm.listAccounts({
        nameContains: debouncedName,
        tier: debouncedTier,
        sort,
        pageToken: currentToken,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: (prev) => prev,
  });

  const columns: Column<Account>[] = [
    {
      key: "name",
      header: "Name",
      render: (a) => <span className="font-medium text-slate-900">{a.name}</span>,
    },
    {
      key: "tier",
      header: "Tier",
      render: (a) => (a.tier ? <Badge>{a.tier}</Badge> : <span className="text-slate-400">—</span>),
    },
    {
      key: "industry",
      header: "Industry",
      render: (a) => a.industry || <span className="text-slate-400">—</span>,
    },
    {
      key: "annualRevenue",
      header: "Annual revenue",
      align: "right",
      render: (a) =>
        a.annualRevenue
          ? a.annualRevenue.toLocaleString(undefined, { style: "currency", currency: "USD" })
          : "—",
    },
    {
      key: "duplicate",
      header: "",
      render: (a) => (a.duplicateOfAccountId ? <Badge tone="warning">Possible duplicate</Badge> : null),
    },
  ];

  const hasFilters = nameContains !== "" || tier !== "";

  return (
    <div>
      <PageHeader
        title="Accounts"
        actions={
          hasScope("crm:account:write") && (
            <Button onClick={() => navigate("/accounts/new")}>New account</Button>
          )
        }
      />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <div className="w-64">
          <Input
            label="Search by name"
            value={nameContains}
            onChange={(e) => setNameContains(e.target.value)}
            placeholder="e.g. Acme"
          />
        </div>
        <div className="w-48">
          <Input
            label="Tier"
            value={tier}
            onChange={(e) => setTier(e.target.value)}
            placeholder="e.g. gold"
          />
        </div>
        <div className="w-48">
          <Select
            label="Sort by"
            value={sort}
            onChange={(e) => setSort(e.target.value)}
            options={SORT_OPTIONS}
          />
        </div>
      </div>

      {isPending && <SkeletonRows rows={6} />}

      {error &&
        renderQueryError(error, {
          onRetry: () => refetch(),
          forbiddenDescription: "You don't have permission to view accounts.",
        })}

      {data && data.accounts.length === 0 && !hasFilters && (
        <EmptyState
          title="No accounts yet"
          description="Create your first account to get started."
          action={
            hasScope("crm:account:write") && (
              <Button onClick={() => navigate("/accounts/new")}>New account</Button>
            )
          }
        />
      )}

      {data && data.accounts.length === 0 && hasFilters && (
        <EmptyState
          title="No matching accounts"
          description="Try a different search or clear your filters."
        />
      )}

      {data && data.accounts.length > 0 && (
        <div className={isPlaceholderData ? "opacity-60" : undefined}>
          <Table
            caption="Accounts"
            columns={columns}
            rows={data.accounts}
            getRowKey={(a) => a.id}
            onRowActivate={(a) => navigate(`/accounts/${a.id}`)}
          />
          <div className="mt-3">
            <Pagination
              hasPrevious={history.length > 1}
              hasNext={!!data.nextPageToken}
              onPrevious={() => setHistory((h) => (h.length > 1 ? h.slice(0, -1) : h))}
              onNext={() => setHistory((h) => [...h, data.nextPageToken])}
            />
          </div>
        </div>
      )}
    </div>
  );
}
