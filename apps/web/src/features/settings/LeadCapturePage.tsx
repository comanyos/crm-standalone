import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Dialog } from "../../components/ui/Dialog";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Input } from "../../components/ui/Input";
import { Table } from "../../components/ui/Table";
import { Badge } from "../../components/ui/Badge";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useToast } from "../../components/ui/Toast";

/** The lead fields a mapping may target — internal/domain/lead_capture.go's
 * CaptureLeadFields, which is itself the CSV import vocabulary. Kept in sync
 * by hand because the server refuses anything outside it, so drift shows up
 * as an endpoint that will not save rather than as leads with missing fields. */
const LEAD_FIELDS = ["company", "contact_email", "owner_id", "score", "source"];

const OUTCOME_TONE: Record<string, "success" | "neutral" | "warning" | "danger"> = {
  created: "success",
  duplicate: "neutral",
  rejected: "warning",
  failed: "danger",
};

/** What each bounded reason code means, in words an administrator can act on.
 * The server sends codes rather than sentences so nothing from a payload
 * reaches a metric label or a log; turning them into guidance is this page's
 * job. */
const REASON_HELP: Record<string, string> = {
  bad_signature:
    "The signature did not match. Check the signing secret in the calling system, and that it signs the exact request body.",
  endpoint_disabled: "This endpoint is switched off. Deliveries are recorded but create nothing.",
  malformed_payload: "The body was not valid JSON.",
  no_usable_fields:
    "Nothing in the payload matched this endpoint's field mapping — check the field names the caller actually sends.",
  rate_limited: "Too many deliveries in one minute. The caller should slow down or batch.",
  store_failed: "The lead could not be saved. The caller's own retry should recover it.",
  unknown_endpoint: "No endpoint matched the token in the URL.",
};

type Draft = {
  id?: string;
  name: string;
  defaultOwnerId: string;
  status: string;
  /** Rows rather than a map, so an author can edit a field name without the
   * entry vanishing the moment it is briefly blank or duplicated. */
  mapping: { from: string; to: string }[];
};

const blankDraft = (): Draft => ({
  name: "",
  defaultOwnerId: "",
  status: "active",
  mapping: [{ from: "email", to: "contact_email" }],
});

export default function LeadCapturePage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [draft, setDraft] = useState<Draft>(blankDraft());
  const [rotating, setRotating] = useState<string | null>(null);
  const [openLog, setOpenLog] = useState<string | null>(null);
  /** Shown once after a create or a rotate. There is no RPC that reads a
   * secret back, so if this is dismissed without copying it, the only way
   * forward is another rotation — which the copy says. */
  const [issued, setIssued] = useState<{ secret: string; url: string } | null>(null);

  const canRead = hasScope("crm:capture:read");
  const canWrite = hasScope("crm:capture:write");

  const endpointsQuery = useQuery({
    queryKey: ["crm.listLeadCaptureEndpoints"],
    queryFn: () => crm.listLeadCaptureEndpoints({}),
    enabled: canRead,
  });
  const endpoints = endpointsQuery.data?.endpoints ?? [];
  const baseUrl = endpointsQuery.data?.baseUrl ?? "";

  const deliveriesQuery = useQuery({
    queryKey: ["crm.listLeadCaptureDeliveries", openLog],
    queryFn: () => crm.listLeadCaptureDeliveries({ endpointId: openLog!, limit: 50 }),
    enabled: canRead && !!openLog,
  });

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["crm.listLeadCaptureEndpoints"] });
  const closeDialog = () => {
    setDialogOpen(false);
    setDraft(blankDraft());
  };

  const mappingOf = (d: Draft) =>
    Object.fromEntries(
      d.mapping.filter((row) => row.from.trim() && row.to.trim()).map((row) => [row.from.trim(), row.to]),
    );

  const save = useMutation({
    mutationFn: async () => {
      if (draft.id) {
        await crm.updateLeadCaptureEndpoint({
          id: draft.id,
          name: draft.name.trim(),
          fieldMapping: mappingOf(draft),
          defaultOwnerId: draft.defaultOwnerId.trim(),
          status: draft.status,
        });
        return null;
      }
      const res = await crm.createLeadCaptureEndpoint({
        name: draft.name.trim(),
        fieldMapping: mappingOf(draft),
        defaultOwnerId: draft.defaultOwnerId.trim(),
      });
      return { secret: res.signingSecret, url: res.deliveryUrl };
    },
    onSuccess: (result) => {
      closeDialog();
      void refresh();
      if (result) setIssued(result);
      else toast.show({ tone: "success", message: "Endpoint saved." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const rotate = useMutation({
    mutationFn: (id: string) => crm.rotateLeadCaptureSecret({ id }),
    onSuccess: (res) => {
      setRotating(null);
      void refresh();
      setIssued({
        secret: res.signingSecret,
        url: `${baseUrl}${res.endpoint?.token ?? ""}`,
      });
    },
    onError: (error) => {
      setRotating(null);
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    },
  });

  if (!canRead) {
    return (
      <div>
        <PageHeader title="Lead capture" />
        <EmptyState
          title="Access required"
          description="Viewing lead capture endpoints requires the crm:capture:read scope."
        />
      </div>
    );
  }

  const startEdit = (endpoint: (typeof endpoints)[number]) => {
    setDraft({
      id: endpoint.id,
      name: endpoint.name,
      defaultOwnerId: endpoint.defaultOwnerId,
      status: endpoint.status,
      mapping: Object.entries(endpoint.fieldMapping).map(([from, to]) => ({ from, to })),
    });
    setDialogOpen(true);
  };

  return (
    <div>
      <PageHeader
        title="Lead capture"
        actions={
          canWrite && (
            <Button
              onClick={() => {
                setDraft(blankDraft());
                setDialogOpen(true);
              }}
            >
              New endpoint
            </Button>
          )
        }
      />
      <p className="mb-5 max-w-3xl text-sm text-slate-600">
        A URL an external system can POST a lead to — a website form, an ad platform, a Zapier step. Every
        delivery must be signed with the endpoint's secret; unsigned posts are refused and recorded. Captured
        leads are ordinary leads, so your automations run on them.
      </p>

      {endpointsQuery.isPending && <SkeletonRows rows={3} />}
      {endpointsQuery.error &&
        renderQueryError(endpointsQuery.error, {
          onRetry: () => endpointsQuery.refetch(),
          forbiddenDescription: "You don't have permission to view lead capture endpoints.",
        })}
      {endpointsQuery.data && endpoints.length === 0 && (
        <EmptyState
          title="No capture endpoints yet"
          description="Create one to start receiving leads from a form or an ad platform."
          action={
            canWrite && (
              <Button
                onClick={() => {
                  setDraft(blankDraft());
                  setDialogOpen(true);
                }}
              >
                New endpoint
              </Button>
            )
          }
        />
      )}

      {endpoints.length > 0 && (
        <Table
          caption="Lead capture endpoints"
          rows={endpoints}
          getRowKey={(endpoint) => endpoint.id}
          columns={[
            { key: "name", header: "Endpoint", rowHeader: true, render: (e) => e.name },
            {
              key: "status",
              header: "Status",
              render: (e) => <Badge tone={e.status === "active" ? "success" : "neutral"}>{e.status}</Badge>,
            },
            {
              key: "last",
              header: "Last delivery",
              render: (e) =>
                e.lastDeliveryAt ? (
                  <span className={e.lastError ? "text-warning-700" : "text-slate-600"}>
                    {timestampDate(e.lastDeliveryAt).toLocaleString()}
                    {e.lastError ? ` — ${e.lastError.replace(/_/g, " ")}` : ""}
                  </span>
                ) : (
                  <span className="text-slate-400">never</span>
                ),
            },
            {
              key: "actions",
              header: "Row actions",
              headerHidden: true,
              render: (e) => (
                <div className="flex justify-end gap-2">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setOpenLog(e.id)}
                    aria-label={`Delivery log for ${e.name}`}
                  >
                    Deliveries
                  </Button>
                  {canWrite && (
                    <>
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => startEdit(e)}
                        aria-label={`Edit endpoint ${e.name}`}
                      >
                        Edit
                      </Button>
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => setRotating(e.id)}
                        aria-label={`Rotate secret for ${e.name}`}
                      >
                        Rotate secret
                      </Button>
                    </>
                  )}
                </div>
              ),
            },
          ]}
        />
      )}

      <Dialog
        open={dialogOpen}
        onClose={closeDialog}
        title={draft.id ? "Edit endpoint" : "New endpoint"}
        footer={
          <>
            <Button variant="secondary" onClick={closeDialog} disabled={save.isPending}>
              Cancel
            </Button>
            <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!draft.name.trim()}>
              Save endpoint
            </Button>
          </>
        }
      >
        <div className="grid gap-3">
          <Input
            label="Name"
            value={draft.name}
            onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            required
          />
          <Input
            label="Assign new leads to"
            value={draft.defaultOwnerId}
            onChange={(event) => setDraft({ ...draft, defaultOwnerId: event.target.value })}
          />
          {draft.id && (
            <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
              Status
              <select
                value={draft.status}
                onChange={(event) => setDraft({ ...draft, status: event.target.value })}
                className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900"
              >
                <option value="active">Active</option>
                <option value="disabled">Disabled</option>
              </select>
            </label>
          )}

          <fieldset className="rounded-md border border-slate-200 p-3">
            <legend className="px-1 text-sm font-medium text-slate-700">Field mapping</legend>
            <p className="mb-2 text-xs text-slate-500">
              Which field the caller sends, and which lead field it becomes.
            </p>
            {draft.mapping.map((row, index) => (
              <div key={index} className="mb-2 flex flex-wrap items-end gap-2">
                <Input
                  label={`Their field ${index + 1}`}
                  value={row.from}
                  onChange={(event) => {
                    const next = [...draft.mapping];
                    next[index] = { ...row, from: event.target.value };
                    setDraft({ ...draft, mapping: next });
                  }}
                />
                <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
                  {`Our field ${index + 1}`}
                  <select
                    value={row.to}
                    onChange={(event) => {
                      const next = [...draft.mapping];
                      next[index] = { ...row, to: event.target.value };
                      setDraft({ ...draft, mapping: next });
                    }}
                    className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900"
                  >
                    {LEAD_FIELDS.map((field) => (
                      <option key={field} value={field}>
                        {field}
                      </option>
                    ))}
                  </select>
                </label>
                {draft.mapping.length > 1 && (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() =>
                      setDraft({ ...draft, mapping: draft.mapping.filter((_, i) => i !== index) })
                    }
                  >
                    Remove<span className="sr-only"> mapping {index + 1}</span>
                  </Button>
                )}
              </div>
            ))}
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setDraft({ ...draft, mapping: [...draft.mapping, { from: "", to: "company" }] })}
            >
              Add a field
            </Button>
          </fieldset>
        </div>
      </Dialog>

      {/* Shown once. There is no RPC that reads a secret back, so this dialog
          is the only chance to copy it — and the copy has to say so. */}
      <Dialog
        open={!!issued}
        onClose={() => setIssued(null)}
        title="Copy the signing secret now"
        footer={<Button onClick={() => setIssued(null)}>Done</Button>}
      >
        <div className="grid gap-3 text-sm">
          <p className="text-slate-700">
            This secret is shown once and cannot be read back. If you lose it, rotate the endpoint to get a
            new one — which stops the old one working.
          </p>
          <label className="flex flex-col gap-1 font-medium text-slate-700">
            Delivery URL
            <input
              readOnly
              value={issued?.url ?? ""}
              className="rounded-md border border-slate-300 px-3 py-2 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1 font-medium text-slate-700">
            Signing secret
            <input
              readOnly
              value={issued?.secret ?? ""}
              className="rounded-md border border-slate-300 px-3 py-2 font-mono text-xs"
            />
          </label>
          <p className="text-xs text-slate-500">
            Sign the exact request body with HMAC-SHA256 and send it as{" "}
            <code className="font-mono">X-CRM-Signature: sha256=&lt;hex&gt;</code>.
          </p>
        </div>
      </Dialog>

      <Dialog
        open={!!openLog}
        onClose={() => setOpenLog(null)}
        title="Delivery log"
        footer={<Button onClick={() => setOpenLog(null)}>Close</Button>}
      >
        {deliveriesQuery.isPending && <SkeletonRows rows={3} />}
        {deliveriesQuery.data?.deliveries.length === 0 && (
          <p className="text-sm text-slate-500">Nothing has been delivered to this endpoint yet.</p>
        )}
        <ol className="space-y-3">
          {(deliveriesQuery.data?.deliveries ?? []).map((delivery) => (
            <li key={delivery.id} className="border-l-2 border-slate-200 pl-3 text-sm">
              <p className="flex items-center gap-2">
                <Badge tone={OUTCOME_TONE[delivery.outcome] ?? "neutral"}>{delivery.outcome}</Badge>
                <span className="text-xs text-slate-500">
                  {delivery.receivedAt ? timestampDate(delivery.receivedAt).toLocaleString() : ""}
                </span>
              </p>
              {delivery.reason && (
                <p className="mt-1 text-slate-600">
                  {REASON_HELP[delivery.reason] ?? delivery.reason.replace(/_/g, " ")}
                </p>
              )}
              {delivery.rawPayload && (
                <pre className="mt-1 overflow-x-auto rounded bg-slate-50 p-2 text-xs text-slate-700">
                  {delivery.rawPayload.slice(0, 500)}
                </pre>
              )}
            </li>
          ))}
        </ol>
      </Dialog>

      <ConfirmDialog
        open={!!rotating}
        title="Rotate this signing secret?"
        description="Every system configured with the current secret stops working immediately, and starts being recorded as a bad signature. Have the new secret ready to paste in before you do this."
        confirmLabel="Rotate secret"
        tone="danger"
        loading={rotate.isPending}
        onConfirm={() => {
          if (rotating) rotate.mutate(rotating);
        }}
        onCancel={() => setRotating(null)}
      />
    </div>
  );
}
