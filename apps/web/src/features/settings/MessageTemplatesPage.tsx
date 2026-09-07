import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Dialog } from "../../components/ui/Dialog";
import { ConfirmDialog } from "../../components/ui/ConfirmDialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Table } from "../../components/ui/Table";
import { Badge } from "../../components/ui/Badge";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useToast } from "../../components/ui/Toast";
import { mergeValuesForContact, renderTemplate } from "../../lib/messageTemplates";

type Draft = { id?: string; name: string; channelKind: string; subject: string; body: string };
const blankDraft = (): Draft => ({ name: "", channelKind: "whatsapp", subject: "", body: "" });

const CHANNEL_LABELS: Record<string, string> = {
  email: "Email",
  sms: "SMS",
  whatsapp: "WhatsApp",
};

/** A stand-in contact, so the preview shows what a real message looks like
 * rather than the raw braces. Obviously fictional on purpose: a preview built
 * from a real contact would be one more place a record's fields are rendered
 * for somebody who may not be looking at that record. */
const PREVIEW_VALUES = mergeValuesForContact(
  { firstName: "Alex", lastName: "Morgan", email: "alex.morgan@example.com" },
  "Example Ltd",
  "you@example.com",
);

export default function MessageTemplatesPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [draft, setDraft] = useState<Draft>(blankDraft());
  const [deleting, setDeleting] = useState<(typeof templates)[number] | null>(null);

  const canRead = hasScope("crm:template:read");
  const canWrite = hasScope("crm:template:write");

  const templatesQuery = useQuery({
    queryKey: ["crm.listMessageTemplates"],
    queryFn: () => crm.listMessageTemplates({}),
    enabled: canRead,
  });
  const catalogQuery = useQuery({
    queryKey: ["crm.describeMessageTemplateCatalog"],
    queryFn: () => crm.describeMessageTemplateCatalog({}),
    enabled: canRead,
  });
  const templates = templatesQuery.data?.templates ?? [];

  const closeDialog = () => {
    setDialogOpen(false);
    setDraft(blankDraft());
  };
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["crm.listMessageTemplates"] });

  const save = useMutation({
    mutationFn: () => {
      // A subject only belongs to an email template, and the server refuses
      // one anywhere else — so it is dropped here rather than sent and
      // bounced back as a validation error about a field the author cannot
      // see once they have switched channel.
      const subject = draft.channelKind === "email" ? draft.subject.trim() : "";
      const payload = {
        name: draft.name.trim(),
        channelKind: draft.channelKind,
        subject,
        body: draft.body.trim(),
      };
      return draft.id
        ? crm.updateMessageTemplate({ id: draft.id, ...payload }).then(() => undefined)
        : crm.createMessageTemplate(payload).then(() => undefined);
    },
    onSuccess: () => {
      closeDialog();
      void refresh();
      toast.show({ tone: "success", message: "Template saved." });
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  const remove = useMutation({
    mutationFn: (id: string) => crm.deleteMessageTemplate({ id }),
    onSuccess: () => {
      setDeleting(null);
      void refresh();
      toast.show({ tone: "success", message: "Template deleted." });
    },
    onError: (error) => {
      setDeleting(null);
      toast.show({ tone: "danger", message: mutationErrorMessage(error) });
    },
  });

  if (!canRead) {
    return (
      <div>
        <PageHeader title="Message templates" />
        <EmptyState
          title="Access required"
          description="Viewing message templates requires the crm:template:read scope."
        />
      </div>
    );
  }

  const startEdit = (template: (typeof templates)[number]) => {
    setDraft({
      id: template.id,
      name: template.name,
      channelKind: template.channelKind,
      subject: template.subject,
      body: template.body,
    });
    setDialogOpen(true);
  };

  const mergeFields = catalogQuery.data?.mergeFields ?? [];

  return (
    <div>
      <PageHeader
        title="Message templates"
        actions={
          canWrite && (
            <Button
              onClick={() => {
                setDraft(blankDraft());
                setDialogOpen(true);
              }}
            >
              New template
            </Button>
          )
        }
      />
      <p className="mb-5 max-w-3xl text-sm text-slate-600">
        Wording your team reuses when reaching out from a contact's page. Templates are shared across the
        whole organization. A body may include the placeholders listed in the editor; they are filled in from
        the record you are looking at, in your browser.
      </p>

      {templatesQuery.isPending && <SkeletonRows rows={4} />}
      {templatesQuery.error &&
        renderQueryError(templatesQuery.error, {
          onRetry: () => templatesQuery.refetch(),
          forbiddenDescription: "You don't have permission to view message templates.",
        })}
      {templatesQuery.data && templates.length === 0 && (
        <EmptyState
          title="No templates yet"
          description="Create one so your team does not retype the same opener."
          action={
            canWrite && (
              <Button
                onClick={() => {
                  setDraft(blankDraft());
                  setDialogOpen(true);
                }}
              >
                New template
              </Button>
            )
          }
        />
      )}

      {templates.length > 0 && (
        <Table
          caption="Message templates"
          rows={templates}
          getRowKey={(template) => template.id}
          columns={[
            { key: "name", header: "Template", rowHeader: true, render: (t) => t.name },
            {
              key: "channel",
              header: "Channel",
              render: (t) => <Badge tone="neutral">{CHANNEL_LABELS[t.channelKind] ?? t.channelKind}</Badge>,
            },
            {
              key: "preview",
              header: "Body",
              render: (t) => <span className="text-slate-600">{t.body.slice(0, 80)}</span>,
            },
            {
              key: "actions",
              header: "Row actions",
              headerHidden: true,
              render: (t) =>
                canWrite && (
                  <div className="flex justify-end gap-2">
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => startEdit(t)}
                      aria-label={`Edit template ${t.name}`}
                    >
                      Edit
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => setDeleting(t)}
                      aria-label={`Delete template ${t.name}`}
                    >
                      Delete
                    </Button>
                  </div>
                ),
            },
          ]}
        />
      )}

      <Dialog
        open={dialogOpen}
        onClose={closeDialog}
        title={draft.id ? "Edit template" : "New template"}
        footer={
          <>
            <Button variant="secondary" onClick={closeDialog} disabled={save.isPending}>
              Cancel
            </Button>
            <Button
              onClick={() => save.mutate()}
              loading={save.isPending}
              disabled={!draft.name.trim() || !draft.body.trim()}
            >
              Save template
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
          <Select
            label="Channel"
            value={draft.channelKind}
            onChange={(event) => setDraft({ ...draft, channelKind: event.target.value })}
            options={(catalogQuery.data?.channelKinds ?? ["email", "sms", "whatsapp"]).map((kind) => ({
              value: kind,
              label: CHANNEL_LABELS[kind] ?? kind,
            }))}
          />
          {draft.channelKind === "email" && (
            <Input
              label="Subject"
              value={draft.subject}
              onChange={(event) => setDraft({ ...draft, subject: event.target.value })}
            />
          )}
          <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
            Body
            <textarea
              value={draft.body}
              onChange={(event) => setDraft({ ...draft, body: event.target.value })}
              rows={5}
              maxLength={2000}
              className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-100"
              placeholder="Hi {{contact.first_name}},"
            />
          </label>

          {mergeFields.length > 0 && (
            <div className="text-xs text-slate-600">
              <p className="mb-1 font-medium">Placeholders you can use:</p>
              <ul className="flex flex-wrap gap-1">
                {mergeFields.map((field) => (
                  <li key={field}>
                    <button
                      type="button"
                      className="rounded bg-slate-100 px-1.5 py-0.5 font-mono hover:bg-slate-200"
                      onClick={() => setDraft({ ...draft, body: `${draft.body}{{${field}}}` })}
                    >
                      {`{{${field}}}`}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {draft.body.trim() && (
            <div className="rounded-md border border-slate-200 bg-slate-50 p-3">
              <p className="mb-1 text-xs font-medium uppercase tracking-wide text-slate-500">Preview</p>
              <p className="whitespace-pre-wrap text-sm text-slate-800">
                {renderTemplate(draft.body, PREVIEW_VALUES)}
              </p>
            </div>
          )}
        </div>
      </Dialog>

      <ConfirmDialog
        open={!!deleting}
        title={deleting ? `Delete ${deleting.name}?` : ""}
        description="Messages already sent are unaffected — they were recorded as their own timeline entries. There is no undo for the template itself."
        confirmLabel="Delete template"
        loading={remove.isPending}
        onConfirm={() => {
          if (deleting) remove.mutate(deleting.id);
        }}
        onCancel={() => setDeleting(null)}
      />
    </div>
  );
}
