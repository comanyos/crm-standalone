import { useState } from "react";
import { useNavigate, useParams, Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Badge } from "../../components/ui/Badge";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { ActivityTimeline } from "../../components/ActivityTimeline";
import { TasksPanel } from "../../components/TasksPanel";
import { NotesPanel } from "../../components/NotesPanel";
import type { ReactNode } from "react";

export default function ContactDetailPage() {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  const contactQuery = useQuery({
    queryKey: ["crm.getContact", id],
    queryFn: () => crm.getContact({ id }),
  });
  const contact = contactQuery.data?.contact;

  const accountQuery = useQuery({
    queryKey: ["crm.getAccount", contact?.accountId],
    queryFn: () => crm.getAccount({ id: contact!.accountId }),
    enabled: !!contact?.accountId,
  });

  const duplicateQuery = useQuery({
    queryKey: ["crm.getContact", contact?.duplicateOfContactId],
    queryFn: () => crm.getContact({ id: contact!.duplicateOfContactId }),
    enabled: !!contact?.duplicateOfContactId,
  });

  const deleteMutation = useMutation({
    mutationFn: () => crm.deleteContact({ id }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["crm.listContacts"] });
      toast.show({ tone: "success", message: "Contact deleted." });
      navigate("/contacts");
    },
    onError: (err) => {
      setConfirmingDelete(false);
      toast.show({ tone: "danger", message: mutationErrorMessage(err) });
    },
  });

  if (contactQuery.isPending) {
    return (
      <div>
        <PageHeader title="Contact" breadcrumbs={[{ label: "Contacts", to: "/contacts" }]} />
        <SkeletonRows rows={5} />
      </div>
    );
  }

  if (contactQuery.error) {
    return (
      <div>
        <PageHeader title="Contact" breadcrumbs={[{ label: "Contacts", to: "/contacts" }]} />
        {renderQueryError(contactQuery.error, {
          onRetry: () => contactQuery.refetch(),
          forbiddenDescription: "You don't have permission to view this contact.",
        })}
      </div>
    );
  }

  if (!contact) return null;

  const displayName =
    contact.firstName || contact.lastName ? `${contact.firstName} ${contact.lastName}`.trim() : contact.email;
  const canWrite = hasScope("crm:contact:write");

  return (
    <div>
      <PageHeader
        title={displayName}
        breadcrumbs={[{ label: "Contacts", to: "/contacts" }]}
        actions={
          canWrite && (
            <>
              <Button variant="secondary" onClick={() => navigate(`/contacts/${id}/edit`)}>
                Edit
              </Button>
              <Button variant="danger" onClick={() => setConfirmingDelete(true)}>
                Delete
              </Button>
            </>
          )
        }
      />

      {contact.duplicateOfContactId && (
        <div className="mb-4 rounded-md border border-warning-200 bg-warning-50 px-4 py-3 text-sm text-warning-800">
          This email matches{" "}
          <Link to={`/contacts/${contact.duplicateOfContactId}`} className="font-medium underline">
            {duplicateQuery.data?.contact
              ? `${duplicateQuery.data.contact.firstName} ${duplicateQuery.data.contact.lastName}`.trim() ||
                duplicateQuery.data.contact.email
              : "another contact"}
          </Link>
          . Review both before assuming they're the same person — contacts are never merged automatically.
        </div>
      )}

      <section className="max-w-xl rounded-lg border border-slate-200 bg-white p-4">
        <h2 className="mb-3 text-sm font-semibold text-slate-900">Details</h2>
        <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-3 text-sm">
          <Field label="Email" value={contact.email} />
          <Field label="Phone" value={contact.phone} />
          <Field label="Title" value={contact.title} />
          <Field label="Role" value={contact.role ? <Badge>{contact.role}</Badge> : undefined} />
          <Field
            label="Account"
            value={
              <Link to={`/accounts/${contact.accountId}`} className="text-brand-700 underline">
                {accountQuery.data?.account?.name ?? contact.accountId}
              </Link>
            }
          />
        </dl>
      </section>
      <TasksPanel subjectType="contact" subjectId={id} />
      <NotesPanel subjectType="contact" subjectId={id} />
      <ActivityTimeline subjectType="contact" subjectId={id} />

      <ConfirmDialog
        open={confirmingDelete}
        title="Delete contact"
        description={`Delete ${displayName}? This cannot be undone.`}
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
      <dd className="mt-0.5 text-slate-900">{value || <span className="text-slate-400">—</span>}</dd>
    </div>
  );
}
