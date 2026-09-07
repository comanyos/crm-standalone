import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { Select } from "./ui/Select";
import { renderQueryError, mutationErrorMessage } from "../lib/connectErrors";
import { useCrmClients } from "../lib/useCrmClients";
import { useAuth } from "../auth/AuthProvider";
import { useToast } from "./ui/Toast";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  buildChannelLink,
  defaultOutreachMessage,
  outreachNote,
  CHANNEL_ACTIONS,
  CHANNEL_LABELS,
  OUTREACH_ACTIVITY_KIND,
  type ChannelKind,
} from "../lib/messagingLinks";
import { mergeValuesForContact, renderTemplate } from "../lib/messageTemplates";

/** Reach out to a contact on the channel they prefer, in one tap
 * (task_crm.md §3).
 *
 * The server decides whether each channel may be used — ListContactChannels
 * returns the verdict, not the ingredients — so this component renders an
 * answer rather than computing one. That is deliberate: a client that fetched
 * the address and the consent rows and decided for itself would be a second
 * implementation of domain.MayMessage, and the first time the two disagreed
 * somebody who opted out would get a message.
 *
 * Nothing is sent from here. Each action is a real link that opens the user's
 * own WhatsApp, SMS composer, dialer or mail client with the text pre-filled;
 * the user presses send. The timeline entry says "opened" for that reason.
 */
export function ContactChannelsPanel({
  contactId,
  firstName,
  lastName = "",
  email = "",
  accountName = "",
}: {
  contactId: string;
  firstName: string;
  lastName?: string;
  email?: string;
  accountName?: string;
}) {
  const { crm } = useCrmClients();
  const { hasScope, session } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [message, setMessage] = useState(() => defaultOutreachMessage(firstName));

  const canRead = hasScope("crm:consent:read");
  const canRecordConsent = hasScope("crm:consent:write");
  const canLog = hasScope("crm:activity:write");
  const canUseTemplates = hasScope("crm:template:read");

  const channelsKey = ["crm.listContactChannels", contactId] as const;
  const channelsQuery = useQuery({
    queryKey: channelsKey,
    queryFn: () => crm.listContactChannels({ contactId }),
    enabled: canRead && !!contactId,
  });

  const templatesQuery = useQuery({
    queryKey: ["crm.listMessageTemplates"],
    queryFn: () => crm.listMessageTemplates({}),
    enabled: canUseTemplates,
  });

  /** Applies a template to the message box.
   *
   * The substitution happens here, from the record already on this page, so
   * nothing on the server ever expands a placeholder — see
   * lib/messageTemplates.ts for why that is what makes merge fields possible
   * at all in this version. */
  const applyTemplate = (templateId: string) => {
    const template = templatesQuery.data?.templates.find((t) => t.id === templateId);
    if (!template) return;
    setMessage(
      renderTemplate(
        template.body,
        mergeValuesForContact({ firstName, lastName, email }, accountName, session?.claims.email ?? ""),
      ),
    );
  };

  const logOutreach = useMutation({
    mutationFn: (vars: { kind: ChannelKind; address: string }) =>
      crm.createActivity({
        activity: {
          kind: OUTREACH_ACTIVITY_KIND,
          notes: outreachNote(vars.kind, vars.address, message),
          subjectType: "contact",
          subjectId: contactId,
          actorId: session?.claims.sub ?? "",
          occurredAt: timestampFromDate(new Date()),
        },
      }),
    onSuccess: () =>
      void queryClient.invalidateQueries({
        queryKey: ["crm.listActivitiesForSubject", "contact", contactId],
      }),
    // A failed log must not look like a failed message. The hand-off already
    // happened — the anchor navigated — so the honest report is that the
    // timeline entry is missing, not that the outreach did not occur.
    onError: (error) =>
      toast.show({
        tone: "danger",
        message: `Opened, but the timeline entry failed to save: ${mutationErrorMessage(error)}`,
      }),
  });

  const recordConsent = useMutation({
    mutationFn: (vars: { kind: ChannelKind; address: string; state: "granted" | "withdrawn" }) =>
      crm.recordMessagingConsent({
        channelKind: vars.kind,
        address: vars.address,
        state: vars.state,
        source: "manual",
      }),
    onSuccess: (_res, vars) => {
      void queryClient.invalidateQueries({ queryKey: channelsKey });
      toast.show({
        tone: "success",
        message:
          vars.state === "withdrawn"
            ? `Recorded: do not contact on ${CHANNEL_LABELS[vars.kind]}.`
            : `Recorded: ${CHANNEL_LABELS[vars.kind]} contact permitted again.`,
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (!canRead) return null;

  if (channelsQuery.error) {
    return (
      <section
        aria-labelledby="channels-heading"
        className="mt-4 rounded-lg border border-slate-200 bg-white p-4"
      >
        <h2 id="channels-heading" className="mb-2 text-sm font-semibold text-slate-900">
          Reach out
        </h2>
        {renderQueryError(channelsQuery.error, { onRetry: () => channelsQuery.refetch() })}
      </section>
    );
  }

  const channels = channelsQuery.data?.channels ?? [];
  if (!channelsQuery.isPending && channels.length === 0) {
    return (
      <section
        aria-labelledby="channels-heading"
        className="mt-4 rounded-lg border border-slate-200 bg-white p-4"
      >
        <h2 id="channels-heading" className="mb-2 text-sm font-semibold text-slate-900">
          Reach out
        </h2>
        <p className="text-sm text-slate-500">No email address or phone number on this contact yet.</p>
      </section>
    );
  }

  return (
    <section
      aria-labelledby="channels-heading"
      className="mt-4 rounded-lg border border-slate-200 bg-white p-4"
    >
      <h2 id="channels-heading" className="mb-2 text-sm font-semibold text-slate-900">
        Reach out
      </h2>

      {canUseTemplates && (templatesQuery.data?.templates.length ?? 0) > 0 && (
        <div className="mb-3">
          <Select
            label="Start from a template"
            value=""
            onChange={(event) => applyTemplate(event.target.value)}
            options={[
              { value: "", label: "Write it myself" },
              ...(templatesQuery.data?.templates ?? []).map((template) => ({
                value: template.id,
                label: `${CHANNEL_LABELS[template.channelKind as ChannelKind] ?? template.channelKind} — ${template.name}`,
              })),
            ]}
          />
        </div>
      )}

      <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
        Message
        <textarea
          value={message}
          onChange={(event) => setMessage(event.target.value)}
          rows={3}
          maxLength={2000}
          className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-100"
          placeholder="What do you want to say?"
        />
      </label>
      <p className="mt-1 text-xs text-slate-500">
        Opens your own app with this text ready to send — nothing is sent from here.
      </p>

      <ul className="mt-3 flex flex-col gap-2">
        {channels.map((channel) => {
          const kind = channel.kind as ChannelKind;
          const label = CHANNEL_LABELS[kind] ?? channel.kind;
          const href = channel.mayMessage
            ? buildChannelLink(kind, channel.address, kind === "voice" ? "" : message)
            : null;

          return (
            <li key={channel.kind} className="flex flex-wrap items-center gap-2 text-sm">
              <span className="w-20 shrink-0 font-medium text-slate-700">{label}</span>
              <span className="min-w-0 flex-1 truncate text-slate-500">{channel.address}</span>

              {href ? (
                <a
                  href={href}
                  {...(kind === "whatsapp" ? { target: "_blank", rel: "noopener noreferrer" } : {})}
                  onClick={() => {
                    // Fire and forget: the anchor navigates natively either
                    // way. Awaiting the log would delay the hand-off, and a
                    // popup blocker treats a deferred navigation as unsolicited.
                    if (canLog) logOutreach.mutate({ kind, address: channel.address });
                  }}
                  className="inline-flex items-center justify-center gap-2 rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-medium text-slate-900 transition-colors hover:bg-slate-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-600"
                >
                  {CHANNEL_ACTIONS[kind] ?? label}
                  <span className="sr-only"> {channel.address}</span>
                </a>
              ) : (
                <span className="text-xs text-slate-500">
                  {channel.consentState === "withdrawn" ? (
                    <Badge tone="warning">Do not contact</Badge>
                  ) : (
                    channel.unreachableReason || "Unavailable"
                  )}
                </span>
              )}

              {canRecordConsent &&
                (channel.consentState === "withdrawn" ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    loading={recordConsent.isPending}
                    onClick={() => recordConsent.mutate({ kind, address: channel.address, state: "granted" })}
                  >
                    Allow again<span className="sr-only"> on {label}</span>
                  </Button>
                ) : (
                  channel.reachable && (
                    <Button
                      variant="ghost"
                      size="sm"
                      loading={recordConsent.isPending}
                      onClick={() =>
                        recordConsent.mutate({ kind, address: channel.address, state: "withdrawn" })
                      }
                    >
                      Opt out<span className="sr-only"> of {label}</span>
                    </Button>
                  )
                ))}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
