import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
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
import type { Contact } from "@crm/contracts";

const SORT_OPTIONS = [
  { value: "", label: "Newest first" },
  { value: "email", label: "Email (A–Z)" },
];

const PAGE_SIZE = 20;

export default function ContactsListPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();

  const [nameContains, setNameContains] = useState("");
  const [emailContains, setEmailContains] = useState("");
  const [sort, setSort] = useState("");
  const debouncedName = useDebouncedValue(nameContains);
  const debouncedEmail = useDebouncedValue(emailContains);

  const [history, setHistory] = useState<string[]>([""]);
  const currentToken = history[history.length - 1];

  useEffect(() => {
    setHistory([""]);
  }, [debouncedName, debouncedEmail, sort]);

  const { data, error, isPending, isPlaceholderData, refetch } = useQuery({
    queryKey: ["crm.listContacts", debouncedName, debouncedEmail, sort, currentToken],
    queryFn: () =>
      crm.listContacts({
        nameContains: debouncedName,
        emailContains: debouncedEmail,
        sort,
        pageToken: currentToken,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: (prev) => prev,
  });

  const columns: Column<Contact>[] = [
    {
      key: "name",
      header: "Name",
      render: (c) => (
        <span className="font-medium text-slate-900">
          {c.firstName || c.lastName ? (
            `${c.firstName} ${c.lastName}`.trim()
          ) : (
            <span className="text-slate-400">—</span>
          )}
        </span>
      ),
    },
    { key: "email", header: "Email", render: (c) => c.email },
    {
      key: "role",
      header: "Role",
      render: (c) => (c.role ? <Badge>{c.role}</Badge> : <span className="text-slate-400">—</span>),
    },
    {
      key: "account",
      header: "Account",
      render: (c) => (
        <Link
          to={`/accounts/${c.accountId}`}
          className="text-brand-700 underline"
          onClick={(e) => e.stopPropagation()}
        >
          View account
        </Link>
      ),
    },
    {
      key: "duplicate",
      header: "",
      render: (c) => (c.duplicateOfContactId ? <Badge tone="warning">Possible duplicate</Badge> : null),
    },
  ];

  const hasFilters = nameContains !== "" || emailContains !== "";

  return (
    <div>
      <PageHeader
        title="Contacts"
        actions={
          hasScope("crm:contact:write") && (
            <Button onClick={() => navigate("/contacts/new")}>New contact</Button>
          )
        }
      />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <div className="w-64">
          <Input
            label="Search by name"
            value={nameContains}
            onChange={(e) => setNameContains(e.target.value)}
          />
        </div>
        <div className="w-64">
          <Input
            label="Search by email"
            value={emailContains}
            onChange={(e) => setEmailContains(e.target.value)}
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
          forbiddenDescription: "You don't have permission to view contacts.",
        })}

      {data && data.contacts.length === 0 && !hasFilters && (
        <EmptyState
          title="No contacts yet"
          description="Create your first contact to get started."
          action={
            hasScope("crm:contact:write") && (
              <Button onClick={() => navigate("/contacts/new")}>New contact</Button>
            )
          }
        />
      )}

      {data && data.contacts.length === 0 && hasFilters && (
        <EmptyState
          title="No matching contacts"
          description="Try a different search or clear your filters."
        />
      )}

      {data && data.contacts.length > 0 && (
        <div className={isPlaceholderData ? "opacity-60" : undefined}>
          <Table
            caption="Contacts"
            columns={columns}
            rows={data.contacts}
            getRowKey={(c) => c.id}
            onRowActivate={(c) => navigate(`/contacts/${c.id}`)}
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
