import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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

type Draft = { id?: string; name: string; color: string };
const blankDraft = (): Draft => ({ name: "", color: "#bb431d" });

export default function TagsPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [draft, setDraft] = useState<Draft>(blankDraft());
  // The tag awaiting a delete confirmation. A ConfirmDialog rather than
  // window.confirm — see CustomFieldsPage's identical archive-confirmation
  // comment for why a native prompt is the wrong choice here.
  const [deleting, setDeleting] = useState<(typeof tags)[number] | null>(null);

  const canWrite = hasScope("crm:tag:write");
  const canRead = hasScope("crm:tag:read");
  const tagsQuery = useQuery({ queryKey: ["crm.listTags"], queryFn: () => crm.listTags({}), enabled: canRead });
  const tags = tagsQuery.data?.tags ?? [];
  const closeDialog = () => { setDialogOpen(false); setDraft(blankDraft()); };
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["crm.listTags"] });

  const save = useMutation({
    mutationFn: () => (draft.id
      ? crm.updateTag({ id: draft.id, name: draft.name.trim(), color: draft.color }).then(() => undefined)
      : crm.createTag({ name: draft.name.trim(), color: draft.color }).then(() => undefined)),
    onSuccess: () => { closeDialog(); void refresh(); toast.show({ tone: "success", message: "Tag saved." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const remove = useMutation({
    mutationFn: (id: string) => crm.deleteTag({ id }),
    onSuccess: () => { setDeleting(null); void refresh(); toast.show({ tone: "success", message: "Tag deleted." }); },
    onError: (error) => { setDeleting(null); toast.show({ tone: "danger", message: mutationErrorMessage(error) }); },
  });

  if (!canRead) {
    return (
      <div>
        <PageHeader title="Tags" />
        <EmptyState title="Access required" description="Viewing tags requires the crm:tag:read scope." />
      </div>
    );
  }

  const startEdit = (tag: (typeof tags)[number]) => {
    setDraft({ id: tag.id, name: tag.name, color: tag.color || "#bb431d" });
    setDialogOpen(true);
  };

  return (
    <div>
      <PageHeader
        title="Tags"
        actions={canWrite && <Button onClick={() => { setDraft(blankDraft()); setDialogOpen(true); }}>New tag</Button>}
      />
      <p className="mb-5 max-w-3xl text-sm text-slate-600">
        Tags are a shared, tenant-wide taxonomy. Assign or remove them from an account, contact, lead, opportunity,
        or task on that record's own page. Deleting a tag here removes it from every record it is currently on.
      </p>
      {tagsQuery.isPending && <SkeletonRows rows={4} />}
      {tagsQuery.error && renderQueryError(tagsQuery.error, {
        onRetry: () => tagsQuery.refetch(),
        forbiddenDescription: "You don't have permission to view tags.",
      })}
      {tagsQuery.data && tags.length === 0 && (
        <EmptyState
          title="No tags yet"
          description="Create a tag to start labeling records."
          action={canWrite && <Button onClick={() => { setDraft(blankDraft()); setDialogOpen(true); }}>New tag</Button>}
        />
      )}
      {tags.length > 0 && (
        <Table
          caption="Tags"
          rows={tags}
          getRowKey={(tag) => tag.id}
          columns={[
            {
              key: "name",
              header: "Tag",
              render: (tag) => (
                <Badge tone="neutral">
                  <span
                    className="mr-1 inline-block h-2 w-2 rounded-full"
                    style={{ backgroundColor: tag.color || "#8b957d" }}
                    aria-hidden="true"
                  />
                  {tag.name}
                </Badge>
              ),
            },
            {
              key: "actions",
              header: "Row actions",
              headerHidden: true,
              render: (tag) => canWrite && (
                <div className="flex justify-end gap-2">
                  <Button size="sm" variant="secondary" onClick={() => startEdit(tag)} aria-label={`Edit tag ${tag.name}`}>
                    Edit
                  </Button>
                  <Button size="sm" variant="secondary" onClick={() => setDeleting(tag)} aria-label={`Delete tag ${tag.name}`}>
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
        title={draft.id ? "Edit tag" : "New tag"}
        footer={
          <>
            <Button variant="secondary" onClick={closeDialog} disabled={save.isPending}>Cancel</Button>
            <Button onClick={() => save.mutate()} loading={save.isPending} disabled={!draft.name.trim()}>Save tag</Button>
          </>
        }
      >
        <div className="grid gap-3">
          <Input label="Name" value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} required />
          <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
            Color
            <input
              type="color"
              className="h-9 w-16 rounded-md border border-slate-300 p-1"
              value={draft.color}
              onChange={(event) => setDraft({ ...draft, color: event.target.value })}
            />
          </label>
        </div>
      </Dialog>
      <ConfirmDialog
        open={!!deleting}
        title={deleting ? `Delete ${deleting.name}?` : ""}
        description="This removes the tag from every record it is currently assigned to. There is no undo."
        confirmLabel="Delete tag"
        loading={remove.isPending}
        onConfirm={() => { if (deleting) remove.mutate(deleting.id); }}
        onCancel={() => setDeleting(null)}
      />
    </div>
  );
}
