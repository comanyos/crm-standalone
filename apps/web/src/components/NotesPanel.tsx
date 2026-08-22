import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Note } from "@crm/contracts";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";
import { ConfirmDialog } from "./ui/ConfirmDialog";
import { SkeletonRows } from "./ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../lib/connectErrors";
import { useCrmClients } from "../lib/useCrmClients";
import { useAuth } from "../auth/AuthProvider";
import { useToast } from "./ui/Toast";

/** Notes on one record.
 *
 * Deliberately separate from the activity timeline next to it. The timeline
 * is an immutable log of what happened; these are the working notes people
 * keep about an account and correct as they learn more. Putting an editable
 * note into the timeline would either make the log mutable or produce six
 * near-identical entries where the reader wanted one current answer.
 */
export function NotesPanel({
  subjectType,
  subjectId,
}: {
  subjectType: "account" | "contact" | "lead" | "opportunity";
  subjectId: string;
}) {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [composing, setComposing] = useState(false);
  const [editing, setEditing] = useState<Note | null>(null);
  const [deleting, setDeleting] = useState<Note | null>(null);
  const [body, setBody] = useState("");

  const canWrite = hasScope("crm:note:write");
  const key = ["crm.listNotes", subjectType, subjectId] as const;
  const query = useQuery({
    queryKey: key,
    queryFn: () => crm.listNotes({ subjectType, subjectId, pageSize: 50 }),
    enabled: !!subjectId,
  });

  const done = (message: string) => {
    setComposing(false);
    setEditing(null);
    setDeleting(null);
    setBody("");
    queryClient.invalidateQueries({ queryKey: key });
    toast.show({ tone: "success", message });
  };
  const failed = (error: unknown) => toast.show({ tone: "danger", message: mutationErrorMessage(error) });

  const create = useMutation({
    mutationFn: () => crm.createNote({ note: { body: body.trim(), subjectType, subjectId } }),
    onSuccess: () => done("Note added."),
    onError: failed,
  });

  const update = useMutation({
    // updatedAt round-trips the version being edited, so two people editing
    // the same note produce a conflict rather than one silently overwriting
    // the other.
    mutationFn: () =>
      crm.updateNote({ note: { id: editing?.id ?? "", body: body.trim(), updatedAt: editing?.updatedAt } }),
    onSuccess: () => done("Note updated."),
    onError: failed,
  });

  const remove = useMutation({
    mutationFn: () => crm.deleteNote({ id: deleting?.id ?? "" }),
    onSuccess: () => done("Note deleted."),
    onError: failed,
  });

  // Rendered per dialog rather than shared, and only while that dialog is
  // open: two mounted copies of the same labelled field are two elements
  // answering to "Note", which is ambiguous to a screen reader and to a test
  // for the same reason.
  const editor = (
    <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">
      Note
      <textarea
        value={body}
        onChange={(event) => setBody(event.target.value)}
        maxLength={50000}
        rows={6}
        className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-100"
        placeholder="What should the next person to open this record know?"
      />
    </label>
  );

  return (
    <section className="mt-4 max-w-2xl rounded-lg border border-slate-200 bg-white p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-slate-900">Notes</h2>
        {canWrite && (
          <Button
            size="sm"
            onClick={() => {
              setBody("");
              setComposing(true);
            }}
          >
            Add note
          </Button>
        )}
      </div>

      {query.isPending && <SkeletonRows rows={2} />}
      {query.error &&
        renderQueryError(query.error, {
          onRetry: () => query.refetch(),
          forbiddenDescription: "You don't have permission to view notes on this record.",
        })}
      {query.data?.notes.length === 0 && <p className="text-sm text-slate-500">No notes yet.</p>}

      {query.data?.notes.length ? (
        <ul className="space-y-3">
          {query.data.notes.map((note) => (
            <li key={note.id} className="rounded-md border border-slate-200 p-3 text-sm">
              <p className="whitespace-pre-wrap text-slate-800">{note.body}</p>
              <div className="mt-2 flex items-center justify-between gap-3">
                <p className="text-xs text-slate-500">
                  {note.authorId || "Unknown author"}
                  {note.createdAt ? ` · ${timestampDate(note.createdAt).toLocaleString()}` : ""}
                  {/* Shown because a note is mutable: a reader needs to be
                      able to tell a corrected note from an original one
                      without diffing anything. */}
                  {note.editedAt ? ` · edited ${timestampDate(note.editedAt).toLocaleString()}` : ""}
                </p>
                {canWrite && (
                  <div className="flex gap-2">
                    {/* "Edit note", not "Edit". A record page already has its
                        own Edit and Delete for the record itself, and a list
                        of notes each offering a bare "Edit" tells a screen
                        reader nothing about what would be edited. */}
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => {
                        setBody(note.body);
                        setEditing(note);
                      }}
                    >
                      Edit note
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => setDeleting(note)}>
                      Delete note
                    </Button>
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      ) : null}

      <Dialog
        open={composing}
        onClose={() => setComposing(false)}
        title="Add note"
        footer={
          <>
            <Button variant="secondary" onClick={() => setComposing(false)} disabled={create.isPending}>
              Cancel
            </Button>
            <Button onClick={() => create.mutate()} loading={create.isPending} disabled={!body.trim()}>
              Save note
            </Button>
          </>
        }
      >
        {composing ? editor : null}
      </Dialog>

      <Dialog
        open={!!editing}
        onClose={() => setEditing(null)}
        title="Edit note"
        footer={
          <>
            <Button variant="secondary" onClick={() => setEditing(null)} disabled={update.isPending}>
              Cancel
            </Button>
            <Button onClick={() => update.mutate()} loading={update.isPending} disabled={!body.trim()}>
              Save changes
            </Button>
          </>
        }
      >
        {editing ? editor : null}
      </Dialog>

      <ConfirmDialog
        open={!!deleting}
        title="Delete note"
        description="This note will be removed for everyone. This cannot be undone."
        confirmLabel="Delete"
        loading={remove.isPending}
        onConfirm={() => remove.mutate()}
        onCancel={() => setDeleting(null)}
      />
    </section>
  );
}
