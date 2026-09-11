import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { CalendarSyncStatus, GmailSyncStatus, GoogleConnection } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Badge, type BadgeTone } from "../../components/ui/Badge";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useToast } from "../../components/ui/Toast";
import { MetaLeadAdsCard } from "./MetaLeadAdsCard";

/** Where the OAuth callback sends the browser back to. It is sent to the
 * server at Start and stored with the pending authorization, rather than
 * carried through Google and read out of the callback — a return address that
 * survives a round trip through a third party is a return address a third
 * party can rewrite. */
const RETURN_PATH = "/settings/integrations";

const STATUS_LABEL: Record<string, string> = {
  connected: "Connected",
  reauthorization_required: "Reconnection needed",
  disconnected: "Disconnected",
};

const STATUS_TONE: Record<string, BadgeTone> = {
  connected: "success",
  reauthorization_required: "warning",
  disconnected: "neutral",
};

/** The bounded reason codes the server's callback can report, rendered as
 * something a person can act on. An unrecognized code falls back to a generic
 * message rather than being printed: the set is the server's to define, and
 * echoing an unknown value would put server-chosen text on the page. */
const CALLBACK_REASON: Record<string, string> = {
  access_denied: "You declined the permissions Google asked for, so nothing was connected.",
  provider_error: "Google could not complete the authorization. Please try again.",
  invalid_request: "Google's response was missing the authorization code. Please try again.",
  invalid_state:
    "That sign-in link had already been used or had expired. Start the connection again from this page.",
  exchange_failed: "Google rejected the authorization. Please try connecting again.",
  storage_failed: "The connection could not be saved. Please try again, or contact your administrator.",
  not_configured: "Google is not configured for Savvy CRM. Ask your administrator to enable it.",
};

/** Renders a scope URL as the permission it actually grants. Showing the raw
 * URL would be honest but unreadable; showing a hand-written list would drift
 * from what the server requests, so the list comes from the server and only
 * the wording is local. */
function scopeLabel(scope: string): string {
  if (scope.endsWith("/gmail.readonly")) return "Read your Gmail messages";
  if (scope.endsWith("/gmail.send")) return "Send email on your behalf";
  if (scope.endsWith("/calendar.events")) return "Read and write your calendar events";
  return scope;
}

function formatMoment(value: GoogleConnection["connectedAt"]): string {
  if (!value) return "";
  return timestampDate(value).toLocaleString();
}

/** The bounded reason codes a sync pass can record, in words a user can act
 * on. As with the callback reasons, an unrecognized code is not printed: the
 * set belongs to the server, and echoing an unknown value would put
 * server-chosen text on the page. */
const SYNC_REASON: Record<string, string> = {
  not_connected:
    "Gmail access is not available for this account. Reconnect and make sure you grant permission to read your email.",
  token_unavailable: "Google would not renew Savvy CRM's access. Reconnect your account.",
  history_expired:
    "Savvy CRM was out of sync for long enough that Google no longer has the change history. A fresh pass over recent mail is running.",
  provider_error: "Google could not be reached on the last attempt. Savvy CRM will try again shortly.",
  storage_failed: "The last pass could not be saved. If this persists, contact your administrator.",
};

const CALENDAR_SYNC_REASON: Record<string, string> = {
  not_connected:
    "Calendar access is not available for this account. Reconnect and make sure you grant permission to your calendar.",
  token_unavailable: "Google would not renew Savvy CRM's access. Reconnect your account.",
  sync_token_expired:
    "Savvy CRM was out of sync long enough that Google reset the cursor. A fresh pass over your recent and upcoming meetings is running.",
  provider_error: "Google could not be reached on the last attempt. Savvy CRM will try again shortly.",
  storage_failed: "The last pass could not be saved. If this persists, contact your administrator.",
};

/** Connected accounts. Today that means Google plus Meta Lead Ads; Microsoft
 * is deliberately not here yet because a greyed-out button is still a promise
 * this CRM has not made.
 *
 * Nothing on this page ever holds a credential. The Connect button asks the
 * server for a consent URL and navigates to it; the token exchange, the
 * sealed storage and the revocation all happen server-side, and the only
 * thing that comes back here is status. */
export default function IntegrationsPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false);

  const canRead = hasScope("crm:integration:read");
  const canWrite = hasScope("crm:integration:write");

  const query = useQuery({
    queryKey: ["crm.getGoogleConnection"],
    queryFn: () => crm.getGoogleConnection({}),
    enabled: canRead,
  });

  const connection = query.data?.connection;
  const sync: GmailSyncStatus | undefined = query.data?.gmailSync;
  const calendarSync: CalendarSyncStatus | undefined = query.data?.calendarSync;
  const configured = query.data?.configured ?? false;
  const requestedScopes = useMemo(() => query.data?.requestedScopes ?? [], [query.data]);
  const status = connection?.status ?? "";

  // The callback redirects back here with its outcome in the query string.
  // It is reported once and then cleared, so a reload does not re-announce a
  // connection that happened minutes ago.
  const outcome = searchParams.get("google");
  const outcomeReason = searchParams.get("google_reason") ?? "";
  useEffect(() => {
    if (!outcome) return;
    if (outcome === "connected") {
      toast.show({ tone: "success", message: "Google account connected." });
      void queryClient.invalidateQueries({ queryKey: ["crm.getGoogleConnection"] });
    } else {
      toast.show({
        tone: "danger",
        message:
          CALLBACK_REASON[outcomeReason] ??
          "Google was not connected. Please try again.",
      });
    }
    const next = new URLSearchParams(searchParams);
    next.delete("google");
    next.delete("google_reason");
    setSearchParams(next, { replace: true });
    // Only when the outcome itself changes; the setters are stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [outcome, outcomeReason]);

  const start = useMutation({
    mutationFn: () => crm.startGoogleConnection({ returnPath: RETURN_PATH }),
    onSuccess: (response) => {
      // A full navigation, not a popup: Google's consent screen refuses to
      // render in a frame, and a popup is the thing browsers block.
      window.location.assign(response.authorizationUrl);
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const syncNow = useMutation({
    mutationFn: () => crm.syncGoogleMailbox({}),
    onSuccess: (response) => {
      void queryClient.invalidateQueries({ queryKey: ["crm.getGoogleConnection"] });
      // The count that means something to a user is entries added to their
      // records, not messages Gmail was asked about — most mail has no CRM
      // counterparty and is never recorded.
      const created = response.entriesCreated;
      toast.show({
        tone: "success",
        message:
          created > 0
            ? `Added ${created} email${created === 1 ? "" : "s"} to your contacts' timelines.`
            : "Mailbox checked. No new email involving your contacts.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const syncCalendarNow = useMutation({
    mutationFn: () => crm.syncGoogleCalendar({}),
    onSuccess: (response) => {
      void queryClient.invalidateQueries({ queryKey: ["crm.getGoogleConnection"] });
      const created = response.entriesCreated;
      toast.show({
        tone: "success",
        message:
          created > 0
            ? `Recorded ${created} meeting update${created === 1 ? "" : "s"} on your contacts' timelines.`
            : "Calendar checked. Nothing new involving your contacts.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const disconnect = useMutation({
    mutationFn: (purge: boolean) => crm.disconnectGoogleConnection({ purge }),
    onSuccess: () => {
      setConfirmingDisconnect(false);
      void queryClient.invalidateQueries({ queryKey: ["crm.getGoogleConnection"] });
      toast.show({
        tone: "success",
        message: "Google access has been revoked and the stored credentials erased.",
      });
    },
    onError: (error) => {
      setConfirmingDisconnect(false);
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    },
  });

  if (!canRead) {
    return (
      <div>
        <PageHeader title="Connected accounts" />
        <EmptyState
          title="You don't have access to connected accounts"
          description="Ask an administrator to grant you permission to manage connected accounts."
        />
      </div>
    );
  }

  return (
    <div>
      <PageHeader title="Connected accounts" />
      <p className="mb-5 max-w-3xl text-sm text-slate-600">
        Connect Google for your email and calendar activity, or connect a Meta Business, Page and
        Instant Form to capture Facebook and Instagram leads. Credentials remain server-side;
        each connection can be revoked from here.
      </p>

      {query.isPending && <SkeletonRows rows={3} />}
      {query.error &&
        renderQueryError(query.error, {
          onRetry: () => query.refetch(),
          forbiddenDescription: "You don't have permission to view connected accounts.",
        })}

      {query.data && (
        <section
          aria-labelledby="google-connection-heading"
          className="max-w-3xl rounded-lg border border-slate-200 bg-white p-5"
        >
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 id="google-connection-heading" className="text-sm font-semibold text-slate-900">
                Google
              </h2>
              <p className="mt-1 text-sm text-slate-600">Gmail and Google Calendar</p>
            </div>
            {status && <Badge tone={STATUS_TONE[status] ?? "neutral"}>{STATUS_LABEL[status] ?? status}</Badge>}
          </div>

          {!configured && (
            <p className="mt-4 rounded-md bg-slate-50 p-3 text-sm text-slate-700">
              Google is not configured for Savvy CRM. An administrator needs to set the Google
              OAuth credentials before anyone can connect an account.
            </p>
          )}

          {configured && status === "connected" && connection && (
            <dl className="mt-4 grid gap-2 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-slate-500">Google account</dt>
                <dd className="font-medium text-slate-900">
                  {connection.googleAccountEmail || "Not reported by Google"}
                </dd>
              </div>
              <div>
                <dt className="text-slate-500">Connected on</dt>
                <dd className="font-medium text-slate-900">{formatMoment(connection.connectedAt)}</dd>
              </div>
              {connection.grantedScopes.length > 0 && (
                <div className="sm:col-span-2">
                  <dt className="text-slate-500">Permissions you granted</dt>
                  <dd>
                    <ul className="mt-1 list-inside list-disc text-slate-900">
                      {connection.grantedScopes.map((scope) => (
                        <li key={scope}>{scopeLabel(scope)}</li>
                      ))}
                    </ul>
                  </dd>
                </div>
              )}
            </dl>
          )}

          {configured && status === "connected" && (
            <div className="mt-5 border-t border-slate-200 pt-4">
              <h3 className="text-sm font-semibold text-slate-900">Email sync</h3>
              <p className="mt-1 max-w-prose text-sm text-slate-600">
                Savvy CRM checks your mailbox for messages involving people already in your
                contacts, and adds those to the contact's timeline. Email with no matching
                contact is never read into Savvy CRM.
              </p>

              {sync ? (
                <dl className="mt-3 grid gap-2 text-sm sm:grid-cols-2">
                  <div>
                    <dt className="text-slate-500">Emails added to timelines</dt>
                    <dd className="font-medium text-slate-900">{Number(sync.activitiesCreated)}</dd>
                  </div>
                  <div>
                    <dt className="text-slate-500">Last checked</dt>
                    <dd className="font-medium text-slate-900">
                      {formatMoment(sync.lastSyncedAt) || "Not yet"}
                    </dd>
                  </div>
                  {!sync.backfillComplete && (
                    <div className="sm:col-span-2">
                      <p className="rounded-md bg-slate-50 p-3 text-sm text-slate-700">
                        Still working through your recent mail. This runs in the background and
                        may take a few passes.
                      </p>
                    </div>
                  )}
                  {sync.lastError && SYNC_REASON[sync.lastError] && (
                    <div className="sm:col-span-2">
                      <p className="rounded-md bg-warning-50 p-3 text-sm text-warning-700">
                        {SYNC_REASON[sync.lastError]}
                      </p>
                    </div>
                  )}
                </dl>
              ) : (
                <p className="mt-3 text-sm text-slate-600">
                  Sync has not run yet. It starts automatically, or you can check now.
                </p>
              )}

              {canWrite && (
                <div className="mt-3">
                  <Button
                    variant="secondary"
                    size="sm"
                    loading={syncNow.isPending}
                    onClick={() => syncNow.mutate()}
                  >
                    Check for new email
                  </Button>
                </div>
              )}
            </div>
          )}

          {configured && status === "connected" && (
            <div className="mt-5 border-t border-slate-200 pt-4">
              <h3 className="text-sm font-semibold text-slate-900">Calendar sync</h3>
              <p className="mt-1 max-w-prose text-sm text-slate-600">
                Meetings with people already in your contacts are added to the contact's
                timeline, and the timeline is updated when one is moved or cancelled. Meetings
                with no matching contact are never read into Savvy CRM.
              </p>

              {calendarSync ? (
                <dl className="mt-3 grid gap-2 text-sm sm:grid-cols-2">
                  <div>
                    <dt className="text-slate-500">Meeting updates recorded</dt>
                    <dd className="font-medium text-slate-900">
                      {Number(calendarSync.activitiesCreated)}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-slate-500">Last checked</dt>
                    <dd className="font-medium text-slate-900">
                      {formatMoment(calendarSync.lastSyncedAt) || "Not yet"}
                    </dd>
                  </div>
                  {!calendarSync.backfillComplete && (
                    <div className="sm:col-span-2">
                      <p className="rounded-md bg-slate-50 p-3 text-sm text-slate-700">
                        Still working through your recent and upcoming meetings. This runs in the
                        background and may take a few passes.
                      </p>
                    </div>
                  )}
                  {calendarSync.lastError && CALENDAR_SYNC_REASON[calendarSync.lastError] && (
                    <div className="sm:col-span-2">
                      <p className="rounded-md bg-warning-50 p-3 text-sm text-warning-700">
                        {CALENDAR_SYNC_REASON[calendarSync.lastError]}
                      </p>
                    </div>
                  )}
                </dl>
              ) : (
                <p className="mt-3 text-sm text-slate-600">
                  Calendar sync has not run yet. It starts automatically, or you can check now.
                </p>
              )}

              {canWrite && (
                <div className="mt-3">
                  <Button
                    variant="secondary"
                    size="sm"
                    loading={syncCalendarNow.isPending}
                    onClick={() => syncCalendarNow.mutate()}
                  >
                    Check for meeting changes
                  </Button>
                </div>
              )}
            </div>
          )}

          {configured && status === "reauthorization_required" && (
            <p className="mt-4 rounded-md bg-warning-50 p-3 text-sm text-warning-700">
              Google has stopped accepting Savvy CRM's stored permission for this account — usually
              because access was withdrawn from your Google account settings. Reconnect to resume
              syncing.
            </p>
          )}

          {configured && status !== "connected" && requestedScopes.length > 0 && (
            <div className="mt-4">
              <h3 className="text-sm font-medium text-slate-900">Connecting will ask Google for</h3>
              <ul className="mt-1 list-inside list-disc text-sm text-slate-600">
                {requestedScopes.map((scope) => (
                  <li key={scope}>{scopeLabel(scope)}</li>
                ))}
              </ul>
            </div>
          )}

          {configured && canWrite && (
            <div className="mt-5 flex flex-wrap gap-2">
              {status !== "connected" && (
                <Button loading={start.isPending} onClick={() => start.mutate()}>
                  {status === "reauthorization_required" ? "Reconnect Google" : "Connect Google"}
                </Button>
              )}
              {status === "connected" && (
                <>
                  <Button variant="secondary" loading={start.isPending} onClick={() => start.mutate()}>
                    Reconnect
                  </Button>
                  <Button
                    variant="secondary"
                    loading={disconnect.isPending}
                    onClick={() => setConfirmingDisconnect(true)}
                  >
                    Disconnect
                  </Button>
                </>
              )}
            </div>
          )}

          {configured && !canWrite && (
            <p className="mt-4 text-sm text-slate-600">
              You can see this connection's status but not change it.
            </p>
          )}
        </section>
      )}

      <MetaLeadAdsCard />

      <ConfirmDialog
        open={confirmingDisconnect}
        title="Disconnect your Google account?"
        description="Savvy CRM's access is revoked at Google straight away and the stored credentials are erased. Email and calendar records already brought into Savvy CRM stay where they are. You can reconnect at any time."
        confirmLabel="Disconnect Google"
        loading={disconnect.isPending}
        onConfirm={() => disconnect.mutate(false)}
        onCancel={() => setConfirmingDisconnect(false)}
      />
    </div>
  );
}
