import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Badge, type BadgeTone } from "../../components/ui/Badge";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useAuth } from "../../auth/AuthProvider";
import { useToast } from "../../components/ui/Toast";
import { ActivityTimeline } from "../../components/ActivityTimeline";
import { TasksPanel } from "../../components/TasksPanel";
import { NotesPanel } from "../../components/NotesPanel";
import { TagsPanel } from "../../components/TagsPanel";
import { DraftedMessagesPanel } from "../../components/DraftedMessagesPanel";

function statusTone(status: string): BadgeTone {
  if (status === "qualified" || status === "converted") return "success";
  if (status === "disqualified") return "danger";
  if (status === "working") return "brand";
  return "neutral";
}

export default function LeadDetailPage() {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [converting, setConverting] = useState(false);
  const [createOpportunity, setCreateOpportunity] = useState(false);
  const [opportunityName, setOpportunityName] = useState("");
  const [opportunityAmount, setOpportunityAmount] = useState("0");

  const leadQuery = useQuery({ queryKey: ["crm.getLead", id], queryFn: () => crm.getLead({ id }) });
  const lead = leadQuery.data?.lead;
  const duplicateQuery = useQuery({
    queryKey: ["crm.getContact", lead?.duplicateOfContactId],
    queryFn: () => crm.getContact({ id: lead!.duplicateOfContactId }),
    enabled: !!lead?.duplicateOfContactId,
  });

  const deleteMutation = useMutation({
    mutationFn: () => crm.deleteLead({ id }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["crm.listLeads"] });
      toast.show({ tone: "success", message: "Lead deleted." });
      navigate("/leads");
    },
    onError: (error) => {
      setConfirmingDelete(false);
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    },
  });
  const convertMutation = useMutation({
    mutationFn: () =>
      crm.convertLead({
        leadId: id,
        createOpportunity,
        opportunityName: opportunityName.trim(),
        opportunityAmount: Number(opportunityAmount) || 0,
      }),
    onSuccess: (result) => {
      setConverting(false);
      queryClient.invalidateQueries({ queryKey: ["crm.getLead", id] });
      queryClient.invalidateQueries({ queryKey: ["crm.listLeads"] });
      queryClient.invalidateQueries({ queryKey: ["crm.listAccounts"] });
      queryClient.invalidateQueries({ queryKey: ["crm.listContacts"] });
      queryClient.invalidateQueries({ queryKey: ["crm.listOpportunities"] });
      toast.show({
        tone: "success",
        message: result.opportunity
          ? "Lead converted to an account, contact, and opportunity."
          : "Lead converted to an account and contact.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (leadQuery.isPending)
    return (
      <div>
        <PageHeader title="Lead" breadcrumbs={[{ label: "Leads", to: "/leads" }]} />
        <SkeletonRows rows={5} />
      </div>
    );
  if (leadQuery.error)
    return (
      <div>
        <PageHeader title="Lead" breadcrumbs={[{ label: "Leads", to: "/leads" }]} />
        {renderQueryError(leadQuery.error, {
          onRetry: () => leadQuery.refetch(),
          forbiddenDescription: "You don't have permission to view this lead.",
        })}
      </div>
    );
  if (!lead) return null;

  const canWrite = hasScope("crm:lead:write");
  const canConvert = canWrite && lead.status === "qualified";

  return (
    <div>
      <PageHeader
        title={lead.company}
        breadcrumbs={[{ label: "Leads", to: "/leads" }]}
        actions={
          canWrite &&
          lead.status !== "converted" &&
          lead.status !== "disqualified" && (
            <>
              {canConvert && <Button onClick={() => setConverting(true)}>Convert lead</Button>}
              <Button variant="secondary" onClick={() => navigate(`/leads/${id}/edit`)}>
                Edit
              </Button>
              <Button variant="danger" onClick={() => setConfirmingDelete(true)}>
                Delete
              </Button>
            </>
          )
        }
      />
      {lead.duplicateOfContactId && (
        <div className="mb-4 rounded-md border border-warning-200 bg-warning-50 px-4 py-3 text-sm text-warning-800">
          This email matches{" "}
          <Link to={`/contacts/${lead.duplicateOfContactId}`} className="font-medium underline">
            {duplicateQuery.data?.contact?.email ?? "an existing contact"}
          </Link>
          . Review both before conversion.
        </div>
      )}
      <section className="max-w-2xl rounded-lg border border-slate-200 bg-white p-4">
        <h2 className="mb-3 text-sm font-semibold text-slate-900">Details</h2>
        <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-3 text-sm">
          <Field label="Status" value={<Badge tone={statusTone(lead.status)}>{lead.status}</Badge>} />
          <Field label="Score" value={lead.score.toString()} />
          <Field label="Contact email" value={lead.contactEmail} />
          <Field label="Source" value={lead.source} />
          <Field label="Owner" value={lead.ownerId} />
        </dl>
      </section>
      <TagsPanel objectType="lead" objectId={id} />
      <TasksPanel subjectType="lead" subjectId={id} />
      <NotesPanel subjectType="lead" subjectId={id} />
      <DraftedMessagesPanel subjectType="lead" subjectId={id} />
      <ActivityTimeline subjectType="lead" subjectId={id} />
      {lead.status === "converted" && (
        <section className="mt-6 max-w-2xl rounded-lg border border-success-200 bg-success-50 p-4">
          <h2 className="mb-3 text-sm font-semibold text-success-700">Converted</h2>
          <div className="flex flex-wrap gap-4 text-sm">
            {lead.convertedAccountId && (
              <Link to={`/accounts/${lead.convertedAccountId}`} className="text-brand-700 underline">
                View account
              </Link>
            )}
            {lead.convertedContactId && (
              <Link to={`/contacts/${lead.convertedContactId}`} className="text-brand-700 underline">
                View contact
              </Link>
            )}
            {lead.convertedOpportunityId && (
              <Link to={`/opportunities/${lead.convertedOpportunityId}`} className="text-brand-700 underline">
                View opportunity
              </Link>
            )}
          </div>
        </section>
      )}
      <ConfirmDialog
        open={confirmingDelete}
        title="Delete lead"
        description={`Delete ${lead.company}? This cannot be undone.`}
        confirmLabel="Delete"
        tone="danger"
        loading={deleteMutation.isPending}
        onConfirm={() => deleteMutation.mutate()}
        onCancel={() => setConfirmingDelete(false)}
      />
      <Dialog
        open={converting}
        onClose={() => setConverting(false)}
        title="Convert qualified lead"
        footer={
          <>
            <Button
              variant="secondary"
              onClick={() => setConverting(false)}
              disabled={convertMutation.isPending}
            >
              Cancel
            </Button>
            <Button onClick={() => convertMutation.mutate()} loading={convertMutation.isPending}>
              Convert lead
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <p>
            Conversion creates or reuses an account and contact in this organization. It cannot be undone.
          </p>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={createOpportunity}
              onChange={(event) => setCreateOpportunity(event.target.checked)}
            />{" "}
            Also create an opportunity
          </label>
          {createOpportunity && (
            <>
              <Input
                label="Opportunity name"
                value={opportunityName}
                onChange={(event) => setOpportunityName(event.target.value)}
                placeholder={`${lead.company} opportunity`}
              />
              <Input
                label="Opportunity amount"
                type="number"
                min={0}
                step="any"
                value={opportunityAmount}
                onChange={(event) => setOpportunityAmount(event.target.value)}
              />
            </>
          )}
        </div>
      </Dialog>
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
