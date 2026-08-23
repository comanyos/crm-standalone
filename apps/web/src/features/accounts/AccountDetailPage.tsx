import { useState, type ReactNode } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Badge } from "../../components/ui/Badge";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { EmptyState } from "../../components/ui/StatePanel";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { ActivityTimeline } from "../../components/ActivityTimeline";
import { TasksPanel } from "../../components/TasksPanel";
import { NotesPanel } from "../../components/NotesPanel";

export default function AccountDetailPage() {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  const accountQuery = useQuery({
    queryKey: ["crm.getAccount", id],
    queryFn: () => crm.getAccount({ id }),
  });
  const account = accountQuery.data?.account;

  const parentQuery = useQuery({
    queryKey: ["crm.getAccount", account?.parentId],
    queryFn: () => crm.getAccount({ id: account!.parentId }),
    enabled: !!account?.parentId,
  });

  const childrenQuery = useQuery({
    queryKey: ["crm.listAccounts.children", id],
    queryFn: () => crm.listAccounts({ parentId: id, pageSize: 50 }),
    enabled: !!account,
  });

  const contactsQuery = useQuery({
    queryKey: ["crm.listContacts.forAccount", id],
    queryFn: () => crm.listContacts({ accountId: id, pageSize: 50 }),
    enabled: !!account,
  });


  const deleteMutation = useMutation({
    mutationFn: () => crm.deleteAccount({ id }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["crm.listAccounts"] });
      toast.show({ tone: "success", message: `${account?.name ?? "Account"} deleted.` });
      navigate("/accounts");
    },
    onError: (err) => {
      setConfirmingDelete(false);
      toast.show({ tone: "danger", message: mutationErrorMessage(err) });
    },
  });

  if (accountQuery.isPending) {
    return (
      <div>
        <PageHeader title="Account" breadcrumbs={[{ label: "Accounts", to: "/accounts" }]} />
        <SkeletonRows rows={5} />
      </div>
    );
  }

  if (accountQuery.error) {
    return (
      <div>
        <PageHeader title="Account" breadcrumbs={[{ label: "Accounts", to: "/accounts" }]} />
        {renderQueryError(accountQuery.error, {
          onRetry: () => accountQuery.refetch(),
          forbiddenDescription: "You don't have permission to view this account.",
        })}
      </div>
    );
  }

  if (!account) return null;

  const canWrite = hasScope("crm:account:write");

  return (
    <div>
      <PageHeader
        title={account.name}
        breadcrumbs={[{ label: "Accounts", to: "/accounts" }]}
        actions={
          canWrite && (
            <>
              <Button variant="secondary" onClick={() => navigate(`/accounts/${id}/edit`)}>
                Edit
              </Button>
              <Button variant="danger" onClick={() => setConfirmingDelete(true)}>
                Delete
              </Button>
            </>
          )
        }
      />

      {account.duplicateOfAccountId && (
        <div className="mb-4 rounded-md border border-warning-200 bg-warning-50 px-4 py-3 text-sm text-warning-800">
          This account's website matches{" "}
          <Link to={`/accounts/${account.duplicateOfAccountId}`} className="font-medium underline">
            another account
          </Link>
          . Review both before assuming they're unrelated — accounts are never merged automatically.
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 md:grid-cols-3">
        <section className="rounded-lg border border-slate-200 bg-white p-4 md:col-span-2">
          <h2 className="mb-3 text-sm font-semibold text-slate-900">Details</h2>
          <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-3 text-sm">
            <Field label="Tier" value={account.tier ? <Badge>{account.tier}</Badge> : undefined} />
            <Field label="Industry" value={account.industry} />
            <Field label="Owner" value={account.ownerId} />
            <Field
              label="Website"
              value={
                account.website ? (
                  <a
                    href={account.website}
                    target="_blank"
                    rel="noreferrer"
                    className="text-brand-700 underline"
                  >
                    {account.website}
                  </a>
                ) : undefined
              }
            />
            <Field
              label="Annual revenue"
              value={
                account.annualRevenue
                  ? account.annualRevenue.toLocaleString(undefined, { style: "currency", currency: "USD" })
                  : undefined
              }
            />
            <Field
              label="Parent account"
              value={
                account.parentId ? (
                  <Link to={`/accounts/${account.parentId}`} className="text-brand-700 underline">
                    {parentQuery.data?.account?.name ?? account.parentId}
                  </Link>
                ) : undefined
              }
            />
          </dl>
        </section>

        <section className="rounded-lg border border-slate-200 bg-white p-4">
          <h2 className="mb-3 text-sm font-semibold text-slate-900">Child accounts</h2>
          {childrenQuery.isPending && <SkeletonRows rows={2} />}
          {childrenQuery.error &&
            renderQueryError(childrenQuery.error, { onRetry: () => childrenQuery.refetch() })}
          {childrenQuery.data && childrenQuery.data.accounts.length === 0 && (
            <p className="text-sm text-slate-500">No child accounts.</p>
          )}
          <ul className="flex flex-col gap-2">
            {childrenQuery.data?.accounts.map((child) => (
              <li key={child.id}>
                <Link to={`/accounts/${child.id}`} className="text-sm text-brand-700 underline">
                  {child.name}
                </Link>
              </li>
            ))}
          </ul>
        </section>

        <section className="rounded-lg border border-slate-200 bg-white p-4 md:col-span-2">
          <div className="mb-3 flex items-center justify-between">
            <h2 className="text-sm font-semibold text-slate-900">Contacts</h2>
            {hasScope("crm:contact:write") && (
              <Button
                size="sm"
                variant="secondary"
                onClick={() =>
                  navigate(`/contacts/new?accountId=${id}&accountName=${encodeURIComponent(account.name)}`)
                }
              >
                Add contact
              </Button>
            )}
          </div>
          {contactsQuery.isPending && <SkeletonRows rows={3} />}
          {contactsQuery.error &&
            renderQueryError(contactsQuery.error, { onRetry: () => contactsQuery.refetch() })}
          {contactsQuery.data && contactsQuery.data.contacts.length === 0 && (
            <EmptyState title="No contacts" description="This account has no contacts yet." />
          )}
          {contactsQuery.data && contactsQuery.data.contacts.length > 0 && (
            <ul className="divide-y divide-slate-100">
              {contactsQuery.data.contacts.map((c) => (
                <li key={c.id} className="flex items-center justify-between py-2 text-sm">
                  <Link to={`/contacts/${c.id}`} className="text-brand-700 underline">
                    {c.firstName || c.lastName ? `${c.firstName} ${c.lastName}`.trim() : c.email}
                  </Link>
                  <span className="text-slate-500">{c.email}</span>
                </li>
              ))}
            </ul>
          )}
        </section>

        <TasksPanel subjectType="account" subjectId={id} />
      <NotesPanel subjectType="account" subjectId={id} />
      <ActivityTimeline subjectType="account" subjectId={id} />
      </div>

      <ConfirmDialog
        open={confirmingDelete}
        title="Delete account"
        description={`Delete ${account.name}? This cannot be undone.`}
        confirmLabel="Delete"
        tone="danger"
        loading={deleteMutation.isPending}
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setConfirmingDelete(false)}
      />
    </div>
  );
}

function Field({ label, value }: { label: string; value?: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-0.5 text-slate-900">{value ?? <span className="text-slate-400">—</span>}</dd>
    </div>
  );
}
