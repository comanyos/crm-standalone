import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Table, type Column } from "../../components/ui/Table";
import { Pagination } from "../../components/ui/Pagination";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Button } from "../../components/ui/Button";
import { Badge } from "../../components/ui/Badge";
import { Dialog } from "../../components/ui/Dialog";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { useToast } from "../../components/ui/Toast";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useDebouncedValue } from "../../lib/useDebouncedValue";
import { useAuth } from "../../auth/AuthProvider";
import type { Account, SavedView } from "@crm/contracts";

const SORT_OPTIONS = [
  { value: "", label: "Name (A–Z)" },
  { value: "-created_at", label: "Newest first" },
];

const PAGE_SIZE = 20;

export default function AccountsListPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const toast = useToast();

  const [nameContains, setNameContains] = useState("");
  const [tier, setTier] = useState("");
  const [sort, setSort] = useState("");
  const debouncedName = useDebouncedValue(nameContains);
  const debouncedTier = useDebouncedValue(tier);

  const [history, setHistory] = useState<string[]>([""]);
  const currentToken = history[history.length - 1];

  // Bulk tag selection is page-local, the same as the tasks list's bulk
  // selection: it does not survive a filter change or a page turn, both of
  // which already invalidate what "selected" means.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [tagToApply, setTagToApply] = useState("");
  const canTag = hasScope("crm:tag:write");
  const tagsQuery = useQuery({ queryKey: ["crm.listTags"], queryFn: () => crm.listTags({}), enabled: canTag });
  const toggleSelected = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const reportBulkOutcome = (results: { success: boolean }[], verb: string) => {
    const succeeded = results.filter((r) => r.success).length;
    const failed = results.length - succeeded;
    if (failed === 0) {
      toast.show({ tone: "success", message: `${verb} ${succeeded} account${succeeded === 1 ? "" : "s"}.` });
    } else {
      toast.show({
        tone: "warning",
        message: `${verb} ${succeeded}, ${failed} failed — an account may have changed since you selected it.`,
      });
    }
  };
  const bulkAssign = useMutation({
    mutationFn: () => crm.bulkAssignTag({ tagId: tagToApply, objectType: "account", objectIds: Array.from(selected) }),
    onSuccess: (res) => {
      setSelected(new Set());
      setTagToApply("");
      reportBulkOutcome(res.results, "Tagged");
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });
  const bulkRemove = useMutation({
    mutationFn: () => crm.bulkRemoveTag({ tagId: tagToApply, objectType: "account", objectIds: Array.from(selected) }),
    onSuccess: (res) => {
      setSelected(new Set());
      setTagToApply("");
      reportBulkOutcome(res.results, "Untagged");
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });

  useEffect(() => {
    setHistory([""]);
    setSelected(new Set());
  }, [debouncedName, debouncedTier, sort]);

  // Saved views: a private, per-owner preset of this page's own filters and
  // sort. applyView is the one place that knows how a SavedView's flat
  // filters map maps back onto this page's own filter state.
  const canReadViews = hasScope("crm:saved_view:read");
  const canSaveViews = hasScope("crm:saved_view:write");
  const [activeViewId, setActiveViewId] = useState("");
  const [savingView, setSavingView] = useState(false);
  const [newViewName, setNewViewName] = useState("");
  const savedViewsQuery = useQuery({
    queryKey: ["crm.listSavedViews", "account"],
    queryFn: () => crm.listSavedViews({ objectType: "account" }),
    enabled: canReadViews,
  });
  const applyView = (view: SavedView) => {
    setNameContains(view.filters.nameContains ?? "");
    setTier(view.filters.tier ?? "");
    setSort(view.sort);
  };
  const saveView = useMutation({
    mutationFn: () =>
      crm.createSavedView({
        objectType: "account",
        name: newViewName.trim(),
        filters: { nameContains, tier },
        sort,
      }),
    onSuccess: (res) => {
      savedViewsQuery.refetch();
      setActiveViewId(res.view?.id ?? "");
      setSavingView(false);
      setNewViewName("");
      toast.show({ tone: "success", message: "View saved." });
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });
  const deleteView = useMutation({
    mutationFn: (id: string) => crm.deleteSavedView({ id }),
    onSuccess: () => {
      setActiveViewId("");
      savedViewsQuery.refetch();
      toast.show({ tone: "success", message: "View deleted." });
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });

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
      key: "select",
      header: "Select", headerHidden: true,
      render: (a) =>
        canTag ? (
          <input
            type="checkbox"
            checked={selected.has(a.id)}
            aria-label={`Select ${a.name}`}
            onClick={(event) => event.stopPropagation()}
            onChange={(event) => {
              event.stopPropagation();
              toggleSelected(a.id);
            }}
            className="h-4 w-4 rounded border-slate-300 text-brand-600 focus:ring-brand-500"
          />
        ) : null,
    },
    {
      key: "name",
      header: "Name",
      rowHeader: true,
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
      header: "Duplicate status", headerHidden: true,
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
        {canReadViews && (savedViewsQuery.data?.views.length ?? 0) > 0 && (
          <div className="w-56">
            <Select
              label="Saved view"
              value={activeViewId}
              onChange={(e) => {
                setActiveViewId(e.target.value);
                const view = savedViewsQuery.data?.views.find((v) => v.id === e.target.value);
                if (view) applyView(view);
              }}
              placeholder="None"
              options={(savedViewsQuery.data?.views ?? []).map((v) => ({ value: v.id, label: v.name }))}
            />
          </div>
        )}
        {canSaveViews && (
          <Button variant="secondary" onClick={() => { setNewViewName(""); setSavingView(true); }}>
            Save current view
          </Button>
        )}
        {canSaveViews && activeViewId && (
          <Button variant="secondary" loading={deleteView.isPending} onClick={() => deleteView.mutate(activeViewId)}>
            Delete view
          </Button>
        )}
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
          {canTag && selected.size > 0 && (
            <div className="mb-3 flex flex-wrap items-end gap-3 rounded-md border border-brand-200 bg-brand-50 px-3 py-2 text-sm">
              <span className="font-medium text-brand-800">
                {selected.size} account{selected.size === 1 ? "" : "s"} selected
              </span>
              <div className="w-48">
                <Select
                  label="Tag"
                  value={tagToApply}
                  onChange={(e) => setTagToApply(e.target.value)}
                  placeholder="Choose a tag…"
                  options={(tagsQuery.data?.tags ?? []).map((t) => ({ value: t.id, label: t.name }))}
                />
              </div>
              <Button
                size="sm"
                variant="secondary"
                disabled={!tagToApply}
                loading={bulkAssign.isPending}
                onClick={() => bulkAssign.mutate()}
              >
                Add tag to selected
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={!tagToApply}
                loading={bulkRemove.isPending}
                onClick={() => bulkRemove.mutate()}
              >
                Remove tag from selected
              </Button>
              <Button size="sm" variant="secondary" onClick={() => setSelected(new Set())}>
                Clear selection
              </Button>
            </div>
          )}
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
      <Dialog
        open={savingView}
        onClose={() => setSavingView(false)}
        title="Save current view"
        footer={
          <>
            <Button variant="secondary" onClick={() => setSavingView(false)} disabled={saveView.isPending}>
              Cancel
            </Button>
            <Button onClick={() => saveView.mutate()} loading={saveView.isPending} disabled={!newViewName.trim()}>
              Save view
            </Button>
          </>
        }
      >
        <Input
          label="View name"
          value={newViewName}
          onChange={(e) => setNewViewName(e.target.value)}
          hint="Saves the current search, tier filter, and sort order for you to reapply later. Views are private to you."
          required
        />
      </Dialog>
    </div>
  );
}
