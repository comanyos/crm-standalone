import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { MetaConnection } from "@crm/contracts";
import { useSearchParams } from "react-router-dom";
import { Badge, type BadgeTone } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Select } from "../../components/ui/Select";
import { useToast } from "../../components/ui/Toast";
import { useAuth } from "../../auth/AuthProvider";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";

const RETURN_PATH = "/settings/integrations";

const STATUS_LABEL: Record<string, string> = {
  selection_required: "Choose Business and form",
  connected: "Connected",
  reauthorization_required: "Reconnect required",
  disconnected: "Disconnected",
};

const STATUS_TONE: Record<string, BadgeTone> = {
  selection_required: "warning",
  connected: "success",
  reauthorization_required: "warning",
  disconnected: "neutral",
};

const CALLBACK_REASON: Record<string, string> = {
  access_denied: "You declined Meta's permissions, so nothing was connected.",
  provider_error: "Meta could not complete the authorization. Please try again.",
  invalid_request: "Meta returned an incomplete authorization. Please try again.",
  invalid_state: "That connection link expired or was already used. Start again here.",
  exchange_failed: "Meta rejected the authorization code. Please reconnect.",
  permission_missing:
    "Meta did not grant every Lead Ads permission. Confirm Page access and approve all requested permissions.",
  storage_failed: "The connection could not be saved. Please contact your administrator.",
  not_configured: "Meta Lead Ads is not configured on this deployment.",
};

function permissionLabel(value: string): string {
  const labels: Record<string, string> = {
    pages_show_list: "See the Facebook Pages you manage",
    pages_read_engagement: "Read Page details needed for lead forms",
    pages_manage_metadata: "Subscribe the Page to Lead Ads webhooks",
    leads_retrieval: "Retrieve submitted Facebook and Instagram leads",
    business_management: "Choose the Meta Business that owns the Page",
    ads_read: "Read ad, ad set and campaign attribution",
  };
  return labels[value] ?? value;
}

function formatMoment(value: MetaConnection["connectedAt"]): string {
  return value ? timestampDate(value).toLocaleString() : "";
}

function MetaMark() {
  return (
    <svg aria-hidden="true" viewBox="0 0 48 28" className="h-6 w-10 text-[#0866ff]">
      <path
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="4.2"
        d="M3 23C8 5 14 3 19 12l5 8 5-8c5-9 11-7 16 11M3 23c3 4 7 3 11-3l5-8m10 0 5 8c4 6 8 7 11 3"
      />
    </svg>
  );
}

export function MetaLeadAdsCard() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const [businessId, setBusinessId] = useState("");
  const [pageId, setPageId] = useState("");
  const [formId, setFormId] = useState("");
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false);

  const canRead = hasScope("crm:integration:read");
  const canWrite = hasScope("crm:integration:write");
  const query = useQuery({
    queryKey: ["crm.getMetaConnection"],
    queryFn: () => crm.getMetaConnection({}),
    enabled: canRead,
  });
  const connection = query.data?.connection;
  const configured = query.data?.configured ?? false;
  const status = connection?.status ?? "";
  const requestedPermissions = useMemo(
    () => query.data?.requestedPermissions ?? [],
    [query.data],
  );
  const webhookUrl = query.data?.webhookUrl ?? "";

  const businesses = useQuery({
    queryKey: ["crm.listMetaBusinesses"],
    queryFn: () => crm.listMetaBusinesses({}),
    enabled: canRead && configured && status === "selection_required",
  });
  const activeBusinessId =
    businessId || connection?.businessId || businesses.data?.businesses[0]?.id || "";
  const pages = useQuery({
    queryKey: ["crm.listMetaPages", activeBusinessId],
    queryFn: () => crm.listMetaPages({ businessId: activeBusinessId }),
    enabled: canRead && !!activeBusinessId && status === "selection_required",
  });
  const activePageId = pageId || connection?.pageId || pages.data?.pages[0]?.id || "";
  const forms = useQuery({
    queryKey: ["crm.listMetaForms", activeBusinessId, activePageId],
    queryFn: () => crm.listMetaForms({ businessId: activeBusinessId, pageId: activePageId }),
    enabled: canRead && !!activeBusinessId && !!activePageId && status === "selection_required",
  });
  const activeFormId = formId || connection?.formId || "";

  const outcome = searchParams.get("meta");
  const outcomeReason = searchParams.get("meta_reason") ?? "";
  useEffect(() => {
    if (!outcome) return;
    toast.show({
      tone: outcome === "select_form" ? "success" : "danger",
      message:
        outcome === "select_form"
          ? "Meta authorized. Choose the Business, Facebook Page and Instant Form to receive leads from."
          : (CALLBACK_REASON[outcomeReason] ?? "Meta was not connected. Please try again."),
    });
    void queryClient.invalidateQueries({ queryKey: ["crm.getMetaConnection"] });
    const next = new URLSearchParams(searchParams);
    next.delete("meta");
    next.delete("meta_reason");
    setSearchParams(next, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [outcome, outcomeReason]);

  const start = useMutation({
    mutationFn: () => crm.startMetaConnection({ returnPath: RETURN_PATH }),
    onSuccess: (response) => window.location.assign(response.authorizationUrl),
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const select = useMutation({
    mutationFn: () =>
      crm.selectMetaLeadForm({
        businessId: activeBusinessId,
        pageId: activePageId,
        formId: activeFormId,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["crm.getMetaConnection"] });
      toast.show({
        tone: "success",
        message: "Meta Lead Ads connected. New Facebook and Instagram form leads will arrive automatically.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const importLeads = useMutation({
    mutationFn: () => crm.importMetaLeads({ limit: 500 }),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: ["crm.getMetaConnection"] });
      toast.show({
        tone: "success",
        message: `Meta checked ${result.examined} lead${result.examined === 1 ? "" : "s"}: ${result.created} added, ${result.duplicates} already present, ${result.rejected} rejected because required identity data was invalid or missing.`,
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const disconnect = useMutation({
    mutationFn: () => crm.disconnectMetaConnection({ purge: false }),
    onSuccess: () => {
      setConfirmingDisconnect(false);
      void queryClient.invalidateQueries({ queryKey: ["crm.getMetaConnection"] });
      toast.show({ tone: "success", message: "Meta access revoked and stored tokens erased." });
    },
    onError: (error) => {
      setConfirmingDisconnect(false);
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    },
  });

  if (!canRead) return null;

  return (
    <>
      <section
        aria-labelledby="meta-connection-heading"
        className="mt-5 max-w-3xl rounded-lg border border-slate-200 bg-white p-5"
      >
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-start gap-3">
            <MetaMark />
            <div>
              <h2 id="meta-connection-heading" className="text-sm font-semibold text-slate-900">
                Meta Lead Ads
              </h2>
              <p className="mt-1 text-sm text-slate-600">
                Facebook and Instagram Instant Forms
              </p>
            </div>
          </div>
          {status && (
            <Badge tone={STATUS_TONE[status] ?? "neutral"}>{STATUS_LABEL[status] ?? status}</Badge>
          )}
        </div>

        {query.error && renderQueryError(query.error, { onRetry: () => query.refetch() })}
        {query.data && !configured && (
          <p className="mt-4 rounded-md bg-slate-50 p-3 text-sm text-slate-700">
            Meta Lead Ads is not configured on the server yet. Add the Meta App credentials,
            encryption key and webhook verification token to enable connection.
          </p>
        )}

        {configured && !status && (
          <>
            <p className="mt-4 max-w-prose text-sm text-slate-600">
              Authorize the Meta Business account that owns your Facebook Page. Savvy CRM will
              then let you choose the exact Page and form; it never sends access tokens to this
              browser.
            </p>
            {requestedPermissions.length > 0 && (
              <details className="mt-3 text-sm text-slate-600">
                <summary className="cursor-pointer font-medium text-slate-900">
                  Permissions Savvy CRM will request
                </summary>
                <ul className="mt-2 list-inside list-disc">
                  {requestedPermissions.map((permission) => (
                    <li key={permission}>{permissionLabel(permission)}</li>
                  ))}
                </ul>
              </details>
            )}
          </>
        )}

        {configured && status === "selection_required" && (
          <div className="mt-4 space-y-3 border-t border-slate-200 pt-4">
            <p className="text-sm text-slate-600">
              Authorized as <strong className="text-slate-900">{connection?.metaUserName}</strong>.
              Choose the Business, Page and active Instant Form whose Facebook and Instagram
              leads belong in this workspace.
            </p>
            {businesses.error &&
              renderQueryError(businesses.error, { onRetry: () => businesses.refetch() })}
            <Select
              label="Meta Business"
              value={activeBusinessId}
              onChange={(event) => {
                setBusinessId(event.target.value);
                setPageId("");
                setFormId("");
              }}
              options={[
                ...(businesses.data?.businesses.length ? [] : [{ value: "", label: "No Business available" }]),
                ...(businesses.data?.businesses ?? []).map((business) => ({
                  value: business.id,
                  label: business.name,
                })),
              ]}
            />
            {pages.error && renderQueryError(pages.error, { onRetry: () => pages.refetch() })}
            <Select
              label="Facebook Page"
              value={activePageId}
              onChange={(event) => {
                setPageId(event.target.value);
                setFormId("");
              }}
              options={[
                ...(pages.data?.pages.length ? [] : [{ value: "", label: "No Page available" }]),
                ...(pages.data?.pages ?? []).map((page) => ({ value: page.id, label: page.name })),
              ]}
            />
            {forms.error && renderQueryError(forms.error, { onRetry: () => forms.refetch() })}
            <Select
              label="Instant Form"
              value={activeFormId}
              onChange={(event) => setFormId(event.target.value)}
              options={[
                { value: "", label: "Choose a form" },
                ...(forms.data?.forms ?? []).map((form) => ({
                  value: form.id,
                  label: `${form.name}${form.status ? ` · ${form.status}` : ""}`,
                })),
              ]}
            />
            {canWrite && (
              <Button
                loading={select.isPending}
                disabled={!activeBusinessId || !activePageId || !activeFormId}
                onClick={() => select.mutate()}
              >
                Connect this form
              </Button>
            )}
          </div>
        )}

        {configured && status === "connected" && connection && (
          <div className="mt-4 border-t border-slate-200 pt-4">
            <dl className="grid gap-3 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-slate-500">Meta Business</dt>
                <dd className="font-medium text-slate-900">{connection.businessName}</dd>
              </div>
              <div>
                <dt className="text-slate-500">Facebook Page</dt>
                <dd className="font-medium text-slate-900">{connection.pageName}</dd>
              </div>
              <div>
                <dt className="text-slate-500">Instant Form</dt>
                <dd className="font-medium text-slate-900">{connection.formName}</dd>
              </div>
              <div>
                <dt className="text-slate-500">Last live lead</dt>
                <dd className="font-medium text-slate-900">
                  {formatMoment(connection.lastWebhookAt) || "Waiting for a test lead"}
                </dd>
              </div>
              <div>
                <dt className="text-slate-500">Last history import</dt>
                <dd className="font-medium text-slate-900">
                  {formatMoment(connection.lastImportAt) || "Not run"}
                </dd>
              </div>
            </dl>
            {webhookUrl && (
              <p className="mt-3 break-all rounded-md bg-slate-50 p-3 text-xs text-slate-600">
                Webhook callback: <span className="font-mono">{webhookUrl}</span>
              </p>
            )}
            <p className="mt-3 text-sm text-slate-600">
              Use Meta's{" "}
              <a
                className="font-medium text-brand-700 underline"
                href="https://developers.facebook.com/tools/lead-ads-testing/"
                target="_blank"
                rel="noreferrer"
              >
                Lead Ads Testing Tool
              </a>{" "}
              once. “Last live lead” changing confirms the full webhook-to-CRM path.
            </p>
            {canWrite && (
              <div className="mt-4 flex flex-wrap gap-2">
                <Button loading={importLeads.isPending} onClick={() => importLeads.mutate()}>
                  Import recent leads
                </Button>
                <Button variant="secondary" loading={start.isPending} onClick={() => start.mutate()}>
                  Reconnect / change form
                </Button>
                <Button variant="secondary" onClick={() => setConfirmingDisconnect(true)}>
                  Disconnect
                </Button>
              </div>
            )}
          </div>
        )}

        {configured && status === "reauthorization_required" && (
          <div className="mt-4">
            <p className="rounded-md bg-warning-50 p-3 text-sm text-warning-700">
              Meta no longer accepts this connection. Reauthorize the Business account to resume
              Facebook and Instagram lead delivery.
            </p>
            {canWrite && (
              <Button className="mt-3" loading={start.isPending} onClick={() => start.mutate()}>
                Reconnect Meta
              </Button>
            )}
          </div>
        )}

        {configured && status === "disconnected" && canWrite && (
          <Button className="mt-4" loading={start.isPending} onClick={() => start.mutate()}>
            Connect Meta
          </Button>
        )}
        {configured && !status && canWrite && (
          <Button className="mt-4" loading={start.isPending} onClick={() => start.mutate()}>
            Connect Meta
          </Button>
        )}
      </section>

      <ConfirmDialog
        open={confirmingDisconnect}
        title="Disconnect Meta Lead Ads?"
        description="Savvy CRM will revoke the Meta grant and erase its stored encrypted tokens. Existing CRM leads and attribution stay available, but new Facebook and Instagram leads stop arriving."
        confirmLabel="Disconnect Meta"
        loading={disconnect.isPending}
        onConfirm={() => disconnect.mutate()}
        onCancel={() => setConfirmingDisconnect(false)}
      />
    </>
  );
}
