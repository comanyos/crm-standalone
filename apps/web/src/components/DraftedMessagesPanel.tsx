import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { renderQueryError, mutationErrorMessage } from "../lib/connectErrors";
import { useCrmClients } from "../lib/useCrmClients";
import { useAuth } from "../auth/AuthProvider";
import { useToast } from "./ui/Toast";
import { buildChannelLink, CHANNEL_ACTIONS, type ChannelKind } from "../lib/messagingLinks";

/** Messages an automation drafted for this record (task_crm.md §6).
 *
 * The CRM does not send them. A rule decided who to contact, checked that
 * contacting them is permitted and wrote the wording; this is where a person
 * reads it, sends it in one tap through their own app, and says they did.
 *
 * "Mark as sent" is a separate press on purpose. Opening WhatsApp is not
 * sending — the user may close it, edit the text, or change their mind — so
 * the CRM records what it actually knows rather than assuming, the same
 * distinction the click-to-chat timeline entry keeps by saying "opened".
 */
export function DraftedMessagesPanel({
  subjectType,
  subjectId,
}: {
  subjectType: "lead" | "contact";
  subjectId: string;
}) {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();

  const canRead = hasScope("crm:message:read");
  const canWrite = hasScope("crm:message:write");

  const key = ["crm.listOutboundMessages", subjectType, subjectId] as const;
  const query = useQuery({
    queryKey: key,
    queryFn: () => crm.listOutboundMessages({ subjectType, subjectId, status: "pending" }),
    enabled: canRead && !!subjectId,
  });

  /** Sending from the CRM needs a connected Google account, which is per
   * user rather than per tenant. Asking here — rather than guessing from a
   * config flag the browser cannot see — means the button appears exactly
   * when it will work. */
  const connectionQuery = useQuery({
    queryKey: ["crm.getGoogleConnection"],
    queryFn: () => crm.getGoogleConnection({}),
    enabled: canWrite && hasScope("crm:integration:read"),
    // A user with no connection is the normal case, not an error.
    retry: false,
  });
  const canSendFromCrm = connectionQuery.data?.connection?.status === "connected";

  const send = useMutation({
    mutationFn: (id: string) => crm.sendOutboundMessage({ id }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: key });
      void queryClient.invalidateQueries({
        queryKey: ["crm.listActivitiesForSubject", subjectType, subjectId],
      });
      toast.show({ tone: "success", message: "Sent from your mailbox." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const resolve = useMutation({
    mutationFn: (vars: { id: string; action: "sent" | "discard" }) =>
      vars.action === "sent"
        ? crm.markOutboundMessageSent({ id: vars.id }).then(() => undefined)
        : crm.discardOutboundMessage({ id: vars.id }).then(() => undefined),
    onSuccess: (_res, vars) => {
      void queryClient.invalidateQueries({ queryKey: key });
      toast.show({
        tone: "success",
        message: vars.action === "sent" ? "Marked as sent." : "Draft discarded.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (!canRead) return null;

  const drafts = query.data?.messages ?? [];
  // An empty queue is the normal state, and a panel saying "no drafts" on
  // every record would be noise on every page.
  if (!query.error && drafts.length === 0) return null;

  return (
    <section
      aria-labelledby="drafts-heading"
      className="mt-4 rounded-lg border border-slate-200 bg-white p-4"
    >
      <h2 id="drafts-heading" className="mb-2 text-sm font-semibold text-slate-900">
        Drafted for you
      </h2>
      {query.error && renderQueryError(query.error, { onRetry: () => query.refetch() })}

      <ul className="flex flex-col gap-3">
        {drafts.map((draft) => {
          const kind = draft.channelKind as ChannelKind;
          const href = draft.mayStillSend
            ? buildChannelLink(kind, draft.address, draft.body, draft.subject)
            : null;

          return (
            <li key={draft.id} className="rounded-md border border-slate-200 p-3 text-sm">
              <div className="mb-1 flex flex-wrap items-center gap-2">
                <Badge tone="brand">{CHANNEL_ACTIONS[kind] ?? draft.channelKind}</Badge>
                <span className="text-slate-500">{draft.address}</span>
                {!draft.mayStillSend && (
                  <Badge tone="warning">
                    {draft.refusalReason === "consent_withdrawn" ? "Do not contact" : "Cannot send"}
                  </Badge>
                )}
              </div>
              {draft.subject && <p className="font-medium text-slate-800">{draft.subject}</p>}
              <p className="whitespace-pre-wrap text-slate-700">{draft.body}</p>

              {!draft.mayStillSend && (
                <p className="mt-2 text-xs text-warning-800">
                  This was drafted before they opted out. It is kept so you know it existed, and cannot be
                  sent.
                </p>
              )}

              {canWrite && (
                <div className="mt-2 flex flex-wrap gap-2">
                  {href && (
                    <a
                      href={href}
                      {...(kind === "whatsapp" ? { target: "_blank", rel: "noopener noreferrer" } : {})}
                      className="inline-flex items-center rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-medium text-slate-900 hover:bg-slate-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-600"
                    >
                      Open in {CHANNEL_ACTIONS[kind] ?? draft.channelKind}
                    </a>
                  )}
                  {canSendFromCrm && draft.channelKind === "email" && (
                    <Button
                      size="sm"
                      disabled={!draft.mayStillSend}
                      loading={send.isPending}
                      onClick={() => send.mutate(draft.id)}
                    >
                      Send from my email
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant={canSendFromCrm && draft.channelKind === "email" ? "secondary" : "primary"}
                    disabled={!draft.mayStillSend}
                    loading={resolve.isPending}
                    onClick={() => resolve.mutate({ id: draft.id, action: "sent" })}
                  >
                    Mark as sent
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    loading={resolve.isPending}
                    onClick={() => resolve.mutate({ id: draft.id, action: "discard" })}
                  >
                    Discard
                  </Button>
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
